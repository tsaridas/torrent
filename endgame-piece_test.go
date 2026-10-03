package torrent

import (
	"expvar"
	"net"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestEndgamePieceWithoutPeersOrNotNow(t *testing.T) {
	tor := greetingTorrent(t)
	// Piece 0 exists but is not Now and there are no peers.
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 16, 4), 0))
	qt.Check(t, qt.Equals(tor.EndgamePiece(-1, 0, 16, 4), 0))
	qt.Check(t, qt.Equals(tor.EndgamePiece(99, 0, 16, 4), 0))
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 0, 4), 0))
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 16, 0), 0))

	before := endgameNotNowSkips()
	// Still not Now: skip must be counted.
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 16, 4), 0))
	qt.Check(t, qt.Equals(endgameNotNowSkips(), before+1))

	tor.piece(0).SetPriorityNow()
	// Now, but still no peers to ask.
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 16, 4), 0))
}

func TestEndgamePieceAllReceivedIsNoop(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	tor.cl.lock()
	p := tor.piece(0)
	// dirtyChunks = received; with every chunk dirty there is nothing to ask for.
	for ci := chunkIndexType(0); ci < p.numChunks(); ci++ {
		p.unpendChunkIndex(ci)
	}
	tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 16, 4), 0))
}

// TestEndgamePieceSendsCappedShadowRequests exercises the main loop: two
// capable peers, a Now piece with missing chunks, caps of 2 chunks × 2 peers.
func TestEndgamePieceSendsCappedShadowRequests(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	a, b := endgameTestPeers(t, tor)

	// Piece 0 is 5 bytes / 2-byte chunks → 3 missing chunks. Cap at 2×2 so
	// both peers are asked on the first call.
	sent := tor.EndgamePiece(0, 0, 2, 2)
	qt.Check(t, qt.Equals(sent, 4))

	totalShadow := 0
	for _, pc := range []*PeerConn{a, b} {
		totalShadow += len(pc.shadowRequests)
		qt.Check(t, qt.Equals(len(pc.shadowRequests), 2))
	}
	qt.Check(t, qt.Equals(totalShadow, 4))

	// A second call must not re-ask peers already expecting those chunks.
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 2, 2), 0))
}

// TestEndgamePieceStartsAtOffset skips chunks before off — the multi-file
// case where the focused file begins mid-piece.
func TestEndgamePieceStartsAtOffset(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	a, b := endgameTestPeers(t, tor)

	// Piece 0: chunks at torrent offsets 0, 2, 4. Start at offset 2 → chunk 1.
	sent := tor.EndgamePiece(0, 2, 16, 2)
	// Chunks 1 and 2 × 2 peers = 4. Chunk 0 must not be asked.
	qt.Check(t, qt.Equals(sent, 4))

	base := tor.pieceRequestIndexOffset(0)
	for _, pc := range []*PeerConn{a, b} {
		if _, ok := pc.shadowRequests[base+0]; ok {
			t.Fatal("chunk 0 before off must not be duplicated")
		}
		if _, ok := pc.shadowRequests[base+1]; !ok {
			t.Fatal("chunk 1 at off must be duplicated")
		}
		if _, ok := pc.shadowRequests[base+2]; !ok {
			t.Fatal("chunk 2 after off must be duplicated")
		}
	}
}

func endgameTestPeers(t *testing.T, tor *Torrent) (a, b *PeerConn) {
	t.Helper()
	mk := func(port int, id byte) *PeerConn {
		addr := &net.TCPAddr{IP: net.IPv6loopback, Port: port}
		c := tor.cl.newConnection(nil, newConnectionOpts{
			remoteAddr: addr,
			network:    addr.Network(),
		})
		c.PeerID[0] = id
		c.initMessageWriter()
		c.peerChoking = false
		c._peerPieces.Add(0)
		c.setTorrent(tor)
		tor.cl.lock()
		err := tor.addPeerConn(c)
		tor.cl.unlock()
		qt.Assert(t, qt.IsNil(err))
		return c
	}
	return mk(4747, 1), mk(4748, 2)
}

func endgameNotNowSkips() int64 {
	m, ok := expvar.Get("torrent").(*expvar.Map)
	if !ok {
		return 0
	}
	v, ok := m.Get(endgamePieceNotNowSkipsVar).(*expvar.Int)
	if !ok {
		return 0
	}
	return v.Value()
}

func TestCancelShadowCopiesKeepsExpectation(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	a, b := endgameTestPeers(t, tor)
	tor.cl.lock()
	ri := tor.pieceRequestIndexOffset(0)
	qt.Assert(t, qt.IsTrue(a.shadowRequestOne(ri)))
	tor.cancelShadowCopies(ri, &b.Peer)
	e, still := a.shadowRequests[ri]
	qt.Check(t, qt.IsTrue(still))
	qt.Check(t, qt.IsFalse(e.cancelled.IsZero()), qt.Commentf("must record cancel time"))
	qt.Check(t, qt.Equals(a.validReceiveChunks[ri], 1))
	tor.cl.unlock()
}

func TestShadowInFlightIgnoresReceivedAndExpiresCancel(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	a, b := endgameTestPeers(t, tor)
	tor.cl.lock()
	defer tor.cl.unlock()
	ri := tor.pieceRequestIndexOffset(0)
	qt.Assert(t, qt.IsTrue(a.shadowRequestOne(ri)))
	qt.Check(t, qt.Equals(tor.shadowInFlightLocked(), 1))

	// Chunk received (dirty): live shadow must not count toward the budget.
	tor.dirtyChunks.Add(ri)
	qt.Check(t, qt.Equals(tor.shadowInFlightLocked(), 0))
	tor.dirtyChunks.Remove(ri)
	qt.Check(t, qt.Equals(tor.shadowInFlightLocked(), 1))

	// Cancel keeps the entry for disconnect safety but frees the budget.
	tor.cancelShadowCopies(ri, &b.Peer)
	qt.Check(t, qt.Equals(tor.shadowInFlightLocked(), 0))
	_, still := a.shadowRequests[ri]
	qt.Check(t, qt.IsTrue(still), qt.Commentf("kept until expire/arrival"))

	// Expire the cancel: entry dropped entirely.
	e := a.shadowRequests[ri]
	e.cancelled = e.cancelled.Add(-shadowCancelExpire - time.Second)
	a.shadowRequests[ri] = e
	qt.Check(t, qt.Equals(tor.shadowInFlightLocked(), 0))
	_, still = a.shadowRequests[ri]
	qt.Check(t, qt.IsFalse(still))
	qt.Check(t, qt.Equals(a.validReceiveChunks[ri], 0))
	qt.Check(t, qt.Equals(tor.numShadowRequests, 0))
}

func TestCancelShadowCopiesNoopWithoutShadows(t *testing.T) {
	tor := greetingTorrent(t)
	a, b := endgameTestPeers(t, tor)
	tor.cl.lock()
	defer tor.cl.unlock()
	qt.Assert(t, qt.Equals(tor.numShadowRequests, 0))
	// Must not touch peers when nothing is outstanding.
	ri := tor.pieceRequestIndexOffset(0)
	tor.cancelShadowCopies(ri, &b.Peer)
	qt.Check(t, qt.Equals(len(a.shadowRequests), 0))
}

// TestRequestPromotesOnlyLiveShadow: a cancelled shadow must not be promoted
// into requestState without a new wire Request — the peer was told to cancel.
func TestRequestPromotesOnlyLiveShadow(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	a, b := endgameTestPeers(t, tor)
	tor.cl.lock()
	defer tor.cl.unlock()
	// shouldRequest panics while a piece is hashing / queued; greeting
	// torrents may still be finishing the initial check.
	tor.initialPieceCheckDisabled = true
	tor.piecesQueuedForHash.Clear()
	tor.piece(0).hashing = false
	ri := tor.pieceRequestIndexOffset(0)

	// Live shadow → promote, no new Request on the wire.
	qt.Assert(t, qt.IsTrue(a.shadowRequestOne(ri)))
	before := a.messageWriter.writeBuffer.Len()
	more, err := a.request(ri)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(more))
	qt.Check(t, qt.IsTrue(a.requestState.Requests.Contains(ri)))
	qt.Check(t, qt.Equals(a.messageWriter.writeBuffer.Len(), before),
		qt.Commentf("live promote must not send another Request"))
	qt.Check(t, qt.Equals(len(a.shadowRequests), 0))

	// Clean slate for the cancelled case.
	a.deleteRequest(ri)
	delete(a.t.requestState, ri)
	a.dropAllShadowRequests()
	a.validReceiveChunks = nil

	qt.Assert(t, qt.IsTrue(a.shadowRequestOne(ri)))
	tor.cancelShadowCopies(ri, &b.Peer)
	qt.Assert(t, qt.IsFalse(a.shadowRequests[ri].live()))
	before = a.messageWriter.writeBuffer.Len()
	more, err = a.request(ri)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.IsTrue(more))
	qt.Check(t, qt.IsTrue(a.requestState.Requests.Contains(ri)))
	qt.Check(t, qt.Equals(len(a.shadowRequests), 0))
	qt.Check(t, qt.Not(qt.Equals(a.messageWriter.writeBuffer.Len(), before)),
		qt.Commentf("cancelled shadow must send a fresh Request"))
}

func TestDropAllShadowRequestsOnNonFastChoke(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	a, _ := endgameTestPeers(t, tor)
	tor.cl.lock()
	ri := tor.pieceRequestIndexOffset(0)
	qt.Assert(t, qt.IsTrue(a.shadowRequestOne(ri)))
	qt.Assert(t, qt.Equals(len(a.shadowRequests), 1))
	a.dropAllShadowRequests()
	tor.cl.unlock()
	qt.Check(t, qt.Equals(len(a.shadowRequests), 0))
	qt.Check(t, qt.Equals(a.validReceiveChunks[ri], 0))
}

func TestEndgamePeerBudgetWholePieceAndRoom(t *testing.T) {
	tor := greetingTorrent(t)
	tor.cl.config.EndgamePeerBudget = true
	tor.piece(0).SetPriorityNow()
	a, b := endgameTestPeers(t, tor)
	a.PeerMaxRequests = 250
	b.PeerMaxRequests = 250

	// maxChunks=0 → whole piece (3 chunks) × 2 peers = 6.
	sent := tor.EndgamePiece(0, 0, 0, 0)
	qt.Check(t, qt.Equals(sent, 6))
	qt.Check(t, qt.Equals(len(a.shadowRequests), 3))
	qt.Check(t, qt.Equals(len(b.shadowRequests), 3))
	qt.Check(t, qt.Equals(a.liveShadowCount, 3))
	qt.Check(t, qt.Equals(b.liveShadowCount, 3))

	// No room left on a: PeerMaxRequests == outstanding shadows.
	a.PeerMaxRequests = 3
	// Clear b so only a would be asked; a is full.
	b.dropAllShadowRequests()
	b.validReceiveChunks = nil
	// Allow another pass (one endgame per endgameTorrentInterval otherwise).
	tor.cl.lock()
	tor.lastEndgameAt = time.Time{}
	tor.cl.unlock()
	// a already has 3 live shadows and max 3 → room 0; b can take the piece again.
	sent = tor.EndgamePiece(0, 0, 0, 0)
	qt.Check(t, qt.Equals(sent, 3))
	qt.Check(t, qt.Equals(len(b.shadowRequests), 3))
}

func TestEndgamePieceOnePassPerInterval(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	endgameTestPeers(t, tor)
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 2, 2), 4))
	// Immediate second call is throttled even though peers could take more.
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 2, 2), 0))
	tor.cl.lock()
	tor.lastEndgameAt = time.Now().Add(-endgameTorrentInterval)
	tor.cl.unlock()
	// After the interval, a call that finds nothing new still runs the loop.
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 2, 2), 0))
}

func TestExpireCancelledShadowsFromCancelCopies(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	a, b := endgameTestPeers(t, tor)
	tor.cl.lock()
	defer tor.cl.unlock()
	ri := tor.pieceRequestIndexOffset(0)
	qt.Assert(t, qt.IsTrue(a.shadowRequestOne(ri)))
	tor.cancelShadowCopies(ri, &b.Peer)
	qt.Check(t, qt.Equals(a.liveShadowCount, 0))
	e := a.shadowRequests[ri]
	e.cancelled = e.cancelled.Add(-shadowCancelExpire - time.Second)
	a.shadowRequests[ri] = e
	// Budget mode never calls shadowInFlightLocked; cancelShadowCopies must expire.
	tor.cancelShadowCopies(ri+1, &b.Peer)
	_, still := a.shadowRequests[ri]
	qt.Check(t, qt.IsFalse(still))
	qt.Check(t, qt.Equals(tor.numShadowRequests, 0))
}

func TestEndgameSilentShadowFreesPerChunkSlot(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	a, b := endgameTestPeers(t, tor)
	tor.cl.lock()
	ri := tor.pieceRequestIndexOffset(0)
	qt.Assert(t, qt.IsTrue(a.shadowRequestOne(ri)))
	e := a.shadowRequests[ri]
	e.sent = time.Now().Add(-shadowSilentAge - time.Millisecond)
	a.shadowRequests[ri] = e
	tor.cl.unlock()

	// Cap 1 peer per chunk: silent a must not consume the slot; b gets asked.
	sent := tor.EndgamePiece(0, 0, 1, 1)
	qt.Check(t, qt.Equals(sent, 1))
	_, onB := b.shadowRequests[ri]
	qt.Check(t, qt.IsTrue(onB))
}
