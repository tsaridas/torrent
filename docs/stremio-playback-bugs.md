# Playback bugs (stremio-server-golang / n200)

Bugs found while reviewing `now-priority-deadline` against the Go Stremio
server. Status is relative to this branch.

## Bugs that matter

| # | Issue | Status |
|---|---|---|
| 1 | `peerSourceRank` in btree `Less` breaks same-address de-dup (`prioritized-peers.go`) | **fixed** — one entry per address, keep best source |
| 2 | `hasWantedNowPriorityPiece` only sees Reader Now; app uses `SetPriorityNow` so slow-start bypass never runs | **fixed** — same `purePriority()==Now` check as the restriction |
| 3 | `EndgamePiece` unreachable from HLS/probe (`useEndgame` required `uv==nil`) | **fixed** (app) — cold file-head endgame runs for unverified too |
| 4 | Duplicate-request cap is per call; skips already-shadowed peers without counting toward `perChunk` | **fixed** — outstanding shadows count; torrent in-flight budget |
| 5 | Verified `ShadowRequestAhead` spills into later pieces while blocking piece hashes | **fixed** (app) — verified wait uses `EndgamePiece` (one piece) |
| 6 | `downloadRate` can be +Inf from shadow-only bytes / zero expecting time | **fixed** — rate from intended bytes; guard zero / Inf |
| 7 | `Peer.request` can send a second wire request when a shadow already exists | **fixed** — promote shadow into `requestState` |
| 8 | `PieceHashFailures` misses failures when `storageCompletionOk` is still false | **fixed** — count when session received bytes / dirtiers |
| 9 | `ReadableUnverifiedLen` can spin on piece-aligned v2/hybrid (`pieceEnd-pos==0`) | **fixed** — break when advance `<= 0` |

## Performance

| # | Issue | Status |
|---|---|---|
| P1 | `nominalMaxRequests()` on every heap pop (walks all conns with pipeline on) | **fixed** — compute once per request pass |
| P2 | `cancelPiecesOutsideFile` one write lock per piece (app `engine.go`) | **fixed** (app) — `CancelPieces` ranges |
| P3 | Losing shadow copies never cancelled | **fixed** — cancel without dropping expectation until arrival/reject |
| P4 | Pipeline refill only when queue empty | open (low confidence) |
| P5 | No per-chunk staleness gate before duplicating | open |
| P6 | Promote/demote whole file on every stream flap | open (needs approval) |

## Smaller

- `TotalLength()` / multi lock on unverified poll — open
- `prioritizedPeersItem.Less` builds address string+hash each compare — open
- `stats.json` calls `Torrent.Stats()` many times per request — open (app)

## Notes

- `endgame_piece_requests` / `endgame_not_now_skips` are process-wide expvars;
  the HLS probe log prints them. After #3, probe-path waits can increment them.
- Cap for cold file-head endgame is **2 rounds**, then WaitAndRead falls
  back to ShadowRequestAhead (4×2 unverified / 16×2 verified).
- `cancelShadowCopies` sends cancel but **keeps** validReceiveChunks +
  shadow flag until arrival/reject (clearing first dropped fast peers).
- Pending-peer de-dup uses `byAddr` (O(1) per add).
- `hasWantedNowPriorityPiece` only when `peakRequests==0 && bypass>0`.
- `PieceHashFailures` requires `allChunksDirty` when `storageCompletionOk`
  is still false.
