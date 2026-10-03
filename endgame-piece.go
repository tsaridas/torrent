package torrent

import "sort"

// EndgamePiece asks up to perChunk additional peers for each of the first
// maxChunks still-missing chunks of piece pi when that piece is
// PiecePriorityNow. Unlike ShadowRequestAhead (a byte-offset window across
// pieces), this stays on one critical piece — the cold-start "piece 0 /
// probe head" shape — and keeps whichever copy of each chunk lands first.
//
// Same wire accounting as ShadowRequestAhead: validReceiveChunks +
// shadowRequests, requestState untouched so the tracked holder keeps its
// request. A second copy is dropped as redundant. Returns how many requests
// were sent. Cost is at most maxChunks*perChunk duplicate blocks per call.
//
// Candidates are unchoked peers that have the piece, fastest first. No-ops
// when the piece is not Now, is complete, has no missing chunks, or the
// caps are non-positive.
func (t *Torrent) EndgamePiece(pi pieceIndex, maxChunks, perChunk int) int {
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
		return 0
	}
	nChunks := p.numChunks()
	if nChunks <= 0 {
		return 0
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

	sent, chunks := 0, 0
	base := t.pieceRequestIndexOffset(pi)
	for ci := chunkIndexType(0); ci < nChunks && chunks < maxChunks; ci++ {
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
			if asked >= perChunk {
				break
			}
			pc := c.pc
			if &pc.Peer == holder {
				continue
			}
			if pc.validReceiveChunks[ri] > 0 {
				continue
			}
			if pc.validReceiveChunks == nil {
				pc.validReceiveChunks = make(map[RequestIndex]int)
			}
			if pc.shadowRequests == nil {
				pc.shadowRequests = make(map[RequestIndex]struct{})
			}
			pc.validReceiveChunks[ri]++
			pc.shadowRequests[ri] = struct{}{}
			pc._request(t.requestIndexToRequest(ri))
			asked++
			sent++
		}
	}
	if sent > 0 {
		torrent.Add(endgamePieceRequestsVar, int64(sent))
	}
	return sent
}

// endgamePieceRequestsVar is the expvar key (in the "torrent" map) counting
// requests sent by EndgamePiece.
const endgamePieceRequestsVar = "endgame piece requests sent"
