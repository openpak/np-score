# PS3 Score Server

OpenPak's PS3 Score (leaderboard) server.

| | |
| --- | --- |
| Network | PlayStation 3 |
| Role | Native PS3 `/ranking_update/*` and `/ranking_view/*` (score boards, comments, game info, game data) and the signed ranking discovery resource |
| Depends on | [`account`](https://github.com/openpak/account) (account core), [`np-common`](https://github.com/openpak/np-common) (vendored), [`leaderboards`](https://github.com/openpak/leaderboards) (gRPC; holds every score) |
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

## Storage and the website

np-score keeps no scores. Every record, rank, comment and game data blob lives in the neutral
[`leaderboards`](https://github.com/openpak/leaderboards) service (PS3 is network `np`, the
communication id is the title, the board number the board id, the pcId the entry's sub slot), so a
PS3 score is visible across networks unless a board sets `"cross_network": false` in the titles
file. At start np-score declares the configured boards (order, force update, limits, names) to it;
`leaderboards` serves the website's rankings contract itself
(`http://openpak-leaderboards:20210/t/<title>`), so np-score has no rankings listener.

Set `NP_LEADERBOARDS_ADDR` (`host:port`, gRPC) and `NP_LEADERBOARDS_KEY` (its internal key).
Terraria (`NPWR04270_00`) maps to the neutral title `terraria`.

## Run

```sh
cp example.env .env   # fill in required values
set -a; . ./.env; set +a
GOWORK=off go run -mod=vendor .
```

Tests run against an in-process fake leaderboards server: `GOWORK=off go test -mod=vendor ./...`

## Docs

- [Protocol](docs/protocol.md)
- [Deploy](docs/deploy.md)

## Licence

AGPL-3.0-only. See [LICENSE](LICENSE).
