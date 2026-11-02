package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	accountv1 "github.com/openpak/account-api/openpak/account/v1"
	"openpak/np-common/coreclient"
	"openpak/np-common/resource"
	"openpak/np-common/ticket"
)

// Wire format: research notes "score-protocol" (firmware 4.93 VSH). Replies are always HTTP 200
// with an explicit Content-Length; errors are the root result attribute (2 hex digits,
// SCE_NP_COMMUNITY_SERVER_ERROR_* minus 0x8002a400).
const (
	resultOK          = "00"
	resultBadRequest  = "01"
	resultBadTicket   = "02"
	resultInternal    = "07"
	resultNotFound    = "12" // GAME_RANKING_NOT_FOUND
	resultNotBest     = "15"
	resultNoGameData  = "18" // RANKING_GAME_DATA_MASTER_NOT_FOUND
	resultTooLarge    = "1a"
	resultBadScore    = "23" // record_data for a score the player does not hold
	resultUnspecified = "ff"

	maxXML     = 64 << 10
	maxList    = 100
	maxComment = 63
	maxInfo    = 189
	dateFmt    = "2006-01-02T15:04:05.000Z"
)

var commIDPattern = regexp.MustCompile(`^[A-Z]{4}[0-9]{5}_[0-9]{2}$`)

type Core interface {
	ticket.AccountCore
	GetLinkByOnlineID(context.Context, string) (*coreclient.Link, error)
	GetLinkByAccount(context.Context, string) (*coreclient.Link, error)
	Friends(context.Context, string) ([]*accountv1.Friend, error)
}

// Board is the per-board configuration a title's developer set on Sony's side.
type Board struct {
	Name      string `json:"name,omitempty"` // shown on the website rankings
	Ascending bool   `json:"ascending"`      // lower scores rank higher (times)
	Force     bool   `json:"force_update"`   // every record replaces the previous one
	RankLimit uint32 `json:"rank_limit"`
	DataLimit uint32 `json:"data_limit"`
	SizeLimit uint32 `json:"size_limit"`
}

// Title holds the 128-byte communication passphrase (hex) that keys view reply signatures.
type Title struct {
	Name       string           `json:"name,omitempty"` // shown on the website rankings
	Passphrase string           `json:"passphrase"`
	Boards     map[string]Board `json:"boards"`
}

type Service struct {
	Core        Core
	Tickets     *ticket.Signer
	Store       *Store
	ResourceKey []byte // ranking resource key, firmware key at VSH 0x684030
	Titles      map[string]Title
	RecordFQDN  string
	ViewFQDN    string
	Log         *slog.Logger
}

func (s *Service) board(commid string, n int) Board {
	b := s.Titles[commid].Boards[strconv.Itoa(n)]
	if b.RankLimit == 0 {
		b.RankLimit = 100000
	}
	if b.DataLimit == 0 {
		b.DataLimit = 10
	}
	if b.SizeLimit == 0 {
		b.SizeLimit = 1 << 20
	}
	return b
}

func (s *Service) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /np/resource/title/{commid}/ranking/{file}", s.discovery)
	for path, h := range map[string]func(*call){
		"/ranking_update/sec/record_score":         s.recordScore,
		"/ranking_update/sec/record_data":          s.recordData,
		"/ranking_view/func/get_game_data":         s.getGameData,
		"/ranking_view/func/get_player_rank":       s.getPlayerRank,
		"/ranking_view/func/get_ranking":           s.getRanking,
		"/ranking_view/func/get_board_info":        s.getBoardInfo,
		"/ranking_view/func/censor_comment":        func(c *call) { s.reply(c, resultOK, nil, nil) },
		"/ranking_view/func/sanitize_comment":      s.sanitizeComment,
		"/ranking_view/sec/get_friend_player_rank": s.getFriendPlayerRank,
	} {
		mux.HandleFunc("POST "+path, s.handle(h, strings.Contains(path, "/sec/")))
	}
	// ponytail: clans are not modelled; a clean "no such ranking" beats an HTTP error.
	for _, op := range []string{"get_member_game_data", "get_member_ranking_by_id", "get_member_ranking_by_range", "get_clan_ranking_by_id", "get_clan_ranking_by_range"} {
		mux.HandleFunc("POST /ranking_clan_view/func/"+op, s.handle(func(c *call) { s.reply(c, resultNotFound, nil, nil) }, false))
	}
}

// discovery is the signed ranking resource VSH reads before any score call.
func (s *Service) discovery(w http.ResponseWriter, r *http.Request) {
	commid := r.PathValue("commid")
	if !commIDPattern.MatchString(commid) || r.PathValue("file") != commid+"-ranking.xml" {
		http.NotFound(w, r)
		return
	}
	body := resource.Sign([]byte(`<?xml version="1.0" encoding="utf-8"?><ranking status="alive" titleid="`+commid+
		`"><record-fqdn>`+s.RecordFQDN+`</record-fqdn><view-fqdn>`+s.ViewFQDN+`</view-fqdn></ranking>`), s.ResourceKey)
	w.Header().Set("Content-Type", "text/xml; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Write(body)
}

type jidIn struct {
	PC  string `xml:"pc,attr"`
	JID string `xml:",chardata"`
}

type request struct {
	XMLName  xml.Name `xml:"ranking"`
	Ticket   string   `xml:"ticket"`
	TitleID  string   `xml:"titleid"`
	Board    int      `xml:"board"`
	PC       int      `xml:"pc"`
	Score    int64    `xml:"score"`
	Message  string   `xml:"message"`
	Info     string   `xml:"info"`
	TempRank struct {
		Use string `xml:"use,attr"`
	} `xml:"temp-rank"`
	Size    int64   `xml:"size"`
	Start   int     `xml:"start"`
	ListMax int     `xml:"list-max"`
	JIDs    []jidIn `xml:"jid"`
	Option  struct {
		Message string `xml:"message,attr"`
		Info    string `xml:"info,attr"`
		Owner   string `xml:"owner,attr"`
		Offset  int    `xml:"offset,attr"`
		Limit   int    `xml:"limit,attr"`
	} `xml:"option"`
}

type call struct {
	w        http.ResponseWriter
	r        *http.Request
	req      request
	actor    ticket.Identity
	actorJID string
	data     []byte // raw payload of record_data
}

func (s *Service) handle(h func(*call), sec bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c := &call{w: w, r: r}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxXML+4+1<<24))
		if err != nil {
			s.reply(c, resultTooLarge, nil, nil)
			return
		}
		if n, err := strconv.Atoi(r.Header.Get("Sce-Np-Info-Length")); err == nil {
			if n < 0 || n+4 > len(body) || !bytes.Equal(body[n:n+4], []byte("\r\n\r\n")) {
				s.reply(c, resultBadRequest, nil, nil)
				return
			}
			body, c.data = body[:n], body[n+4:]
		}
		if len(body) > maxXML || xml.Unmarshal(body, &c.req) != nil || !commIDPattern.MatchString(c.req.TitleID) {
			s.reply(c, resultBadRequest, nil, nil)
			return
		}
		if sec {
			raw, err := base64.StdEncoding.DecodeString(strings.Join(strings.Fields(c.req.Ticket), ""))
			if err == nil {
				c.actor, err = ticket.VerifyAccount(r.Context(), s.Core, s.Tickets, raw)
			}
			if err != nil {
				s.reply(c, resultBadTicket, nil, nil)
				return
			}
			c.actorJID = s.jid(r.Context(), c.actor.AccountID, c.actor.OnlineID)
		}
		h(c)
	}
}

func (s *Service) jid(ctx context.Context, account, online string) string {
	country := "us"
	if a, err := s.Core.GetAccount(ctx, account); err == nil && len(a.Country) == 2 {
		country = strings.ToLower(a.Country)
	}
	return online + "@" + country + ".np.community.playstation.net"
}

func (s *Service) fail(c *call, err error) {
	s.Log.Error("score store", "err", err, "path", c.r.URL.Path)
	s.reply(c, resultInternal, nil, nil)
}

func (s *Service) recordScore(c *call) {
	info, err := base64.StdEncoding.DecodeString(c.req.Info)
	if err != nil || len(info) > maxInfo || len([]rune(c.req.Message)) > maxComment {
		s.reply(c, resultBadRequest, nil, nil)
		return
	}
	b := s.board(c.req.TitleID, c.req.Board)
	stored, rank, err := s.Store.Put(c.r.Context(), c.req.TitleID, c.req.Board, b.Ascending, b.Force, Record{
		Account: c.actor.AccountID, PC: c.req.PC, Score: c.req.Score, JID: c.actorJID,
		Name: c.actor.OnlineID, Comment: c.req.Message, Info: info,
	})
	if err != nil {
		s.fail(c, err)
		return
	}
	if !stored {
		s.reply(c, resultNotBest, nil, nil)
		return
	}
	var inner any
	if c.req.TempRank.Use == "1" {
		inner = struct {
			Rank int `xml:"temp-rank"`
		}{rank}
	}
	s.reply(c, resultOK, inner, nil)
}

func (s *Service) recordData(c *call) {
	b := s.board(c.req.TitleID, c.req.Board)
	if int64(len(c.data)) != c.req.Size || len(c.data) == 0 {
		s.reply(c, resultBadRequest, nil, nil)
		return
	}
	if len(c.data) > int(b.SizeLimit) {
		s.reply(c, resultTooLarge, nil, nil)
		return
	}
	ok, err := s.Store.SetData(c.r.Context(), c.req.TitleID, c.req.Board, c.actor.AccountID, c.req.PC, c.req.Score, c.data)
	if err != nil {
		s.fail(c, err)
		return
	}
	if !ok {
		s.reply(c, resultBadScore, nil, nil)
		return
	}
	s.reply(c, resultOK, nil, nil)
}

// account resolves a request <jid> to a core account, "" when unknown.
func (s *Service) account(ctx context.Context, jid string) (string, error) {
	online, _, _ := strings.Cut(jid, "@")
	link, err := s.Core.GetLinkByOnlineID(ctx, online)
	if errors.Is(err, coreclient.ErrUnavailable) {
		return "", err
	}
	if err != nil {
		return "", nil
	}
	return link.AccountID, nil
}

// pcID parses the hex pc attribute of a <jid>.
func pcID(s string) int {
	n, _ := strconv.ParseInt(s, 16, 32)
	return int(n)
}

func (s *Service) getGameData(c *call) {
	if len(c.req.JIDs) != 1 {
		s.reply(c, resultBadRequest, nil, nil)
		return
	}
	account, err := s.account(c.r.Context(), c.req.JIDs[0].JID)
	if err != nil {
		s.fail(c, err)
		return
	}
	var data []byte
	if account != "" {
		if data, err = s.Store.Data(c.r.Context(), c.req.TitleID, c.req.Board, account, pcID(c.req.JIDs[0].PC)); err != nil {
			s.fail(c, err)
			return
		}
	}
	if data == nil {
		s.reply(c, resultNoGameData, nil, nil)
		return
	}
	s.reply(c, resultOK, nil, data)
}

func (s *Service) getBoardInfo(c *call) {
	b := s.board(c.req.TitleID, c.req.Board)
	sortOpt, updateOpt := 0, 0
	if b.Ascending {
		sortOpt = 1
	}
	if b.Force {
		updateOpt = 1
	}
	s.reply(c, resultOK, struct {
		RankLimit uint32 `xml:"rank-limit"`
		Update    int    `xml:"update-option"`
		Sort      int    `xml:"sort-option"`
		DataLimit uint32 `xml:"data-limit"`
		SizeLimit uint32 `xml:"size-limit"`
	}{b.RankLimit, updateOpt, sortOpt, b.DataLimit, b.SizeLimit}, nil)
}

func (s *Service) sanitizeComment(c *call) {
	s.reply(c, resultOK, struct {
		Message string `xml:"message"`
	}{c.req.Message}, nil)
}

func (s *Service) getRanking(c *call) {
	b := s.board(c.req.TitleID, c.req.Board)
	n := min(max(c.req.ListMax, 1), maxList)
	recs, err := s.Store.Range(c.r.Context(), c.req.TitleID, c.req.Board, b.Ascending, max(c.req.Start, 1), n)
	if err != nil {
		s.fail(c, err)
		return
	}
	s.list(c, recs, -1, true)
}

func (s *Service) getPlayerRank(c *call) {
	b := s.board(c.req.TitleID, c.req.Board)
	want := map[string]int{} // account -> pc
	for _, j := range c.req.JIDs {
		account, err := s.account(c.r.Context(), j.JID)
		if err != nil {
			s.fail(c, err)
			return
		}
		if account != "" {
			want[account] = pcID(j.PC)
		}
	}
	accounts := make([]string, 0, len(want))
	for a := range want {
		accounts = append(accounts, a)
	}
	recs, err := s.Store.Accounts(c.r.Context(), c.req.TitleID, c.req.Board, b.Ascending, accounts)
	if err != nil {
		s.fail(c, err)
		return
	}
	var out []Record
	for _, r := range recs {
		if want[r.Account] == r.PC {
			out = append(out, r)
		}
	}
	if len(out) == 0 {
		// ponytail: VSH rejects an empty list here; a record matching nobody leaves every
		// requested player "not ranked". Unknown what Sony sent (research notes, unknown 3).
		out = []Record{{JID: "openpak-none@us.np.community.playstation.net", Name: "openpak-none", Updated: time.Now()}}
	}
	s.list(c, out, -1, false)
}

func (s *Service) getFriendPlayerRank(c *call) {
	b := s.board(c.req.TitleID, c.req.Board)
	fs, err := s.Core.Friends(c.r.Context(), c.actor.AccountID)
	if err != nil {
		s.reply(c, resultInternal, nil, nil)
		return
	}
	var accounts []string
	if c.req.Option.Owner == "true" {
		accounts = append(accounts, c.actor.AccountID)
	}
	for _, f := range fs {
		accounts = append(accounts, f.AccountId)
	}
	recs, err := s.Store.Accounts(c.r.Context(), c.req.TitleID, c.req.Board, b.Ascending, accounts)
	if err != nil {
		s.fail(c, err)
		return
	}
	// ponytail: one entry per player (their best pcId); ranks stay global.
	seen := map[string]bool{}
	var best []Record
	for _, r := range recs {
		if !seen[r.Account] {
			seen[r.Account] = true
			best = append(best, r)
		}
	}
	offset := min(max(c.req.Option.Offset, 0), len(best))
	limit := c.req.Option.Limit
	if limit <= 0 || limit > maxList {
		limit = maxList
	}
	s.list(c, best[offset:min(offset+limit, len(best))], len(best), false)
}

type recordXML struct {
	XMLName xml.Name `xml:"record"`
	Serial  int      `xml:"serial,attr"`
	JID     struct {
		PC  string `xml:"pc,attr"`
		JID string `xml:",chardata"`
	} `xml:"jid"`
	Rank    int     `xml:"rank"`
	MaxRank *int    `xml:"max-rank,omitempty"`
	Score   int64   `xml:"score"`
	Name    string  `xml:"name"`
	Data    int     `xml:"data"`
	Date    string  `xml:"date"`
	Message *string `xml:"message,omitempty"`
	Info    *string `xml:"info,omitempty"`
}

// list writes update/total/[hits]/list for the ranking queries.
func (s *Service) list(c *call, recs []Record, hits int, maxRank bool) {
	total, err := s.Store.Total(c.r.Context(), c.req.TitleID, c.req.Board)
	if err != nil {
		s.fail(c, err)
		return
	}
	items := make([]recordXML, len(recs))
	for i, r := range recs {
		x := &items[i]
		x.Serial, x.Rank, x.Score, x.Name, x.Date = r.Serial, r.Rank, r.Score, r.Name, r.Updated.UTC().Format(dateFmt)
		x.JID.PC, x.JID.JID = strconv.FormatInt(int64(r.PC), 16), r.JID
		if r.HasData {
			x.Data = 1
		}
		if maxRank {
			x.MaxRank = &x.Rank
		}
		if c.req.Option.Message == "true" {
			x.Message = &r.Comment
		}
		if c.req.Option.Info == "true" {
			info := base64.StdEncoding.EncodeToString(r.Info)
			x.Info = &info
		}
	}
	var hitsPtr *int
	if hits >= 0 {
		hitsPtr = &hits
	}
	s.reply(c, resultOK, struct {
		Update string `xml:"update"`
		Total  int    `xml:"total"`
		Hits   *int   `xml:"hits,omitempty"`
		List   struct {
			Number  int         `xml:"number,attr"`
			Records []recordXML `xml:"record"`
		} `xml:"list"`
	}{Update: time.Now().UTC().Format(dateFmt), Total: total, Hits: hitsPtr, List: struct {
		Number  int         `xml:"number,attr"`
		Records []recordXML `xml:"record"`
	}{len(items), items}}, nil)
}

// reply writes <ranking result> with inner's fields as children; view replies carry Sce-Np-Signature (HMAC-SHA1 keyed by the
// title passphrase over the XML part), data follows the XML after CRLFCRLF.
func (s *Service) reply(c *call, result string, inner any, data []byte) {
	var b bytes.Buffer
	if inner == nil {
		inner = struct{}{}
	}
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>`)
	xml.NewEncoder(&b).EncodeElement(inner, xml.StartElement{Name: xml.Name{Local: "ranking"},
		Attr: []xml.Attr{{Name: xml.Name{Local: "result"}, Value: result}}})
	h := c.w.Header()
	if key, err := hex.DecodeString(s.Titles[c.req.TitleID].Passphrase); err == nil && len(key) > 0 {
		mac := hmac.New(sha1.New, key)
		mac.Write(b.Bytes())
		h.Set("Sce-Np-Signature", hex.EncodeToString(mac.Sum(nil)))
	}
	if data != nil {
		h.Set("Sce-Np-Info-Length", strconv.Itoa(b.Len()))
		b.WriteString("\r\n\r\n")
		b.Write(data)
	}
	h.Set("Content-Type", "text/xml; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(b.Len()))
	h.Set("Cache-Control", "no-store")
	c.w.Write(b.Bytes())
}
