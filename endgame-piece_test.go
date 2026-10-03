package torrent

import (
	"expvar"
	"net"
	"testing"

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
