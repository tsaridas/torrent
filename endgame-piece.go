package torrent

import "sort"

// EndgamePiece asks up to perChunk additional peers for each of the first
// maxChunks still-missing chunks of piece pi at or after torrent byte offset
// off, when that piece is PiecePriorityNow. Unlike ShadowRequestAhead (a
// byte-offset window that may cross pieces), this stays on one critical
// piece — the cold-start "piece 0 / probe head" shape — and keeps whichever
// copy of each chunk lands first.
//
// off should be the file start (or read head) in torrent coordinates so a
// multi-file title that begins mid-piece does not duplicate the previous
// file's leading chunks. Negative off is treated as the piece start.
//
// Same wire accounting as ShadowRequestAhead (via shadowRequestOne):
// requestState untouched so the tracked holder keeps its request. A second
// copy is dropped as redundant. Peers that already have an outstanding
// receive for a chunk count toward perChunk. Total outstanding shadows are
// also bounded by maxShadowInFlight. Returns how many requests were sent.
//
// Candidates are unchoked peers that have the piece, fastest first. No-ops
// when the piece is not Now, is complete, has no missing chunks, or the
// caps are non-positive. A not-Now skip increments
// endgamePieceNotNowSkipsVar so a demoted piece is visible in expvar.
func (t *Torrent) EndgamePiece(pi pieceIndex, off int64, maxChunks, perChunk int) int {
	if pi < 0 || maxChunks <= 0 || perChunk <= 0 {
		return 0
	}
	t.cl.lock()
	defer t.cl.unlock()
	if !t.haveInfo() || int(pi) >= t.numPieces() {
		return 0
	}
	if t.pieceComplete(pi) {
		return 0
	}
	p := t.piece(pi)
	if p.purePriority() != PiecePriorityNow {
		torrent.Add(endgamePieceNotNowSkipsVar, 1)
		return 0
	}
	nChunks := p.numChunks()
	if nChunks <= 0 {
		return 0
	}
	pieceLen := int64(t.info.PieceLength)
	chunkSize := int64(t.chunkSize)
	if pieceLen <= 0 || chunkSize <= 0 {
		return 0
	}
	pieceStart := int64(pi) * pieceLen
	startCI := chunkIndexType(0)
	if off > pieceStart {
		startCI = chunkIndexType((off - pieceStart) / chunkSize)
		if startCI >= nChunks {
			return 0
		}
	}
	type cand struct {
		pc   *PeerConn
		rate float64
	}
	var peers []cand
	for pc := range t.conns {
		if pc.closed.IsSet() {
			continue
		}
		if !pc.peerHasPiece(pi) || (pc.peerChoking && !pc.peerAllowedFast.Contains(pi)) {
			continue
		}
		peers = append(peers, cand{pc, pc.downloadRate()})
	}
	if len(peers) == 0 {
		return 0
	}
	sort.Slice(peers, func(i, j int) bool { return peers[i].rate > peers[j].rate })

	inFlight := t.shadowInFlightLocked()
	sent, chunks := 0, 0
	base := t.pieceRequestIndexOffset(pi)
	for ci := startCI; ci < nChunks && chunks < maxChunks && inFlight < maxShadowInFlight; ci++ {
		// dirtyChunks means received (same as ShadowRequestAhead): only ask
		// for chunks we still need.
		if p.chunkIndexDirty(ci) {
			continue
		}
		chunks++
		ri := base + RequestIndex(ci)
		holder := t.requestingPeer(ri)
		asked := 0
		for _, c := range peers {
			if asked >= perChunk || inFlight >= maxShadowInFlight {
				break
			}
			pc := c.pc
			if &pc.Peer == holder {
				continue
			}
			if live, cancelled := pc.shadowSlot(ri); live {
				asked++
				continue
			} else if cancelled {
				// Free the perChunk slot; peer was told to stop.
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
		torrent.Add(endgamePieceRequestsVar, int64(sent))
	}
	return sent
}

const (
	// endgamePieceRequestsVar counts requests sent by EndgamePiece.
	endgamePieceRequestsVar = "endgame piece requests sent"
	// endgamePieceNotNowSkipsVar counts EndgamePiece calls that found the
	// piece no longer at PiecePriorityNow (demoted while a reader waited).
	endgamePieceNotNowSkipsVar = "endgame piece not-now skips"
)
