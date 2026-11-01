package main

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Store keeps one row per (title, board, account, pcId). jid and name are the player's
// identity at the time of the last record.
type Store struct{ DB *pgxpool.Pool }

type Record struct {
	Account string
	PC      int
	Score   int64
	JID     string
	Name    string
	Comment string
	Info    []byte
	HasData bool
	Updated time.Time
	Rank    int // competition rank: ties share it
	Serial  int // unique position
}

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.DB.Exec(ctx, `
	CREATE TABLE IF NOT EXISTS scores (
		commid  text        NOT NULL,
		board   integer     NOT NULL,
		account text        NOT NULL,
		pc      integer     NOT NULL,
		score   bigint      NOT NULL,
		jid     text        NOT NULL,
		name    text        NOT NULL,
		comment text        NOT NULL,
		info    bytea       NOT NULL,
		data    bytea,
		updated timestamptz NOT NULL,
		PRIMARY KEY (commid, board, account, pc));
	CREATE INDEX IF NOT EXISTS scores_rank ON scores (commid, board, score)`)
	return err
}

func dir(asc bool) string {
	if asc {
		return "ASC"
	}
	return "DESC"
}

// ranked is the board with ranks; earlier records win ties.
func ranked(asc bool) string {
	d := dir(asc)
	return `WITH r AS (SELECT account, pc, score, jid, name, comment, info, data IS NOT NULL, updated,
		rank() OVER (ORDER BY score ` + d + `), row_number() OVER (ORDER BY score ` + d + `, updated, account, pc)
		FROM scores WHERE commid = $1 AND board = $2) SELECT * FROM r `
}

func scanRecords(rows pgx.Rows) ([]Record, error) {
	var out []Record
	for rows.Next() {
		var r Record
		if err := rows.Scan(&r.Account, &r.PC, &r.Score, &r.JID, &r.Name, &r.Comment, &r.Info, &r.HasData, &r.Updated, &r.Rank, &r.Serial); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) Total(ctx context.Context, commid string, board int) (int, error) {
	var n int
	err := s.DB.QueryRow(ctx, `SELECT count(*) FROM scores WHERE commid = $1 AND board = $2`, commid, board).Scan(&n)
	return n, err
}

// Range returns up to n records starting at 1-based position start.
func (s *Store) Range(ctx context.Context, commid string, board int, asc bool, start, n int) ([]Record, error) {
	rows, err := s.DB.Query(ctx, ranked(asc)+`WHERE row_number >= $3 ORDER BY row_number LIMIT $4`, commid, board, start, n)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

// Accounts returns every record (all pcIds) of the given accounts, best first.
func (s *Store) Accounts(ctx context.Context, commid string, board int, asc bool, accounts []string) ([]Record, error) {
	rows, err := s.DB.Query(ctx, ranked(asc)+`WHERE account = ANY($3) ORDER BY row_number`, commid, board, accounts)
	if err != nil {
		return nil, err
	}
	return scanRecords(rows)
}

// Put stores rec unless force is off and the existing score is at least as good. It
// returns false when the record was kept, plus the record's rank afterwards.
func (s *Store) Put(ctx context.Context, commid string, board int, asc, force bool, rec Record) (bool, int, error) {
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return false, 0, err
	}
	defer tx.Rollback(ctx)
	var cur int64
	err = tx.QueryRow(ctx, `SELECT score FROM scores WHERE commid = $1 AND board = $2 AND account = $3 AND pc = $4 FOR UPDATE`,
		commid, board, rec.Account, rec.PC).Scan(&cur)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, 0, err
	}
	if err == nil && !force && (asc && rec.Score >= cur || !asc && rec.Score <= cur) {
		return false, 0, nil
	}
	// A new score drops game data uploaded for the old one.
	if _, err = tx.Exec(ctx, `INSERT INTO scores VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NULL, now())
		ON CONFLICT (commid, board, account, pc) DO UPDATE SET score = $5, jid = $6, name = $7, comment = $8, info = $9, data = NULL, updated = now()`,
		commid, board, rec.Account, rec.PC, rec.Score, rec.JID, rec.Name, rec.Comment, rec.Info); err != nil {
		return false, 0, err
	}
	if err = tx.Commit(ctx); err != nil {
		return false, 0, err
	}
	better := ">"
	if asc {
		better = "<"
	}
	var rank int
	err = s.DB.QueryRow(ctx, `SELECT count(*) + 1 FROM scores WHERE commid = $1 AND board = $2 AND score `+better+` $3`,
		commid, board, rec.Score).Scan(&rank)
	return true, rank, err
}

// SetData attaches game data to the record holding exactly score; false when there is none.
func (s *Store) SetData(ctx context.Context, commid string, board int, account string, pc int, score int64, data []byte) (bool, error) {
	tag, err := s.DB.Exec(ctx, `UPDATE scores SET data = $6 WHERE commid = $1 AND board = $2 AND account = $3 AND pc = $4 AND score = $5`,
		commid, board, account, pc, score, data)
	return tag.RowsAffected() == 1, err
}

// Data returns the game data of one record, nil when absent.
func (s *Store) Data(ctx context.Context, commid string, board int, account string, pc int) ([]byte, error) {
	var data []byte
	err := s.DB.QueryRow(ctx, `SELECT data FROM scores WHERE commid = $1 AND board = $2 AND account = $3 AND pc = $4`,
		commid, board, account, pc).Scan(&data)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return data, err
}
