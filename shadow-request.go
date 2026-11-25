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
	total := t.length()
	chunkSize := int64(t.chunkSize)
	if pieceLen <= 0 || chunkSize <= 0 {
		return 0
	}
	var conns []shadowPeerCand
	for pc := range t.conns {
		if pc.closed.IsSet() {
			continue
		}
		if pc.shadowPeerRoom() <= 0 {
			continue
		}
		conns = append(conns, shadowPeerCand{pc, pc.downloadRate()})
	}
	if len(conns) == 0 {
		return 0
	}
	t.sortShadowPeers(conns)

	inFlight := t.shadowInFlightLocked()
	sent, chunks := 0, 0
	sendChunk := func(pi pieceIndex, ri RequestIndex) {
		chunks++
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
			if pc.shadowPeerRoom() <= 0 {
				continue
			}
			if live, cancelled, silent := pc.shadowSlot(ri); live {
				if silent {
					// Free the already-asked slot so other peers can race.
					continue
				}
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

	pos := off
	// When Piece0Spread is on for whole-piece waits (maxChunks >= 8), rank the
	// blocking piece's missing chunks by worst holder first (unrequested, then
	// stalled/oldest 0 B/s holder, then lowest holder rate) so stragglers anywhere
	// in piece 0 are shadowed before chunks already arriving from fast peers.
	if t.cl.config.Piece0Spread && maxChunks >= 8 && pos < total {
		pi0 := pieceIndex(pos / pieceLen)
		if int(pi0) < t.numPieces() {
			pieceStart := int64(pi0) * pieceLen
			pieceEnd := pieceStart + int64(t.pieceLength(pi0))
			if pieceEnd > pos && !t.pieceComplete(pi0) {
				p := t.piece(pi0)
				startCI := chunkIndexType((pos - pieceStart) / chunkSize)
				nChunks := p.numChunks()
				type chunkCand struct {
					ci       chunkIndexType
					ri       RequestIndex
					unreq    bool
					rate     float64
					reqSince time.Time
				}
				var cands []chunkCand
				base := t.pieceRequestIndexOffset(pi0)
				for ci := startCI; ci < nChunks; ci++ {
					if p.chunkIndexDirty(ci) {
						continue
					}
					ri := base + RequestIndex(ci)
					h := t.requestingPeer(ri)
					c := chunkCand{ci: ci, ri: ri}
					if h == nil {
						c.unreq = true
					} else {
						c.rate = h.downloadRate()
						c.reqSince = t.requestState[ri].when
					}
					cands = append(cands, c)
				}
				sort.Slice(cands, func(i, j int) bool {
					a, b := cands[i], cands[j]
					if a.unreq != b.unreq {
						return a.unreq
					}
					aZero, bZero := !a.unreq && a.rate == 0, !b.unreq && b.rate == 0
					if aZero != bZero {
						return aZero
					}
					if aZero && !a.reqSince.Equal(b.reqSince) {
						return a.reqSince.Before(b.reqSince)
					}
					if a.rate != b.rate {
						return a.rate < b.rate
					}
					return a.ci < b.ci
				})
				for _, c := range cands {
					if chunks >= maxChunks || inFlight >= maxShadowInFlight {
						break
					}
					sendChunk(pi0, c.ri)
				}
				pos = pieceEnd
			}
		}
	}

	for pos < total && chunks < maxChunks && inFlight < maxShadowInFlight {
		pi := pieceIndex(pos / pieceLen)
		if int(pi) >= t.numPieces() {
			break
		}
		pieceStart := int64(pi) * pieceLen
		pieceEnd := pieceStart + int64(t.pieceLength(pi))
		if pieceEnd <= pos {
			nextPiecePos := pieceStart + pieceLen
			if nextPiecePos <= pos {
				break
			}
			pos = nextPiecePos
			continue
		}
		if t.pieceComplete(pi) {
			pos = pieceEnd
			continue
		}
		p := t.piece(pi)
		ci := chunkIndexType((pos - pieceStart) / chunkSize)
		if ci >= p.numChunks() {
			pos = pieceEnd
			continue
		}
		chunkEnd := pieceStart + (int64(ci)+1)*chunkSize
		if chunkEnd > pieceEnd {
			chunkEnd = pieceEnd
		}
		pos = chunkEnd
		if p.chunkIndexDirty(ci) {
			continue
		}
		sendChunk(pi, t.pieceRequestIndexOffset(pi)+RequestIndex(ci))
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
	t.maybeExpireCancelledShadowsLocked()
	n := 0
	for pc := range t.conns {
		for ri, e := range pc.shadowRequests {
			if !e.live() {
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
