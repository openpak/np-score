# Changelog: np-score

## 0.3.0

- Scores move to the neutral `leaderboards` core (gRPC). `store.go`, the `scores` table, the Postgres
  dependency and the rankings listener (`RANKINGS_ADDR`) are gone; the website reads the rankings
  contract from `leaderboards`. New env `NP_LEADERBOARDS_ADDR` / `NP_LEADERBOARDS_KEY`; drop
  `NP_SCORE_DATABASE_URL`. Passphrase signing, discovery, XML, result codes and the placeholder
  player-rank record are unchanged. Configured boards are declared to the core at start; optional
  `cross_network` per board in `NP_SCORE_TITLES`.

## 0.2.1

- README: Terraria title configuration example (five named boards, descending).

## 0.2.0

- Rankings contract (`/internal/rankings/boards`, `/boards/{id}`) over the scores table, on its own
  listener when `RANKINGS_ADDR` is set (empty = off). Rows carry the OpenPak account id, network `np`,
  game competition ranks, with an `around` slice. Optional `name` on titles and boards in
  `NP_SCORE_TITLES`.

## 0.1.0

- First release: ranking discovery, record score and game data, range/player/friend ranking, board
  info and comment calls on Postgres, view replies signed with per-title passphrases.
