# Playback bugs (stremio-server-golang / n200)

Bugs found while reviewing `now-priority-deadline` against the Go Stremio
server. Status is relative to this branch.

## Bugs that matter

| # | Issue | Status |
|---|---|---|
| 1 | `peerSourceRank` in btree `Less` breaks same-address de-dup (`prioritized-peers.go`) | **fixed** — one entry per address, keep best source (`byAddr`) |
| 2 | `hasWantedNowPriorityPiece` only sees Reader Now; app uses `SetPriorityNow` so slow-start bypass never runs | **fixed** — same `purePriority()==Now` check as the restriction |
| 3 | `EndgamePiece` unreachable from HLS/probe (`useEndgame` required `uv==nil`) | **fixed** (app) — cold file-head endgame runs for unverified too |
| 4 | Duplicate-request cap is per call; skips already-shadowed peers without counting toward `perChunk` | **fixed** — outstanding shadows count; torrent in-flight budget |
| 5 | Verified `ShadowRequestAhead` spills into later pieces while blocking piece hashes | **fixed** (app) — verified waits always use `EndgamePiece` (incl. after cold-head rounds) |
| 6 | `downloadRate` can be +Inf from shadow-only bytes / zero expecting time | **fixed** — rate from intended bytes; guard zero / Inf |
| 7 | `Peer.request` can send a second wire request when a shadow already exists | **fixed** — promote shadow into `requestState` |
| 8 | `PieceHashFailures` misses failures when `storageCompletionOk` is still false | **fixed** — count when `allChunksDirty` / dirtiers |
| 9 | `ReadableUnverifiedLen` can spin on piece-aligned v2/hybrid (`pieceEnd-pos==0`) | **fixed** — break when advance `<= 0` |
| 10 | Cancelled shadows on silent-cancel peers permanently fill `maxShadowInFlight` | **fixed** — live+missing only; cancelled stubs expire after 30s (kept for disconnect safety, not budget) |
| 11 | A failed DHT announce was retried at once, in a loop (~47k warnings/s on one torrent) | **fixed** (`746c4ec`) — back off 1 s doubling to 1 min, reset on success |

## Performance

| # | Issue | Status |
|---|---|---|
| P1 | `nominalMaxRequests()` on every heap pop (walks all conns with pipeline on) | **fixed** — once per pass; unchoked count maintained on choke/unchoke/close |
| P2 | `cancelPiecesOutsideFile` one write lock per piece (app `engine.go`) | **fixed** (app) — `CancelPieces` ranges |
| P3 | Losing shadow copies never cancelled | **fixed** — cancel without dropping expectation until arrival/reject/expire |
| P4 | Pipeline refill only when queue empty | won't fix: `EarlyRequestRefill` (`ababe89`) was tried and removed, see below |
| P5 | No per-chunk staleness gate before duplicating | open |
| P6 | Promote/demote whole file on every stream flap | open (needs approval) |

## Options tried for the app (2026-11-21/23)

Each was added off by default and A/B tested on real swarms, by time to
the first decoded frame against the same build with it off
(stremio-server-golang `docs/2026-11-21-mac-ab-native-player-benchmark.md`,
runs 7–9). Only `UTPDialDelay` is left.

| Option | Commit | What it does | Outcome |
|---|---|---|---|
| `EarlyRequestRefill` | `ababe89` | Refill a peer at half its window instead of at empty; a top-up keeps `peakRequests` | removed: first frame after a seek 64% later (paired) |
| `BDPRequestWindow` | `ababe89` | Let a proven peer's window grow to 2× its measured BDP (cap 256) | removed: no effect on any controlled path |
| `NowPriorityPreempt` | `2ab20cc` | A piece raised to Now cancels a busy peer's queued lower-priority requests (keeps ~150 ms of its rate) and queues its blocks first | removed: no stable first-frame effect, 12% less throughput |
| `UTPDialDelay` | `adc22ad` | Hold a uTP dial behind TCP to the same address; start it once TCP fails, drop it if TCP connects; holepunch dials are never held | off with uTP (`STREMIOSRV_BT_UTP`); 500 ms when uTP is on |
| `MSEInitialPayloadHandshake` | `adc22ad` | Send the BitTorrent handshake as the MSE initial payload: one round trip less per obfuscated connection | removed: fewer completed handshakes, no first-frame gain |

## Smaller

- `TotalLength()` / multi lock on unverified poll — open
- `prioritizedPeersItem.Less` builds address string+hash each compare — open
- `stats.json` calls `Torrent.Stats()` many times per request — open (app)
- `NowPriorityStealBySpeed` callback fires before grace veto — open
- Cap / restrictedNowCap comments say "per piece" but count is torrent-wide — open (docs)
- `NewDefaultClientConfig` reads `TORRENT_STEAL_REQUEST_GRACE` (can panic) — open

## Notes

- `endgame_piece_requests` / `endgame_not_now_skips` are process-wide expvars;
  the HLS probe log prints them. After #3, probe-path waits can increment them.
- Cold file-head endgame is **peer-budget whole-piece** by default (app
  `endgameEvery=200ms`; A/B-off restores 32×6); mid-file verified stays
  on `EndgamePiece` (**16×2** either way). One EndgamePiece pass per
  **piece** per 150ms (below the app's ~180ms gate); `liveShadowCount`
  keeps room O(1). Cancelled-stub expiry is at most once/second.
  Probe-tail uses `NoShadow` (Now boost, no duplicates) so it does not
  share the shadow budget with the cold head.
- `cancelShadowCopies` sends cancel but **keeps** validReceiveChunks +
  shadow flag until arrival/Reject/`shadowCancelExpire` (30s). Clearing
  first dropped fast peers; counting cancelled stubs filled the budget
  and starved EndgamePiece after a cancel burst.
- `shadowInFlightLocked` counts only **live** shadows whose chunk is
  still missing; cancelled peers free `perChunk` slots for a new peer.
- Pending-peer de-dup uses `byAddr` (O(1) per add).
- `hasWantedNowPriorityPiece` only when `peakRequests==0 && bypass>0`.
- `PieceHashFailures` requires `allChunksDirty` when `storageCompletionOk`
  is still false.
