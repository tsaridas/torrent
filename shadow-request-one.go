package torrent

// shadowRequestOne sends a duplicate request for ri on pc without touching
// requestState. The block is accepted when it arrives (validReceiveChunks);
// receiveChunk treats a second copy as redundant and cancels the tracked
// holder plus any other shadow copies. Returns false if pc was already
// expecting that chunk.
func (pc *PeerConn) shadowRequestOne(ri RequestIndex) bool {
	if pc.validReceiveChunks[ri] > 0 {
		return false
	}
	if pc.validReceiveChunks == nil {
		pc.validReceiveChunks = make(map[RequestIndex]int)
	}
	if pc.shadowRequests == nil {
		pc.shadowRequests = make(map[RequestIndex]struct{})
	}
	pc.validReceiveChunks[ri]++
	pc.shadowRequests[ri] = struct{}{}
	pc._request(pc.t.requestIndexToRequest(ri))
	return true
}

func (pc *PeerConn) dropShadowRequest(ri RequestIndex) {
	if _, ok := pc.shadowRequests[ri]; !ok {
		return
	}
	delete(pc.shadowRequests, ri)
	pc.decExpectedChunkReceive(ri)
}

func (pc *PeerConn) dropAllShadowRequests() {
	for ri := range pc.shadowRequests {
		pc.decExpectedChunkReceive(ri)
	}
	pc.shadowRequests = nil
}

func (t *Torrent) cancelShadowCopies(ri RequestIndex, winner *Peer) {
	for pc := range t.conns {
		if pc.closed.IsSet() {
			continue
		}
		if winner != nil && &pc.Peer == winner {
			continue
		}
		if _, ok := pc.shadowRequests[ri]; !ok {
			continue
		}
		pc.dropShadowRequest(ri)
		pc._cancel(ri)
	}
}
