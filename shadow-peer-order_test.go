package torrent

import (
	"net"
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func mkShadowPeer(t *testing.T, tor *Torrent, port int, id byte) *PeerConn {
	t.Helper()
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

func TestSortShadowPeersColdStartPrefersDeliveredAndSeeder(t *testing.T) {
	tor := greetingTorrent(t)
	tor.cl.config.ShadowPeerOrder = true
	silent := mkShadowPeer(t, tor, 5001, 1)
	delivered := mkShadowPeer(t, tor, 5002, 2)
	seeder := mkShadowPeer(t, tor, 5003, 3)
	delivered.lastUsefulChunkReceived = time.Now()
	seeder.peerSentHaveAll = true

	peers := []shadowPeerCand{
		{silent, 0},
		{seeder, 0},
		{delivered, 0},
	}
	tor.cl.lock()
	tor.sortShadowPeers(peers)
	tor.cl.unlock()
	qt.Check(t, qt.Equals(peers[0].pc, delivered))
	qt.Check(t, qt.Equals(peers[1].pc, seeder))
	qt.Check(t, qt.Equals(peers[2].pc, silent))
}

func TestSortShadowPeersRotatesEqualTier(t *testing.T) {
	tor := greetingTorrent(t)
	tor.cl.config.ShadowPeerOrder = true
	a := mkShadowPeer(t, tor, 5011, 1)
	b := mkShadowPeer(t, tor, 5012, 2)
	peers := []shadowPeerCand{{a, 0}, {b, 0}}
	tor.cl.lock()
	tor.shadowPeerRotate = 0
	tor.sortShadowPeers(peers)
	first0 := peers[0].pc
	peers = []shadowPeerCand{{a, 0}, {b, 0}}
	tor.sortShadowPeers(peers)
	first1 := peers[0].pc
	tor.cl.unlock()
	qt.Check(t, qt.Not(qt.Equals(first0, first1)), qt.Commentf("equal tier should rotate"))
}
