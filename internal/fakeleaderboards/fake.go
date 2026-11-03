// Package fakeleaderboards is an in-process openpak.leaderboards.v1.Leaderboards gRPC server for
// tests: entries live in memory, the best/force rule, rank (ties share it) and position follow the
// real service, friends are declared, and the x-internal-key check is mirrored.
package fakeleaderboards

import (
	"context"
	"net"
	"sort"
	"sync"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"

	pb "openpak/np-score/internal/leaderboardspb"
)

type key struct{ title, board string }

type Fake struct {
	pb.UnimplementedLeaderboardsServer
	Key string
	// Down makes every call fail Unavailable.
	Down    bool
	mu      sync.Mutex
	boards  map[key]*pb.Policy
	entries map[key][]*pb.Entry
	friends map[string][]string
	Synced  int // UpsertBoard calls
	tick    int64
}

func New(apiKey string) *Fake {
	return &Fake{Key: apiKey, boards: map[key]*pb.Policy{}, entries: map[key][]*pb.Entry{}, friends: map[string][]string{}}
}

func (f *Fake) AddFriend(a, b string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.friends[a] = append(f.friends[a], b)
	f.friends[b] = append(f.friends[b], a)
}

// Policy returns the stored policy of a board (nil when none).
func (f *Fake) Policy(title, board string) *pb.Policy {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.boards[key{title, board}]
}

// Serve starts the fake and returns dial options for a client.
func (f *Fake) Serve(t testing.TB) []grpc.DialOption {
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	pb.RegisterLeaderboardsServer(gs, f)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	return []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() })}
}

func (f *Fake) check(ctx context.Context) error {
	md, _ := metadata.FromIncomingContext(ctx)
	if v := md.Get("x-internal-key"); len(v) == 0 || v[0] != f.Key {
		return status.Error(codes.Unauthenticated, "missing internal credentials")
	}
	if f.Down {
		return status.Error(codes.Unavailable, "down")
	}
	return nil
}

func k(r *pb.BoardRef) key { return key{r.GetTitle(), r.GetBoard()} }

func (f *Fake) asc(kk key) bool { p := f.boards[kk]; return p != nil && p.Order == "asc" }

// ranked returns the board's entries in position order with rank and position filled in.
func (f *Fake) ranked(kk key) []*pb.Entry {
	es := append([]*pb.Entry(nil), f.entries[kk]...)
	asc := f.asc(kk)
	sort.SliceStable(es, func(i, j int) bool {
		if es[i].Score != es[j].Score {
			return (es[i].Score < es[j].Score) == asc
		}
		if es[i].UpdatedUnixMs != es[j].UpdatedUnixMs {
			return es[i].UpdatedUnixMs < es[j].UpdatedUnixMs
		}
		if es[i].Account != es[j].Account {
			return es[i].Account < es[j].Account
		}
		return es[i].Sub < es[j].Sub
	})
	out := make([]*pb.Entry, len(es))
	for i, e := range es {
		c := proto.Clone(e).(*pb.Entry)
		c.Position, c.Rank = int64(i+1), int64(i+1)
		if i > 0 && out[i-1].Score == c.Score {
			c.Rank = out[i-1].Rank
		}
		out[i] = c
	}
	return out
}

func (f *Fake) find(kk key, account string, sub int32) *pb.Entry {
	for _, e := range f.entries[kk] {
		if e.Account == account && e.Sub == sub {
			return e
		}
	}
	return nil
}

func (f *Fake) Submit(ctx context.Context, req *pb.SubmitRequest) (*pb.SubmitResponse, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	kk := k(req.Ref)
	if f.boards[kk] == nil {
		f.boards[kk] = req.CreatePolicy
		if f.boards[kk] == nil {
			f.boards[kk] = &pb.Policy{}
		}
	}
	p := f.boards[kk]
	cur := f.find(kk, req.Account, req.Sub)
	changed := cur == nil
	if cur != nil {
		better := req.Score > cur.Score
		if p.Order == "asc" {
			better = req.Score < cur.Score
		}
		changed = better || p.Aggregation == "latest" || p.Aggregation == "force"
	}
	f.tick++
	if changed {
		e := &pb.Entry{Account: req.Account, Sub: req.Sub, Score: req.Score, Comment: req.Comment, Info: req.Info,
			DisplayName: req.DisplayName, Network: req.Ref.Network, UpdatedUnixMs: f.tick}
		if cur != nil {
			cur.Score, cur.Comment, cur.Info, cur.DisplayName, cur.Network, cur.UpdatedUnixMs, cur.HasData = e.Score, e.Comment, e.Info, e.DisplayName, e.Network, e.UpdatedUnixMs, false
		} else {
			f.entries[kk] = append(f.entries[kk], e)
		}
	}
	var entry *pb.Entry
	for _, e := range f.ranked(kk) {
		if e.Account == req.Account && e.Sub == req.Sub {
			entry = e
		}
	}
	return &pb.SubmitResponse{Changed: changed, Entry: entry, Total: int64(len(f.entries[kk]))}, nil
}

func (f *Fake) Range(ctx context.Context, req *pb.RangeRequest) (*pb.RangeResponse, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.ranked(k(req.Ref))
	lo := min(max(int(req.Start)-1, 0), len(all))
	return &pb.RangeResponse{Entries: all[lo:min(lo+int(req.Limit), len(all))], Total: int64(len(all))}, nil
}

func (f *Fake) ByAccounts(ctx context.Context, req *pb.ByAccountsRequest) (*pb.RangeResponse, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	all := f.ranked(k(req.Ref))
	want := map[string]bool{}
	for _, a := range req.Accounts {
		want[a] = true
	}
	out := &pb.RangeResponse{Total: int64(len(all))}
	for _, e := range all {
		if want[e.Account] {
			out.Entries = append(out.Entries, e)
		}
	}
	return out, nil
}

func (f *Fake) FriendRanks(ctx context.Context, req *pb.FriendRanksRequest) (*pb.RangeResponse, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	want := map[string]bool{}
	for _, a := range f.friends[req.Account] {
		want[a] = true
	}
	if req.IncludeSelf {
		want[req.Account] = true
	}
	all := f.ranked(k(req.Ref))
	seen := map[string]bool{}
	var best []*pb.Entry
	for _, e := range all {
		if want[e.Account] && !seen[e.Account] {
			seen[e.Account] = true
			best = append(best, e)
		}
	}
	lo := min(int(req.Offset), len(best))
	return &pb.RangeResponse{Entries: best[lo:min(lo+int(req.Limit), len(best))], Total: int64(len(all)), Hits: int64(len(best))}, nil
}

func (f *Fake) Total(ctx context.Context, req *pb.TotalRequest) (*pb.TotalResponse, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return &pb.TotalResponse{Total: int64(len(f.entries[k(req.Ref)]))}, nil
}

type data struct{ b []byte }

var dataMu sync.Mutex
var datas = map[*pb.Entry]*data{}

func (f *Fake) SetData(ctx context.Context, req *pb.SetDataRequest) (*pb.SetDataResponse, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.find(k(req.Ref), req.Account, req.Sub)
	if e == nil || e.Score != req.ExpectScore {
		return &pb.SetDataResponse{}, nil
	}
	dataMu.Lock()
	datas[e] = &data{req.Data}
	dataMu.Unlock()
	e.HasData = true
	return &pb.SetDataResponse{Stored: true}, nil
}

func (f *Fake) GetData(ctx context.Context, req *pb.GetDataRequest) (*pb.GetDataResponse, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	e := f.find(k(req.Ref), req.Account, req.Sub)
	if e == nil || !e.HasData {
		return &pb.GetDataResponse{}, nil
	}
	dataMu.Lock()
	defer dataMu.Unlock()
	return &pb.GetDataResponse{Found: true, Data: datas[e].b}, nil
}

func (f *Fake) UpsertBoard(ctx context.Context, req *pb.UpsertBoardRequest) (*pb.BoardInfoResponse, error) {
	if err := f.check(ctx); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boards[k(req.Ref)] = req.Policy
	f.Synced++
	return &pb.BoardInfoResponse{}, nil
}
