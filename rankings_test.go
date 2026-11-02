package main

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"openpak/np-score/internal/rankingsapi"
)

type fakeScores struct{ recs []Record }

func (f fakeScores) Boards(context.Context) ([]BoardRef, error) {
	return []BoardRef{{"NPWR01234_00", 1}, {"NPWR01234_00", 2}}, nil
}
func (f fakeScores) Total(_ context.Context, c string, b int) (int, error) {
	if b != 2 {
		return 0, nil
	}
	return len(f.recs), nil
}
func (f fakeScores) Range(_ context.Context, _ string, _ int, _ bool, start, n int) ([]Record, error) {
	lo := min(start-1, len(f.recs))
	return f.recs[lo:min(lo+n, len(f.recs))], nil
}
func (f fakeScores) Accounts(_ context.Context, _ string, _ int, _ bool, a []string) ([]Record, error) {
	for _, r := range f.recs {
		if r.Account == a[0] {
			return []Record{r}, nil
		}
	}
	return nil, nil
}

func testSource() rankingSource {
	var recs []Record
	for i := 1; i <= 20; i++ {
		rank := i
		if i == 4 {
			rank = 3 // tied with position 3
		}
		recs = append(recs, Record{Account: "acc" + string(rune('a'+i-1)), Score: int64(1000 * i), Rank: rank, Serial: i, Updated: time.Unix(1700000000, 0)})
	}
	return rankingSource{store: fakeScores{recs}, titles: map[string]Title{
		"NPWR01234_00": {Name: "Test Racer", Boards: map[string]Board{"2": {Ascending: true, Name: "Time Trial"}}},
	}}
}

func TestRankingBoards(t *testing.T) {
	b, err := testSource().Boards(context.Background())
	if err != nil || len(b) != 2 {
		t.Fatalf("%v %+v", err, b)
	}
	if b[0].ID != "NPWR01234_00.1" || b[0].Name != "Test Racer: Board 1" || b[0].Order != "desc" ||
		b[1].Name != "Test Racer: Time Trial" || b[1].Order != "asc" || b[1].Kind != "score" {
		t.Fatalf("%+v", b)
	}
}

func TestRankingRows(t *testing.T) {
	h := rankingsapi.Handler("np", testSource())
	get := func(path string) (int, rankingsapi.Page) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		var p rankingsapi.Page
		json.Unmarshal(rec.Body.Bytes(), &p)
		return rec.Code, p
	}
	code, p := get("/internal/rankings/boards/NPWR01234_00.2?limit=3&offset=2&around=accj")
	if code != 200 || p.Total != 20 || len(p.Rows) != 3 || p.Rows[0].AccountID != "accc" || p.Rows[1].Rank != 3 ||
		p.Rows[0].Display != "3,000" || p.Rows[0].PID != "" {
		t.Fatalf("%d %+v", code, p)
	}
	if len(p.Around) != 11 || p.Around[0].AccountID != "accE" && p.Around[0].AccountID != "acce" || p.Around[5].AccountID != "accj" {
		t.Fatalf("around %+v", p.Around)
	}
	if code, _ = get("/internal/rankings/boards/NPWR01234_00.1"); code != 404 {
		t.Fatalf("empty unconfigured board: %d", code)
	}
	for _, id := range []string{"x", "NPWR01234_00", "bad.2", "NPWR01234_00.-1"} {
		if code, _ = get("/internal/rankings/boards/" + id); code != 404 {
			t.Fatalf("%s: %d", id, code)
		}
	}
}
