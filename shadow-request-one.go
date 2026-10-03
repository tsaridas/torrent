package torrent

import "time"

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
		pc.shadowRequests = make(map[RequestIndex]time.Time)
	}
	pc.validReceiveChunks[ri]++
	pc.shadowRequests[ri] = time.Time{} // live; cancelShadowCopies sets cancelledAt
	if pc.t != nil {
		pc.t.numShadowRequests++
	}
	pc._request(pc.t.requestIndexToRequest(ri))
	return true
}

// clearShadowFlag removes ri from shadowRequests and the torrent count.
// Does not touch validReceiveChunks — callers that drop the expectation
// must also call decExpectedChunkReceive.
func (p *Peer) clearShadowFlag(ri RequestIndex) bool {
	if _, ok := p.shadowRequests[ri]; !ok {
		return false
	}
	delete(p.shadowRequests, ri)
	if p.t != nil && p.t.numShadowRequests > 0 {
		p.t.numShadowRequests--
	}
	return true
}

func (p *Peer) dropShadowRequest(ri RequestIndex) {
	if !p.clearShadowFlag(ri) {
		return
	}
	p.decExpectedChunkReceive(ri)
}

func (p *Peer) dropAllShadowRequests() {
	for ri := range p.shadowRequests {
		p.dropShadowRequest(ri)
	}
}

// cancelShadowCopies tells losing peers to stop sending a chunk that already
// arrived. Keep validReceiveChunks and the shadow flag until the in-flight
// copy or Reject lands -- Peer.cancel does the same for tracked requests.
// Clearing them first made receiveChunk treat a late copy as unexpected and
// dropped the connection (often a fast peer). Peers that honour cancel
// silently (no fast extension, old Transmission) keep the entry until
// shadowCancelExpire so they do not permanently fill maxShadowInFlight.
func (t *Torrent) cancelShadowCopies(ri RequestIndex, winner *Peer) {
	if t.numShadowRequests == 0 {
		return
	}
	now := time.Now()
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
		// Mark cancelled; leave expectation until arrival/Reject/expire.
		pc.shadowRequests[ri] = now
		pc._cancel(ri)
	}
}
