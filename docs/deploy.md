# Deploy

## Run

```sh
cp example.env .env   # fill in required values
set -a; . ./.env; set +a
GOWORK=off go run -mod=vendor .
```

Tests use an in-process fake leaderboards server: `GOWORK=off go test -mod=vendor ./...`. Needs `NP_LEADERBOARDS_ADDR` and `NP_LEADERBOARDS_KEY` (the leaderboards service, gRPC 20211). Quadlet: `deploy/openpak-np-score.container`; image `ghcr.io/openpak/np-score`.
