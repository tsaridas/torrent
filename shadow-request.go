package torrent

import (
	"sort"
	"time"
)

// maxShadowInFlight is a soft cap on outstanding *live* shadow requests
// across all peers of one torrent. Stops a long wait from ramping
// duplicates toward peers×chunks as each tick slides past already-shadowed
// peers. Sized for concurrent playhead EndgamePiece (32×6) plus a second
// reader without starving the cold head; cancelled stubs do not count.
const maxShadowInFlight = 512

// shadowCancelExpire is how long a cancelled shadow may keep its expectation
// after cancelShadowCopies. Peers without the fast extension (and Transmission
// before 4) honour cancel silently — without an expire those stubs would
// linger forever (disconnect-safety map + validReceiveChunks). Dropping
// immediately reintroduces the unexpected-chunk disconnect. Cancelled stubs
// do not count against maxShadowInFlight.
const shadowCancelExpire = 30 * time.Second

// ShadowRequestAhead sends duplicate requests for the first maxChunks
// not-yet-received chunks at or after torrent offset off, each to up to
// perChunk additional peers, and returns how many requests it sent.
//
// This is libtorrent's time-critical shape, reduced to its core: when a
// reader is blocked on a block, ask more than one peer for it and keep
// whichever copy lands first. Our steal logic can only *move* a request, and
// only once someone has proved faster; at cold start nobody has, and a block
// assigned to a silent peer waits for the stall rule. Measured on n200 with
// block-level probe reads: the first block of the file sometimes arrived
// 5.7s after the read began, held by a 0 B/s peer, while other peers were
// delivering.
//
// A shadow request is sent on the wire and counted in validReceiveChunks, so
// the block is accepted when it arrives -- receiveChunk then treats it as
// unintended-but-useful, writes it, and cancels the tracked holder's request.
// A copy arriving second is dropped as redundant. requestState is untouched,
// so the tracked holder keeps its request until one of those happens.
// Peers that already have an outstanding (shadow) receive for a chunk count
// toward perChunk without sending again. Total outstanding shadows on the
// torrent are also bounded by maxShadowInFlight.
//
// Candidates are unchoked peers that have the piece, fastest first; the
// tracked holder is skipped.
func (t *Torrent) ShadowRequestAhead(off int64, maxChunks, perChunk int) int {
	if off < 0 || maxChunks <= 0 || perChunk <= 0 {
		return 0
	}
	t.cl.lock()
	defer t.cl.unlock()
	if !t.haveInfo() {
		return 0
	}
	pieceLen := int64(t.info.PieceLength)
	total := t.info.TotalLength()
	chunkSize := int64(t.chunkSize)
	if pieceLen <= 0 || chunkSize <= 0 {
		return 0
	}
	type cand struct {
		pc   *PeerConn
		rate float64
	}
	var conns []cand
	for pc := range t.conns {
		if pc.closed.IsSet() {
			continue
		}
		conns = append(conns, cand{pc, pc.downloadRate()})
	}
	if len(conns) == 0 {
		return 0
	}
	sort.Slice(conns, func(i, j int) bool { return conns[i].rate > conns[j].rate })

	inFlight := t.shadowInFlightLocked()
	sent, chunks := 0, 0
	for pos := off; pos < total && chunks < maxChunks && inFlight < maxShadowInFlight; {
		pi := pieceIndex(pos / pieceLen)
		pieceStart := int64(pi) * pieceLen
		if t.pieceComplete(pi) {
			pos = pieceStart + int64(t.pieceLength(pi))
			continue
		}
		p := t.piece(pi)
		ci := chunkIndexType((pos - pieceStart) / chunkSize)
		pos = pieceStart + (int64(ci)+1)*chunkSize
		if p.chunkIndexDirty(ci) {
			continue
		}
		chunks++
		ri := t.pieceRequestIndexOffset(pi) + RequestIndex(ci)
		holder := t.requestingPeer(ri)
		asked := 0
		for _, c := range conns {
			if asked >= perChunk || inFlight >= maxShadowInFlight {
				break
			}
			pc := c.pc
			if &pc.Peer == holder || !pc.peerHasPiece(pi) || pc.peerChoking && !pc.peerAllowedFast.Contains(pi) {
				continue
			}
			if live, cancelled := pc.shadowSlot(ri); live {
				// Already racing — counts toward the cap so the next round
				// does not walk on to every other peer.
				asked++
				continue
			} else if cancelled {
				// Told to stop; skip this peer but free the perChunk slot
				// so a different peer can be asked.
				continue
			}
			if pc.validReceiveChunks[ri] > 0 {
				asked++
				continue
			}
			if !pc.shadowRequestOne(ri) {
				continue
			}
			asked++
			sent++
			inFlight++
		}
	}
	if sent > 0 {
		torrent.Add(shadowRequestsSentVar, int64(sent))
	}
	return sent
}

// shadowInFlightLocked counts live shadows that still compete for a missing
// chunk. Cancelled entries older than shadowCancelExpire are dropped; until
// then they stay for disconnect safety but do not reserve budget slots
// (otherwise a cancel burst fills maxShadowInFlight and EndgamePiece
// returns 0 while the playhead is starved). Received chunks do not count.
func (t *Torrent) shadowInFlightLocked() int {
	now := time.Now()
	n := 0
	for pc := range t.conns {
		for ri, cancelledAt := range pc.shadowRequests {
			if !cancelledAt.IsZero() {
				if now.Sub(cancelledAt) >= shadowCancelExpire {
					pc.dropShadowRequest(ri)
				}
				continue
			}
			if !t.shadowChunkStillMissing(ri) {
				continue
			}
			n++
		}
	}
	return n
}

// shadowChunkStillMissing reports whether ri is still worth racing for.
func (t *Torrent) shadowChunkStillMissing(ri RequestIndex) bool {
	if !t.haveInfo() {
		return false
	}
	pi := t.pieceIndexOfRequestIndex(ri)
	if int(pi) >= t.numPieces() || t.pieceComplete(pi) {
		return false
	}
	return !t.dirtyChunks.Contains(ri)
}

// shadowRequestsSentVar is the expvar key (in the "torrent" map) counting
// requests sent by ShadowRequestAhead.
const shadowRequestsSentVar = "shadow requests sent"
