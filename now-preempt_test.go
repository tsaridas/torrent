package torrent

import (
	"testing"
	"time"

	"github.com/go-quicktest/qt"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

// preemptTestTorrent is the greeting torrent (3 pieces; chunk size 2, so
// pieces 0 and 1 have 3 requests each and piece 2 has 2) with one connected
// peer that has every piece and a window of 3 requests. Pieces 0 and 1 are
// wanted at Normal; piece 2 is not wanted yet. Piece 2 being smaller than the
// window is what makes a swap look like a shrinking peak.
func preemptTestTorrent(t *testing.T, preempt bool) (*Torrent, *PeerConn) {
	t.Helper()
	tor := greetingTorrent(t)
	// greetingTorrent queues an initial check of every piece. Let it finish:
	// a hasher still running would change piece state under the test.
	for deadline := time.Now().Add(10 * time.Second); ; {
		tor.cl.lock()
		busy := !tor.piecesQueuedForHash.IsEmpty()
		for i := range tor.pieces {
			busy = busy || tor.piece(i).hashing || tor.piece(i).marking
		}
		tor.cl.unlock()
		if !busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("initial piece check did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	tor.cl.config.NowPriorityPreempt = preempt
	a, _ := endgameTestPeers(t, tor)
	tor.cl.lock()
	tor.initialPieceCheckDisabled = true
	a._peerPieces.Add(1)
	a._peerPieces.Add(2)
	a.PeerMaxRequests = 3
	a.peakRequests = 2
	tor.cl.unlock()
	tor.piece(0).SetPriority(PiecePriorityNormal)
	tor.piece(1).SetPriority(PiecePriorityNormal)
	return tor, a
}

// updatePass runs one request update for p, as the writer would.
func updatePass(tor *Torrent, p *PeerConn, reason updateRequestReason) {
	tor.cl.lock()
	defer tor.cl.unlock()
	p.needRequestUpdate = reason
	p.maybeUpdateActualRequestState()
}

func requestsOf(p *PeerConn) (rs []RequestIndex) {
	p.t.cl.lock()
	defer p.t.cl.unlock()
	p.requestState.Requests.Iterate(func(r RequestIndex) bool {
		rs = append(rs, r)
		return true
	})
	return
}

func TestNowPreemptVictimsLowestPriorityThenNewest(t *testing.T) {
	tor, a := preemptTestTorrent(t, true)
	tor.piece(2).SetPriority(PiecePriorityHigh)
	tor.cl.lock()
	defer tor.cl.unlock()
	p1 := tor.pieceRequestIndexOffset(1)
	p2 := tor.pieceRequestIndexOffset(2)
	for _, r := range []RequestIndex{p1, p1 + 1, p2} {
		_, err := a.request(r)
		qt.Assert(t, qt.IsNil(err))
	}
	now := time.Now()
	tor.requestState[p1] = requestState{peer: &a.Peer, when: now.Add(-3 * time.Second)}
	tor.requestState[p1+1] = requestState{peer: &a.Peer, when: now.Add(-time.Second)}
	tor.requestState[p2] = requestState{peer: &a.Peer, when: now}
	// Normal before High; within Normal, the newest first.
	qt.Check(t, qt.DeepEquals(a.nowPreemptVictims(), []RequestIndex{p1 + 1, p1, p2}))
}

func TestNowPreemptReplacesQueuedRequestsWithNowBlocks(t *testing.T) {
	tor, a := preemptTestTorrent(t, true)
	updatePass(tor, a, "fill")
	queued := requestsOf(a)
	qt.Assert(t, qt.HasLen(queued, 3), qt.Commentf("window of 3 filled from the Normal pieces"))
	p2 := tor.pieceRequestIndexOffset(2)
	tor.cl.lock()
	oldest := queued[0]
	for _, r := range queued {
		qt.Assert(t, qt.IsTrue(r < p2))
		if tor.requestState[r].when.Before(tor.requestState[oldest].when) {
			oldest = r
		}
	}
	tor.cl.unlock()
	peak := a.peakRequests

	// A reader blocks on piece 2. The peer is busy, so only preemption
	// re-evaluates it.
	tor.piece(2).SetPriorityNow()
	tor.cl.lock()
	reason := a.needRequestUpdate
	tor.cl.unlock()
	qt.Check(t, qt.Equals(reason, peerUpdateRequestsNowPreemptReason))

	updatePass(tor, a, reason)
	// Piece 2's two blocks replace the two most recently queued requests;
	// the oldest, which an in-order peer serves first, stays.
	qt.Check(t, qt.DeepEquals(requestsOf(a), []RequestIndex{oldest, p2, p2 + 1}))
	qt.Check(t, qt.Equals(a.peakRequests, peak),
		qt.Commentf("swapping two requests must not resize the window"))
}

func TestNowPreemptOffLeavesTheQueue(t *testing.T) {
	tor, a := preemptTestTorrent(t, false)
	updatePass(tor, a, "fill")
	queued := requestsOf(a)
	qt.Assert(t, qt.HasLen(queued, 3))

	tor.piece(2).SetPriorityNow()
	tor.cl.lock()
	reason := a.needRequestUpdate
	tor.cl.unlock()
	qt.Check(t, qt.Equals(reason, updateRequestReason("")),
		qt.Commentf("a busy peer is not re-evaluated without preemption"))
	updatePass(tor, a, "later")
	qt.Check(t, qt.DeepEquals(requestsOf(a), queued))
}

func TestNowPreemptNotOnReject(t *testing.T) {
	tor, a := preemptTestTorrent(t, true)
	updatePass(tor, a, "fill")
	queued := requestsOf(a)
	tor.piece(2).SetPriorityNow()
	updatePass(tor, a, peerUpdateRequestsRemoteRejectReason)
	qt.Check(t, qt.DeepEquals(requestsOf(a), queued),
		qt.Commentf("a reject-triggered pass must not add or swap requests"))
}

// A queue can hold more requests than the window once a cancel or reject has
// lowered peakRequests. Preemption must first cancel down below the window,
// or request() refuses the Now block ("too many outstanding requests").
func TestNowPreemptWithQueueAboveTheWindow(t *testing.T) {
	tor, a := preemptTestTorrent(t, true)
	updatePass(tor, a, "fill")
	qt.Assert(t, qt.HasLen(requestsOf(a), 3))
	tor.cl.lock()
	a.PeerMaxRequests = 2 // window 2, three requests outstanding
	tor.cl.unlock()
	tor.piece(2).SetPriorityNow()
	updatePass(tor, a, peerUpdateRequestsNowPreemptReason)
	p2 := tor.pieceRequestIndexOffset(2)
	qt.Check(t, qt.DeepEquals(requestsOf(a), []RequestIndex{p2, p2 + 1}))
}

// With the fast extension a cancel is acknowledged with a reject, and the
// preempted request waits in Cancelled until then: it must not be requested
// again in the same pass.
func TestNowPreemptFastPeerWaitsForRejects(t *testing.T) {
	tor, a := preemptTestTorrent(t, true)
	tor.cl.lock()
	a.PeerExtensionBytes.SetBit(pp.ExtensionBitFast, true)
	qt.Assert(t, qt.IsTrue(a.fastEnabled()))
	tor.cl.unlock()
	updatePass(tor, a, "fill")
	qt.Assert(t, qt.HasLen(requestsOf(a), 3))

	tor.piece(2).SetPriorityNow()
	updatePass(tor, a, peerUpdateRequestsNowPreemptReason)
	p2 := tor.pieceRequestIndexOffset(2)
	got := requestsOf(a)
	tor.cl.lock()
	defer tor.cl.unlock()
	qt.Check(t, qt.Equals(int(a.requestState.Cancelled.GetCardinality()), 2),
		qt.Commentf("the two preempted requests await their rejects"))
	qt.Check(t, qt.HasLen(got, 3))
	for _, r := range []RequestIndex{p2, p2 + 1} {
		qt.Check(t, qt.Equals(tor.requestingPeer(r), &a.Peer))
	}
	a.requestState.Cancelled.Iterate(func(r RequestIndex) bool {
		qt.Check(t, qt.IsFalse(a.requestState.Requests.Contains(r)), qt.Commentf("%d re-requested before its reject", r))
		return true
	})
}

// A peer already working on the Now piece -- the usual case in sequential
// reading, where the next piece has requests in flight -- keeps its queue:
// preempting there only cancels readahead the reader needs next.
func TestNowPreemptSkipsPieceAlreadyInFlight(t *testing.T) {
	tor, a := preemptTestTorrent(t, true)
	tor.piece(2).SetPriority(PiecePriorityNormal)
	p1 := tor.pieceRequestIndexOffset(1)
	p2 := tor.pieceRequestIndexOffset(2)
	tor.cl.lock()
	for _, r := range []RequestIndex{p1, p1 + 1, p2} {
		_, err := a.request(r)
		qt.Assert(t, qt.IsNil(err))
	}
	tor.cl.unlock()
	queued := requestsOf(a)

	peak := a.peakRequests
	tor.piece(2).SetPriorityNow()
	updatePass(tor, a, peerUpdateRequestsNowPreemptReason)
	qt.Check(t, qt.DeepEquals(requestsOf(a), queued),
		qt.Commentf("piece 2 already has a request in flight here"))
	// The pass added nothing; the window must not collapse to that.
	qt.Check(t, qt.Equals(a.peakRequests, peak))
}

// A peer serves requests in order, so freeing slots is not enough: queued
// lower-priority requests beyond nowPreemptKeep are withdrawn and requested
// again behind the Now blocks, while the oldest -- already on the wire --
// stay.
func TestNowPreemptRequeuesLowerPriorityBehindNow(t *testing.T) {
	prevMin := nowPreemptMinKeep
	nowPreemptMinKeep = 1
	t.Cleanup(func() { nowPreemptMinKeep = prevMin })
	tor, a := preemptTestTorrent(t, true)
	tor.cl.lock()
	a.PeerMaxRequests = 6
	a.peakRequests = 3
	tor.cl.unlock()
	updatePass(tor, a, "fill")
	queued := requestsOf(a)
	qt.Assert(t, qt.HasLen(queued, 6), qt.Commentf("pieces 0 and 1 fill a window of 6"))
	tor.cl.lock()
	oldest := queued[0]
	for _, r := range queued {
		if tor.requestState[r].when.Before(tor.requestState[oldest].when) {
			oldest = r
		}
	}
	tor.cl.unlock()
	peak := a.peakRequests

	tor.piece(2).SetPriorityNow()
	updatePass(tor, a, peerUpdateRequestsNowPreemptReason)

	p2 := tor.pieceRequestIndexOffset(2)
	got := requestsOf(a)
	qt.Assert(t, qt.HasLen(got, 6), qt.Commentf("window refilled: %v", got))
	tor.cl.lock()
	defer tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.requestingPeer(oldest), &a.Peer), qt.Commentf("the oldest request stays"))
	for _, r := range []RequestIndex{p2, p2 + 1} {
		qt.Check(t, qt.Equals(tor.requestingPeer(r), &a.Peer), qt.Commentf("Now block %d not requested", r))
	}
	nowAt := tor.requestState[p2+1].when
	requeued := 0
	for _, r := range got {
		if r == oldest || r >= p2 {
			continue
		}
		requeued++
		qt.Check(t, qt.IsFalse(tor.requestState[r].when.Before(nowAt)),
			qt.Commentf("request %d went out ahead of the Now blocks", r))
	}
	qt.Check(t, qt.Equals(requeued, 3))
	qt.Check(t, qt.Equals(a.peakRequests, peak))
}

// The trim on the normal path (queue not full) with the fast extension: the
// withdrawn requests wait in Cancelled for their rejects, yet the pass's heap,
// built before the trim, still lists them. They must be skipped, not
// requested again (shouldRequest panics on a request awaiting its ack).
func TestNowPreemptTrimSkipsRequestsAwaitingRejects(t *testing.T) {
	prevMin := nowPreemptMinKeep
	nowPreemptMinKeep = 1
	t.Cleanup(func() { nowPreemptMinKeep = prevMin })
	tor, a := preemptTestTorrent(t, true)
	tor.cl.lock()
	a.PeerExtensionBytes.SetBit(pp.ExtensionBitFast, true)
	a.PeerMaxRequests = 6
	a.peakRequests = 3
	tor.cl.unlock()
	tor.piece(0).SetPriority(PiecePriorityNone)
	updatePass(tor, a, "fill")
	qt.Assert(t, qt.HasLen(requestsOf(a), 3), qt.Commentf("piece 1 alone, window of 6 not full"))

	tor.piece(2).SetPriorityNow()
	updatePass(tor, a, peerUpdateRequestsNowPreemptReason)
	p2 := tor.pieceRequestIndexOffset(2)
	tor.cl.lock()
	defer tor.cl.unlock()
	qt.Check(t, qt.Equals(int(a.requestState.Cancelled.GetCardinality()), 2))
	for _, r := range []RequestIndex{p2, p2 + 1} {
		qt.Check(t, qt.Equals(tor.requestingPeer(r), &a.Peer))
	}
	a.requestState.Cancelled.Iterate(func(r RequestIndex) bool {
		qt.Check(t, qt.IsFalse(a.requestState.Requests.Contains(r)), qt.Commentf("%d re-requested before its reject", r))
		return true
	})
}
