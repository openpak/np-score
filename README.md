# PS3 Score Server

OpenPak's PS3 Score (leaderboard) server.

| | |
| --- | --- |
| Network | PlayStation 3 |
| Role | Native PS3 `/ranking_update/*` and `/ranking_view/*` (score boards, comments, game info, game data) and the signed ranking discovery resource |
| Depends on | [`account`](https://github.com/openpak/account) (account core), [`np-common`](https://github.com/openpak/np-common) (vendored), PostgreSQL |
| Used by | [`np-account`](https://github.com/openpak/np-account) |
| Image | `ghcr.io/openpak/np-score` |
| Licence | AGPL-3.0-only |

## What it serves

| Endpoint | What |
| --- | --- |
| `GET /np/resource/title/{commid}/ranking/{commid}-ranking.xml` | Signed discovery resource naming the record and view hosts |
| `POST /ranking_update/sec/record_score`, `record_data` | Record a score (best only, unless the board forces updates) and its game data |
| `POST /ranking_view/func/*` | Range ranking, ranks by player, game data, board info, comment censor/sanitize |
| `POST /ranking_view/sec/get_friend_player_rank` | Friend ranking |
| `POST /ranking_clan_view/func/*` | Answered with "no such ranking"; clans are not modelled |
| `/healthz` | Health check |

## Title passphrases

The console only accepts a view reply signed with HMAC-SHA1 keyed by the title's 128-byte
communication passphrase, which lives in the game executable. `NP_SCORE_TITLES` names a JSON file:

```json
{
  "NPWR01234_00": {
    "passphrase": "<256 hex digits>",
    "boards": { "2": { "ascending": true } }
  }
}
```

Board fields: `ascending` (lower ranks higher), `force_update` (every record replaces the last),
`rank_limit`, `data_limit`, `size_limit`, and an optional display `name` (also allowed on the title). A title missing from the file can still record scores, but
the console rejects its leaderboard views.

Example, Terraria (BLES01938, `NPWR04270_00`): five plain score boards, higher is better, recovered from the
game executable (`research/ps3/terraria-leaderboards.md` in the research repo). The defaults for the limits apply.

```json
{
  "NPWR04270_00": {
    "passphrase": "<256 hex digits>",
    "name": "Terraria",
    "boards": {
      "0": { "name": "Distance" },
      "1": { "name": "Mining" },
      "2": { "name": "Crafting" },
      "3": { "name": "Normal bosses" },
      "4": { "name": "Hard bosses" }
    }
  }
}
```

## Rankings for the website

Set `RANKINGS_ADDR` (for example `:9090`) to serve the rankings contract (`/internal/rankings/boards`,
`/boards/{id}`) on the container network; empty means off. Board ids are `<communication id>.<board>`
and rows carry `account_id` (the OpenPak account id the scores table keys players by), network `np`.
Boards are named from the optional `name` fields in the titles file (title and board) and fall back
to the communication id and `Board N`. Every board is a plain score board; its order follows
`ascending`.

## Run

```sh
cp example.env .env   # fill in required values
set -a; . ./.env; set +a
GOWORK=off go run -mod=vendor .
```

Tests need PostgreSQL: `NP_SCORE_TEST_DATABASE_URL=postgres://… GOWORK=off go test -mod=vendor ./...`

## Docs

- [Protocol](docs/protocol.md)
- [Deploy](docs/deploy.md)

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
