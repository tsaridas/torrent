package torrent

import (
	"net"
	"testing"
	"time"

	"github.com/anacrolix/generics"
	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"
	requestStrategy "github.com/anacrolix/torrent/request-strategy"
)

func newLargeSpreadTestTorrent(t *testing.T, numPieces int, pieceLen int64) *Torrent {
	t.Helper()
	cl := newTestingClient(t)
	totalLen := int64(numPieces) * pieceLen
	info := metainfo.Info{
		Name:        "spread.bin",
		PieceLength: pieceLen,
		Length:      totalLen,
		Pieces:      make([]byte, 20*numPieces),
	}
	infoBytes, err := bencode.Marshal(info)
	qt.Assert(t, qt.IsNil(err))
	mi := metainfo.MetaInfo{InfoBytes: infoBytes}
	tor, err := cl.AddTorrent(&mi)
	qt.Assert(t, qt.IsNil(err))
	<-tor.GotInfo()
	tor.cl.lock()
	for i := 0; i < numPieces; i++ {
		tor.pieces[i].hashing = false
		tor.piecesQueuedForHash.Remove(uint32(i))
	}
	tor.cl.unlock()
	return tor
}

func mkSpreadPeer(t *testing.T, tor *Torrent, port int, id byte) *PeerConn {
	t.Helper()
	addr := &net.TCPAddr{IP: net.IPv6loopback, Port: port}
	c := tor.cl.newConnection(nil, newConnectionOpts{
		remoteAddr: addr,
		network:    addr.Network(),
	})
	c.PeerID[0] = id
	c.initMessageWriter()
	c.peerChoking = false
	c.peerSentHaveAll = true
	c.setTorrent(tor)
	tor.cl.lock()
	err := tor.addPeerConn(c)
	tor.cl.unlock()
	qt.Assert(t, qt.IsNil(err))
	return c
}

func TestPiece0SpreadOrdersNowByPieceIndex(t *testing.T) {
	tor := newLargeSpreadTestTorrent(t, 4, 256<<10)
	tor.cl.lock()
	tor.pieceRequestOrder = []int{3, 0, 2, 1} // piece 1 beats piece 0 under random order
	tor.cl.unlock()

	peer := mkSpreadPeer(t, tor, 5101, 1)
	d := desiredPeerRequests{
		peer:        &peer.Peer,
		pieceStates: make([]generics.Option[requestStrategy.PieceRequestOrderState], 4),
	}
	for i := 0; i < 2; i++ {
		d.pieceStates[i].Set(requestStrategy.PieceRequestOrderState{
			Priority: PiecePriorityNow,
		})
	}
	r0 := tor.pieceRequestIndexOffset(0)
	r1 := tor.pieceRequestIndexOffset(1)

	tor.cl.lock()
	tor.cl.config.Piece0Spread = false
	off := d.lessByValue(r0, r1)
	tor.cl.config.Piece0Spread = true
	on := d.lessByValue(r0, r1)
	tor.cl.unlock()

	qt.Check(t, qt.IsFalse(off)) // off follows pieceRequestOrder (1 before 0)
	qt.Check(t, qt.IsTrue(on))   // on orders Now piece 0 before piece 1
}

func TestPiece0SpreadShadowTargetsStalledHolderFirst(t *testing.T) {
	tor := newLargeSpreadTestTorrent(t, 2, 512<<10) // 32 chunks of 16 KiB per piece
	tor.cl.lock()
	tor.cl.config.Piece0Spread = true
	tor.cl.unlock()

	fastHolder := mkSpreadPeer(t, tor, 5111, 1)
	stalledHolder := mkSpreadPeer(t, tor, 5112, 2)
	racer := mkSpreadPeer(t, tor, 5113, 3)

	tor.cl.lock()
	fastHolder.lastUsefulChunkReceived = time.Now()
	fastHolder.cumulativeExpectedToReceiveChunks = 100 * time.Millisecond
	fastHolder._stats.BytesReadUsefulIntendedData.Add(1 << 20)
	racer.lastUsefulChunkReceived = time.Now()
	racer.cumulativeExpectedToReceiveChunks = 100 * time.Millisecond
	racer._stats.BytesReadUsefulIntendedData.Add(2 << 20)

	if tor.requestState == nil {
		tor.requestState = make(map[RequestIndex]requestState)
	}
	for ci := 0; ci < 20; ci++ {
		ri := tor.pieceRequestIndexOffset(0) + RequestIndex(ci)
		fastHolder.requestState.Requests.Add(ri)
		tor.requestState[ri] = requestState{peer: &fastHolder.Peer, when: time.Now()}
	}
	stalledRI := tor.pieceRequestIndexOffset(0) + RequestIndex(25)
	stalledHolder.requestState.Requests.Add(stalledRI)
	tor.requestState[stalledRI] = requestState{peer: &stalledHolder.Peer, when: time.Now().Add(-500 * time.Millisecond)}
	for ci := 20; ci < 32; ci++ {
		if ci == 25 {
			continue
		}
		tor.dirtyChunks.Add(tor.pieceRequestIndexOffset(0) + RequestIndex(ci))
	}
	racer.PeerMaxRequests = 64
	fastHolder.PeerMaxRequests = 64
	stalledHolder.PeerMaxRequests = 64
	tor.cl.unlock()

	sent := tor.ShadowRequestAhead(0, 8, 1)
	qt.Check(t, qt.Not(qt.Equals(sent, 0)))

	tor.cl.rLock()
	_, gotStalled := racer.shadowRequests[stalledRI]
	tor.cl.rUnlock()
	qt.Check(t, qt.IsTrue(gotStalled), qt.Commentf("expected stalled chunk 25 to be shadowed first"))

	// With Piece0Spread off, linear walk shadows only the first 8 chunks (0..7)
	// and never reaches chunk 25.
	tor2 := newLargeSpreadTestTorrent(t, 2, 512<<10)
	fh2 := mkSpreadPeer(t, tor2, 5121, 1)
	sh2 := mkSpreadPeer(t, tor2, 5122, 2)
	r2 := mkSpreadPeer(t, tor2, 5123, 3)
	tor2.cl.lock()
	tor2.requestState = make(map[RequestIndex]requestState)
	for ci := 0; ci < 20; ci++ {
		ri := tor2.pieceRequestIndexOffset(0) + RequestIndex(ci)
		fh2.requestState.Requests.Add(ri)
		tor2.requestState[ri] = requestState{peer: &fh2.Peer, when: time.Now()}
	}
	stalled2 := tor2.pieceRequestIndexOffset(0) + RequestIndex(25)
	sh2.requestState.Requests.Add(stalled2)
	tor2.requestState[stalled2] = requestState{peer: &sh2.Peer, when: time.Now()}
	for ci := 20; ci < 32; ci++ {
		if ci == 25 {
			continue
		}
		tor2.dirtyChunks.Add(tor2.pieceRequestIndexOffset(0) + RequestIndex(ci))
	}
	r2.PeerMaxRequests = 64
	tor2.cl.unlock()
	sent2 := tor2.ShadowRequestAhead(0, 8, 1)
	qt.Check(t, qt.Equals(sent2, 8))
	tor2.cl.rLock()
	_, got2 := r2.shadowRequests[stalled2]
	tor2.cl.rUnlock()
	qt.Check(t, qt.IsFalse(got2), qt.Commentf("default linear walk must stay in chunks 0..7"))
}
