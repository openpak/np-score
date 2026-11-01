// Command np-score serves PS3 Score/Ranking (/ranking_update/*, /ranking_view/*) and its signed
// ranking discovery resource.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"openpak/np-common/coreclient"
	"openpak/np-common/nphttp"
	"openpak/np-common/ticket"
	"os"
	"os/signal"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		slog.Error("np-score stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	dbURL := os.Getenv("NP_SCORE_DATABASE_URL")
	if dbURL == "" {
		return fmt.Errorf("NP_SCORE_DATABASE_URL must be set")
	}
	if len(os.Getenv("NP_CORE_KEY")) < 32 {
		return fmt.Errorf("NP_CORE_KEY must be at least 32 bytes")
	}
	resourceKey, err := os.ReadFile(os.Getenv("NP_RANKING_RESOURCE_KEY"))
	if err != nil || len(resourceKey) != 20 {
		return fmt.Errorf("NP_RANKING_RESOURCE_KEY must name a 20-byte key file")
	}
	titles := map[string]Title{}
	if path := os.Getenv("NP_SCORE_TITLES"); path != "" {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &titles); err != nil {
			return fmt.Errorf("NP_SCORE_TITLES: %w", err)
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	store := &Store{DB: pool}
	if err := store.Migrate(ctx); err != nil {
		return err
	}
	core, err := coreclient.Dial(nphttp.Env("NP_CORE_ADDR", "localhost:7000"), os.Getenv("NP_CORE_KEY"))
	if err != nil {
		return err
	}
	defer core.Close()
	signer, err := ticket.Load(os.Getenv("NP_TICKET_KEY"))
	if err != nil {
		return err
	}
	svc := &Service{Core: core, Tickets: signer, Store: store, ResourceKey: resourceKey, Titles: titles,
		RecordFQDN: nphttp.Env("NP_RANKING_RECORD_FQDN", "ranking-record.np.community.playstation.net"),
		ViewFQDN:   nphttp.Env("NP_RANKING_VIEW_FQDN", "ranking-view.np.community.playstation.net"), Log: log}
	log.Info("score titles loaded", "count", len(titles))
	mux := http.NewServeMux()
	svc.Register(mux)
	nphttp.Health(mux)
	front, err := nphttp.Front()
	if err != nil {
		return err
	}
	return nphttp.Run(ctx, mux, nphttp.Env("NP_HTTP_ADDR", ":8533"), os.Getenv("NP_TLS_ADDR"), front)
}
