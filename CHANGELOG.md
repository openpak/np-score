# Changelog: np-score

## 0.2.0

- Rankings contract (`/internal/rankings/boards`, `/boards/{id}`) over the scores table, on its own
  listener when `RANKINGS_ADDR` is set (empty = off). Rows carry the OpenPak account id, network `np`,
  game competition ranks, with an `around` slice. Optional `name` on titles and boards in
  `NP_SCORE_TITLES`.

## 0.1.0

- First release: ranking discovery, record score and game data, range/player/friend ranking, board
  info and comment calls on Postgres, view replies signed with per-title passphrases.
