package torrent

// EndgamePiece asks every capable peer for every still-dirty chunk of piece
// pi when that piece is PiecePriorityNow. Unlike ShadowRequestAhead (a capped
// number of extra peers on a byte-offset window), this is the cold-start
// "piece 0 / probe head" shape: fill one critical piece from the whole swarm
// and keep whichever copy of each chunk lands first.
//
// Same wire accounting as ShadowRequestAhead: validReceiveChunks +
// shadowRequests, requestState untouched so the tracked holder keeps its
// request. A second copy is dropped as redundant. Returns how many requests
// were sent.
//
// No-ops when the piece is not Now, is complete, or has no dirty chunks.
func (t *Torrent) EndgamePiece(pi pieceIndex) int {
	if pi < 0 {
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
	var peers []*PeerConn
	for pc := range t.conns {
		if pc.closed.IsSet() {
			continue
		}
		if !pc.peerHasPiece(pi) || (pc.peerChoking && !pc.peerAllowedFast.Contains(pi)) {
			continue
		}
		peers = append(peers, pc)
	}
	if len(peers) == 0 {
		return 0
	}
	sent := 0
	base := t.pieceRequestIndexOffset(pi)
	for ci := chunkIndexType(0); ci < nChunks; ci++ {
		// dirtyChunks means received (same as ShadowRequestAhead): only ask
		// for chunks we still need.
		if p.chunkIndexDirty(ci) {
			continue
		}
		ri := base + RequestIndex(ci)
		holder := t.requestingPeer(ri)
		for _, pc := range peers {
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
