package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"openpak/np-score/internal/rankingsapi"
)

// The rankings contract over the scores table. Board ids are "<communication id>.<board>"
// (for example "NPWR01234_00.2"). Rows carry account_id, the OpenPak account id the table keys
// players by (a player with several pcIds appears once per pcId). Ranks are the game's own
// competition ranks (ties share one); paging follows the same order, earlier records first
// among equal scores. Whether a board is a time or a point score is not stored, so every board
// is kind "score", ordered by the title's configured ascending flag.

// scoreStore is what the source reads; Store is the Postgres implementation.
type scoreStore interface {
	Boards(ctx context.Context) ([]BoardRef, error)
	Total(ctx context.Context, commid string, board int) (int, error)
	Range(ctx context.Context, commid string, board int, asc bool, start, n int) ([]Record, error)
	Accounts(ctx context.Context, commid string, board int, asc bool, accounts []string) ([]Record, error)
}

type rankingSource struct {
	store  scoreStore
	titles map[string]Title
}

func (r rankingSource) cfg(commid string, board int) Board {
	return r.titles[commid].Boards[strconv.Itoa(board)]
}

func (r rankingSource) Boards(ctx context.Context) ([]rankingsapi.Board, error) {
	refs, err := r.store.Boards(ctx)
	if err != nil {
		return nil, err
	}
	boards := make([]rankingsapi.Board, 0, len(refs))
	for _, ref := range refs {
		b := r.cfg(ref.CommID, ref.Board)
		name := b.Name
		if name == "" {
			name = rankingsapi.NeutralName(uint64(ref.Board))
		}
		title := r.titles[ref.CommID].Name
		if title == "" {
			title = ref.CommID
		}
		order := rankingsapi.OrderDesc
		if b.Ascending {
			order = rankingsapi.OrderAsc
		}
		boards = append(boards, rankingsapi.Board{
			ID: fmt.Sprintf("%s.%d", ref.CommID, ref.Board), Name: title + ": " + name,
			Kind: rankingsapi.KindScore, Order: order,
		})
	}
	return boards, nil
}

func toRows(recs []Record) []rankingsapi.Row {
	rows := make([]rankingsapi.Row, 0, len(recs))
	for _, rec := range recs {
		rows = append(rows, rankingsapi.Row{
			Rank: rec.Rank, AccountID: rec.Account, Score: rec.Score,
			Display: rankingsapi.Number(rec.Score), UpdatedAt: rec.Updated.UTC(),
		})
	}
	return rows
}

func (r rankingSource) Rows(ctx context.Context, id string, q rankingsapi.Query) (rankingsapi.Page, error) {
	commid, n, found := strings.Cut(id, ".")
	board, err := strconv.Atoi(n)
	if !found || err != nil || !commIDPattern.MatchString(commid) || board < 0 {
		return rankingsapi.Page{}, rankingsapi.ErrNoBoard
	}
	asc := r.cfg(commid, board).Ascending
	total, err := r.store.Total(ctx, commid, board)
	if err != nil {
		return rankingsapi.Page{}, err
	}
	if total == 0 {
		if _, ok := r.titles[commid].Boards[n]; !ok {
			return rankingsapi.Page{}, rankingsapi.ErrNoBoard
		}
		return rankingsapi.Page{}, nil
	}
	recs, err := r.store.Range(ctx, commid, board, asc, q.Offset+1, q.Limit)
	if err != nil {
		return rankingsapi.Page{}, err
	}
	p := rankingsapi.Page{Total: total, Rows: toRows(recs)}
	if q.Around != "" {
		mine, err := r.store.Accounts(ctx, commid, board, asc, []string{q.Around})
		if err != nil {
			return rankingsapi.Page{}, err
		}
		if len(mine) > 0 {
			near, err := r.store.Range(ctx, commid, board, asc, max(mine[0].Serial-rankingsapi.AroundSpan, 1), 2*rankingsapi.AroundSpan+1)
			if err != nil {
				return rankingsapi.Page{}, err
			}
			p.Around = toRows(near)
		}
	}
	return p, nil
}
