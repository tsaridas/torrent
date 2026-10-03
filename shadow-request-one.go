package torrent

import "time"

// shadowEntry tracks one duplicate request. Sent is when the Request went on
// the wire; Cancelled is zero while live and set by cancelShadowCopies.
type shadowEntry struct {
	sent      time.Time
	cancelled time.Time
}

func (e shadowEntry) live() bool { return e.cancelled.IsZero() }

// shadowSilentAge is how long a live shadow may occupy a perChunk / peer-budget
// "already asked" slot before EndgamePiece treats it as silent and asks other
// peers. The expectation stays for disconnect safety.
const shadowSilentAge = 500 * time.Millisecond

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
		pc.shadowRequests = make(map[RequestIndex]shadowEntry)
	}
	pc.validReceiveChunks[ri]++
	pc.shadowRequests[ri] = shadowEntry{sent: time.Now()}
	pc.liveShadowCount++
	if pc.t != nil {
		pc.t.numShadowRequests++
	}
	pc._request(pc.t.requestIndexToRequest(ri))
	return true
}

// shadowSlot reports whether ri is a live shadow or a cancelled stub still
// held for disconnect safety. silent is true when a live shadow is older than
// shadowSilentAge (does not count toward perChunk / already-asked).
func (p *Peer) shadowSlot(ri RequestIndex) (live, cancelled, silent bool) {
	e, ok := p.shadowRequests[ri]
	if !ok {
		return false, false, false
	}
	if !e.live() {
		return false, true, false
	}
	silent = !e.sent.IsZero() && time.Since(e.sent) >= shadowSilentAge
	return true, false, silent
}

// shadowPeerRoom is how many more Requests this peer can take before hitting
// PeerMaxRequests, counting tracked requests and live shadows. O(1).
func (p *Peer) shadowPeerRoom() int {
	max := int(p.PeerMaxRequests)
	if max <= 0 {
		return 0
	}
	n := int(p.requestState.Requests.GetCardinality()) + p.liveShadowCount
	if n >= max {
		return 0
	}
	return max - n
}

// clearShadowFlag removes ri from shadowRequests and the torrent count.
// Does not touch validReceiveChunks — callers that drop the expectation
// must also call decExpectedChunkReceive.
func (p *Peer) clearShadowFlag(ri RequestIndex) bool {
	e, ok := p.shadowRequests[ri]
	if !ok {
		return false
	}
	if e.live() && p.liveShadowCount > 0 {
		p.liveShadowCount--
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

// expireCancelledShadowsLocked drops cancelled stubs older than
// shadowCancelExpire. Budget-mode EndgamePiece never calls
// shadowInFlightLocked, so expiry must run here (and from
// cancelShadowCopies) or silent-cancel peers keep stubs forever and
// numShadowRequests never returns to 0.
func (t *Torrent) expireCancelledShadowsLocked() {
	now := time.Now()
	for pc := range t.conns {
		for ri, e := range pc.shadowRequests {
			if e.live() {
				continue
			}
			if now.Sub(e.cancelled) >= shadowCancelExpire {
				pc.dropShadowRequest(ri)
			}
		}
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
	t.expireCancelledShadowsLocked()
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
		e, ok := pc.shadowRequests[ri]
		if !ok {
			continue
		}
		if e.live() {
			if pc.liveShadowCount > 0 {
				pc.liveShadowCount--
			}
		}
		// Mark cancelled; leave expectation until arrival/Reject/expire.
		e.cancelled = now
		pc.shadowRequests[ri] = e
		pc._cancel(ri)
	}
}
