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

// shadowExpireInterval is how often expireCancelledShadowsLocked may walk all
// conns. cancelShadowCopies runs per received chunk; without this throttle a
// cold-start endgame with thousands of stubs would re-scan them under the
// client lock on every chunk.
const shadowExpireInterval = time.Second

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
		pc.t.addShadowHolder(ri, &pc.Peer)
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
	if p.t != nil {
		if p.t.numShadowRequests > 0 {
			p.t.numShadowRequests--
		}
		p.t.removeShadowHolder(ri, p)
	}
	return true
}

func (t *Torrent) addShadowHolder(ri RequestIndex, p *Peer) {
	if t.shadowHolders == nil {
		t.shadowHolders = make(map[RequestIndex]map[*Peer]struct{})
	}
	m := t.shadowHolders[ri]
	if m == nil {
		m = make(map[*Peer]struct{}, 2)
		t.shadowHolders[ri] = m
	}
	m[p] = struct{}{}
}

func (t *Torrent) removeShadowHolder(ri RequestIndex, p *Peer) {
	m := t.shadowHolders[ri]
	if m == nil {
		return
	}
	delete(m, p)
	if len(m) == 0 {
		delete(t.shadowHolders, ri)
	}
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
// shadowInFlightLocked, so expiry must run from cancelShadowCopies /
// EndgamePiece or silent-cancel peers keep stubs forever and
// numShadowRequests never returns to 0.
func (t *Torrent) expireCancelledShadowsLocked() {
	now := time.Now()
	t.lastShadowExpire = now
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

// maybeExpireCancelledShadowsLocked runs expireCancelledShadowsLocked at most
// once per shadowExpireInterval.
func (t *Torrent) maybeExpireCancelledShadowsLocked() {
	if !t.lastShadowExpire.IsZero() && time.Since(t.lastShadowExpire) < shadowExpireInterval {
		return
	}
	t.expireCancelledShadowsLocked()
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
	t.maybeExpireCancelledShadowsLocked()
	if t.numShadowRequests == 0 {
		return
	}
	holders := t.shadowHolders[ri]
	if len(holders) == 0 {
		return
	}
	now := time.Now()
	for p := range holders {
		if p == winner {
			continue
		}
		pc, ok := p.TryAsPeerConn()
		if !ok || pc.closed.IsSet() {
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
