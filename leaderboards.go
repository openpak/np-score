package main

import (
	"context"
	"strconv"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	pb "openpak/np-score/internal/leaderboardspb"
)

// network is the account-core link namespace the leaderboards service files PS3 entries under.
const network = "np"

// DialLeaderboards connects to the neutral leaderboards service (gRPC) and presents the internal
// key on every call. extra lets tests add a dialer.
func DialLeaderboards(addr, key string, extra ...grpc.DialOption) (pb.LeaderboardsClient, *grpc.ClientConn, error) {
	opts := append([]grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, inv grpc.UnaryInvoker, o ...grpc.CallOption) error {
			return inv(metadata.AppendToOutgoingContext(ctx, "x-internal-key", key), method, req, reply, cc, o...)
		}),
	}, extra...)
	conn, err := grpc.NewClient(addr, opts...)
	if err != nil {
		return nil, nil, err
	}
	return pb.NewLeaderboardsClient(conn), conn, nil
}

func ref(commid string, board int) *pb.BoardRef {
	return &pb.BoardRef{Network: network, Title: commid, Board: strconv.Itoa(board)}
}

// policy is a board's settings in the core's terms. The core is the owner of the rules; np-score
// only declares what the title's developer set on Sony's side.
func (b Board) policy() *pb.Policy {
	p := &pb.Policy{Name: b.Name, Order: "desc", Aggregation: "best", RankLimit: b.RankLimit, DataLimit: b.DataLimit, SizeLimit: b.SizeLimit}
	if b.Ascending {
		p.Order = "asc"
	}
	if b.Force {
		p.Aggregation = "latest"
	}
	if b.CrossNetwork != nil {
		p.CrossNetwork = pb.Tristate_TRISTATE_TRUE
		if !*b.CrossNetwork {
			p.CrossNetwork = pb.Tristate_TRISTATE_FALSE
		}
	}
	return p
}

// SyncBoards declares every configured board to the leaderboards service. It returns the first
// error; Submit also carries the policy, so a board is right even if this never succeeds.
func (s *Service) SyncBoards(ctx context.Context) error {
	for commid, t := range s.Titles {
		for n := range t.Boards {
			board, err := strconv.Atoi(n)
			if err != nil {
				continue
			}
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			_, err = s.LB.UpsertBoard(c, &pb.UpsertBoardRequest{Ref: ref(commid, board), Policy: s.board(commid, board).policy(), TitleName: t.Name})
			cancel()
			if err != nil {
				return err
			}
		}
	}
	return nil
}

// onlineID is the part of a stored jid before the "@".
func onlineID(jid string) string {
	n, _, _ := strings.Cut(jid, "@")
	return n
}
