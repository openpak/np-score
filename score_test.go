package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"openpak/np-common/coreclient"
	"openpak/np-common/fakecore"
	"openpak/np-common/identity"
	"openpak/np-common/resource"
	"openpak/np-common/ticket"
	"openpak/np-score/internal/fakeleaderboards"
)

const commid = "NPWR01234_00"

func TestScore(t *testing.T) {
	ctx := context.Background()

	const coreKey = "test-core-key-at-least-thirty-two-bytes"
	const alice, bob, carol = "0b7c3f4e-1d2a-4c5b-9e8f-112233445566", "1b7c3f4e-1d2a-4c5b-9e8f-112233445566", "2b7c3f4e-1d2a-4c5b-9e8f-112233445566"
	fake := fakecore.New(coreKey)
	fake.AddAccount(fakecore.Account{ID: alice, DisplayName: "Alice", Country: "PT"})
	fake.AddAccount(fakecore.Account{ID: bob, DisplayName: "Bobby", Country: "US"})
	fake.AddAccount(fakecore.Account{ID: carol, DisplayName: "Carol", Country: "US"})
	fake.AddFriend(alice, bob)
	lbFake := fakeleaderboards.New("lb-key")
	lbFake.AddFriend(alice, bob)
	lb, lbConn, err := DialLeaderboards("passthrough:///lb", "lb-key", lbFake.Serve(t)...)
	if err != nil {
		t.Fatal(err)
	}
	defer lbConn.Close()
	core, err := coreclient.Dial("passthrough:///core", coreKey, fake.Serve(t)...)
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := ticket.New(key, nil)
	links, tickets := map[string]*coreclient.Link{}, map[string]string{}
	for _, id := range []string{alice, bob, carol} {
		acct, err := core.GetAccount(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if links[id], err = identity.EnsureNPLink(ctx, core, acct); err != nil {
			t.Fatal(err)
		}
		raw, err := signer.Issue(ticket.Identity{AccountID: id, LinkID: links[id].LinkID, OnlineID: links[id].OnlineID, Country: "us", Service: "IV0001-NPXS01001_00"}, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		tickets[id] = base64.StdEncoding.EncodeToString(raw)
	}
	pass := bytes.Repeat([]byte{0x5a}, 128)
	resourceKey := bytes.Repeat([]byte{7}, 20)
	mux := http.NewServeMux()
	svc := &Service{Core: core, Tickets: signer, LB: lb, ResourceKey: resourceKey, RecordFQDN: "rec.example", ViewFQDN: "view.example", Log: slog.Default(),
		Titles: map[string]Title{commid: {Passphrase: hex.EncodeToString(pass), Boards: map[string]Board{"2": {Ascending: true}, "5": {Force: true}}}}}
	svc.Register(mux)
	// Configured boards are declared to the leaderboards service at start.
	if err := svc.SyncBoards(ctx); err != nil {
		t.Fatal(err)
	}
	if p := lbFake.Policy(commid, "2"); lbFake.Synced != 2 || p == nil || p.Order != "asc" || p.Aggregation != "best" {
		t.Fatalf("board sync: %d %v", lbFake.Synced, p)
	}
	if p := lbFake.Policy(commid, "5"); p == nil || p.Aggregation != "latest" {
		t.Fatalf("force board: %v", p)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()

	jid := func(id string) string { return links[id].OnlineID + "@us.np.community.playstation.net" }
	post := func(path, inner string, data []byte) (string, http.Header) {
		t.Helper()
		body := []byte(`<?xml version="1.0" encoding="utf-8"?><ranking platform="ps3" sv="4.93">` + inner + `</ranking>`)
		req, _ := http.NewRequest("POST", srv.URL+path, nil)
		if data != nil {
			req.Header.Set("Sce-Np-Info-Length", strconv.Itoa(len(body)))
			body = append(append(body, "\r\n\r\n"...), data...)
		}
		req.Body, req.ContentLength = io.NopCloser(bytes.NewReader(body)), int64(len(body))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 || resp.Header.Get("Content-Length") == "" {
			t.Fatalf("%s: HTTP %d without Content-Length", path, resp.StatusCode)
		}
		xmlPart := b
		if n, err := strconv.Atoi(resp.Header.Get("Sce-Np-Info-Length")); err == nil {
			xmlPart = b[:n]
		}
		mac := hmac.New(sha1.New, pass)
		mac.Write(xmlPart)
		if resp.Header.Get("Sce-Np-Signature") != hex.EncodeToString(mac.Sum(nil)) {
			t.Fatalf("%s: bad signature", path)
		}
		return string(b), resp.Header
	}
	expect := func(got, want string) {
		t.Helper()
		if !strings.Contains(got, want) {
			t.Fatalf("want %q in\n%s", want, got)
		}
	}
	record := func(id string, board int, score int64) string {
		r, _ := post("/ranking_update/sec/record_score", `<ticket>`+tickets[id]+`</ticket><ticketjid>`+jid(id)+`</ticketjid><psid>00</psid><titleid version="0">`+commid+
			`</titleid><board>`+strconv.Itoa(board)+`</board><pc>0</pc><score>`+strconv.FormatInt(score, 10)+`</score><name>x</name><message>hi `+id[:1]+`</message><info>AAE=</info><temp-rank use="1"/><anticheat>AA==</anticheat>`, nil)
		return r
	}
	head := `<titleid>` + commid + `</titleid><board>1</board>`

	// Discovery is signed with the ranking resource key and names both hosts.
	resp, _ := http.Get(srv.URL + "/np/resource/title/" + commid + "/ranking/" + commid + "-ranking.xml")
	b, _ := io.ReadAll(resp.Body)
	xmlPart := b[bytes.IndexByte(b, '\n')+1:]
	if !bytes.Equal(b, resource.Sign(xmlPart, resourceKey)) || !bytes.Contains(xmlPart, []byte(`<record-fqdn>rec.example</record-fqdn><view-fqdn>view.example</view-fqdn>`)) {
		t.Fatalf("discovery: %s", b)
	}

	// Board 1 ranks high scores first and keeps only a player's best.
	expect(record(alice, 1, 100), `<temp-rank>1</temp-rank>`)
	expect(record(bob, 1, 300), `<temp-rank>1</temp-rank>`)
	expect(record(carol, 1, 200), `<temp-rank>2</temp-rank>`)
	expect(record(alice, 1, 50), `result="15"`)
	// Board 2 is ascending (times).
	record(alice, 2, 100)
	expect(record(bob, 2, 50), `<temp-rank>1</temp-rank>`)

	r, _ := post("/ranking_view/func/get_ranking", head+`<start>2</start><list-max>5</list-max><option message="true" info="false"/>`, nil)
	expect(r, `<total>3</total><list number="2"><record serial="2"><jid pc="0">`+jid(carol)+`</jid><rank>2</rank><max-rank>2</max-rank><score>200</score>`)
	expect(r, `<message>hi 2</message>`)
	if strings.Contains(r, "<info>") {
		t.Fatal("info not requested", r)
	}

	r, _ = post("/ranking_view/func/get_player_rank", head+`<jid pc="0">`+jid(alice)+`</jid><jid pc="0">nobody@us.np.community.playstation.net</jid><option message="false" info="true"/>`, nil)
	expect(r, `<rank>3</rank><score>100</score>`)
	expect(r, `<info>AAE=</info>`)
	r, _ = post("/ranking_view/func/get_player_rank", `<titleid>`+commid+`</titleid><board>9</board><jid pc="0">`+jid(alice)+`</jid><option message="false" info="false"/>`, nil)
	expect(r, `<list number="1"><record serial="0"><jid pc="0">openpak-none@`)

	r, _ = post("/ranking_view/sec/get_friend_player_rank", `<ticket>`+tickets[alice]+`</ticket><ticketjid>`+jid(alice)+`</ticketjid>`+head+`<option message="false" info="false" owner="true" offset="0" limit="20" pc="0"/>`, nil)
	expect(r, `<hits>2</hits><list number="2"><record serial="1"><jid pc="0">`+jid(bob))
	if strings.Contains(r, jid(carol)) {
		t.Fatal("non-friend listed", r)
	}

	// Game data: only for the score the player holds; XML, CRLFCRLF, raw bytes back.
	payload := []byte("\x00ghost\r\n\r\ndata")
	r, _ = post("/ranking_update/sec/record_data", `<ticket>`+tickets[bob]+`</ticket><ticketjid>`+jid(bob)+`</ticketjid>`+head+`<pc>0</pc><score>299</score><size>`+strconv.Itoa(len(payload))+`</size>`, payload)
	expect(r, `result="23"`)
	r, _ = post("/ranking_update/sec/record_data", `<ticket>`+tickets[bob]+`</ticket><ticketjid>`+jid(bob)+`</ticketjid>`+head+`<pc>0</pc><score>300</score><size>`+strconv.Itoa(len(payload))+`</size>`, payload)
	expect(r, `result="00"`)
	r, h := post("/ranking_view/func/get_game_data", head+`<jid pc="0">`+jid(bob)+`</jid>`, nil)
	n, _ := strconv.Atoi(h.Get("Sce-Np-Info-Length"))
	if n == 0 || r[n:n+4] != "\r\n\r\n" || r[n+4:] != string(payload) || !strings.Contains(r[:n], `result="00"`) {
		t.Fatalf("get_game_data framing: %q", r)
	}
	r, _ = post("/ranking_view/func/get_game_data", head+`<jid pc="0">`+jid(alice)+`</jid>`, nil)
	expect(r, `result="18"`)
	r, _ = post("/ranking_view/func/get_ranking", head+`<start>1</start><list-max>1</list-max><option message="false" info="false"/>`, nil)
	expect(r, `<data>1</data>`)

	r, _ = post("/ranking_view/func/get_board_info", `<titleid>`+commid+`</titleid><board>2</board>`, nil)
	expect(r, `<rank-limit>100000</rank-limit><update-option>0</update-option><sort-option>1</sort-option><data-limit>10</data-limit><size-limit>1048576</size-limit>`)
	r, _ = post("/ranking_view/func/sanitize_comment", `<titleid>`+commid+`</titleid><message>gg</message>`, nil)
	expect(r, `<message>gg</message>`)

	r, _ = post("/ranking_update/sec/record_score", `<ticket>AAAA</ticket>`+head+`<pc>0</pc><score>1</score>`, nil)
	expect(r, `result="02"`)

	// A force board replaces the score even when it is worse.
	record(alice, 5, 100)
	expect(record(alice, 5, 40), `<temp-rank>1</temp-rank>`)
	r, _ = post("/ranking_view/func/get_ranking", `<titleid>`+commid+`</titleid><board>5</board><start>1</start><list-max>5</list-max><option message="false" info="false"/>`, nil)
	expect(r, `<score>40</score>`)

	// The record keeps the jid it was written under, and a downed leaderboards service is an
	// internal error, never a hang or a wrong answer.
	lbFake.Down = true
	r, _ = post("/ranking_view/func/get_ranking", head+`<start>1</start><list-max>5</list-max><option message="false" info="false"/>`, nil)
	expect(r, `result="07"`)
}
