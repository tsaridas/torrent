package torrent

import (
	"cmp"
	"slices"
	"time"
)

// Now-priority preemption behind ClientConfig.NowPriorityPreempt.
//
// Measured through a shaped single-seeder relay (50 Mbit, 40 ms one-way,
// 4 MiB pieces), a seek's first verified byte took ~3.5s, of which a piece's
// own transfer is ~0.7s: the seek's Now blocks were not requested until the
// peer had worked through the ~15 MiB already queued for the old position.
//
// Making room alone barely helps: a peer serves its queue roughly in order,
// so the Now blocks would still wait behind what is queued ahead of them. The
// first preempting Now block in a pass therefore also withdraws the queued
// lower-priority requests beyond nowPreemptKeep, to be requested again behind
// the Now blocks. Measured on the same link: cold seek first byte 3.67s ->
// 1.97s (swap alone: 3.55s), with cold 128 MiB throughput at 200 Mbit 21.0
// -> 22.2 MiB/s.

const peerUpdateRequestsNowPreemptReason updateRequestReason = "Piece.SetPriorityNow preempt"

// Bounds on nowPreemptKeep, in requests. Variables so tests can shrink them.
var (
	nowPreemptMinKeep = 16
	nowPreemptMaxKeep = 128
)

// nowPreemptKeep is how many of a peer's queued lower-priority requests survive
// a preemption: the oldest ones, about what is already on the wire (150ms at
// the peer's measured rate). Cancelling those would not stop their data, only
// cost a message each.
func (p *Peer) nowPreemptKeep() int {
	n := 0
	if cs := int(p.t.chunkSize); cs > 0 {
		n = int(p.downloadRate() * 0.15 / float64(cs))
	}
	return minInt(maxInt(n, nowPreemptMinKeep), nowPreemptMaxKeep)
}

// nowPreemptEnabled reports whether this peer may preempt its own queued
// requests. Webseeds are left alone: they are not served from a request queue
// a cancel can reach.
func (p *Peer) nowPreemptEnabled() bool {
	if p.t == nil || p.t.cl == nil || !p.t.cl.config.NowPriorityPreempt {
		return false
	}
	_, ok := p.peerImpl.(*PeerConn)
	return ok
}

// nowPreemptVictims lists this peer's outstanding requests below
// PiecePriorityNow, the ones to give up first at the front: lowest priority,
// then the most recently requested, which a peer serving requests in order
// would reach last.
func (p *Peer) nowPreemptVictims() []RequestIndex {
	t := p.t
	type victim struct {
		r    RequestIndex
		prio PiecePriority
		when time.Time
	}
	var vs []victim
	p.requestState.Requests.Iterate(func(r RequestIndex) bool {
		if prio := t.piece(t.pieceIndexOfRequestIndex(r)).purePriority(); prio < PiecePriorityNow {
			vs = append(vs, victim{r, prio, t.requestState[r].when})
		}
		return true
	})
	slices.SortFunc(vs, func(a, b victim) int {
		if c := cmp.Compare(a.prio, b.prio); c != 0 {
			return c
		}
		return b.when.Compare(a.when)
	})
	out := make([]RequestIndex, len(vs))
	for i, v := range vs {
		out[i] = v.r
	}
	return out
}

// holdsRequestInPiece reports whether this peer has any outstanding request in
// piece.
func (p *Peer) holdsRequestInPiece(piece pieceIndex) bool {
	t := p.t
	n := RequestIndex(t.pieceNumChunks(piece))
	if n == 0 {
		return false
	}
	start := t.pieceRequestIndexOffset(piece)
	var below uint64
	if start > 0 {
		below = p.requestState.Requests.Rank(start - 1)
	}
	return p.requestState.Requests.Rank(start+n-1) > below
}

// preemptRequest withdraws r to make room for a Now request in the same update
// pass. Unlike cancel it leaves peakRequests alone (nominalMaxRequests must not
// change mid-pass) and does not trigger another update.
func (p *Peer) preemptRequest(r RequestIndex) {
	if !p.deleteRequest(r) {
		panic("preempting a request this peer does not hold")
	}
	if p._cancel(r) {
		if !p.requestState.Cancelled.CheckedAdd(r) {
			panic("request already cancelled")
		}
	}
	torrent.Add("now-priority preempted requests", 1)
}

// triggerNowPreempt re-evaluates the peers that have piece but are not low on
// requests, which onPiecePendingTriggers skips: without this a busy peer would
// not consider a new Now piece until its queue drained.
func (t *Torrent) triggerNowPreempt(piece pieceIndex) {
	if !t._pendingPieces.Contains(uint32(piece)) {
		return
	}
	t.iterPeers(func(c *Peer) {
		if !c.nowPreemptEnabled() || c.isLowOnRequests() || !c.peerHasPiece(piece) {
			return
		}
		if c.peerChoking && !c.peerAllowedFast.Contains(piece) {
			return
		}
		c.updateRequests(peerUpdateRequestsNowPreemptReason)
	})
}
