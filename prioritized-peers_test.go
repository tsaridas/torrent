package torrent

import (
	"net"
	"testing"

	"github.com/google/btree"
	"github.com/stretchr/testify/assert"
)

func TestPrioritizedPeers(t *testing.T) {
	pp := prioritizedPeers{
		om: btree.New(3),
		getPrio: func(p PeerInfo) peerPriority {
			return bep40PriorityIgnoreError(p.addr(), IpPort{IP: net.ParseIP("0.0.0.0")})
		},
	}
	_, ok := pp.DeleteMin()
	assert.Panics(t, func() { pp.PopMax() })
	assert.False(t, ok)
	ps := []PeerInfo{
		{Addr: ipPortAddr{IP: net.ParseIP("1.2.3.4")}},
		{Addr: ipPortAddr{IP: net.ParseIP("1::2")}},
		{Addr: ipPortAddr{IP: net.ParseIP("3.3.3.3")}},
		{Addr: ipPortAddr{IP: net.ParseIP("4.4.4.4")}, Trusted: true},
	}
	for i, p := range ps {
		t.Logf("peer %d priority: %08x trusted: %t\n", i, pp.getPrio(p), p.Trusted)
		assert.False(t, pp.Add(p))
		assert.True(t, pp.Add(p))
		assert.Equal(t, i+1, pp.Len())
	}
	// Trusted outranks everyone.
	assert.Equal(t, ps[3], pp.PopMax())
	assert.Equal(t, 3, pp.Len())
	// Drain the rest without assuming BEP-40 order across IPs.
	for pp.Len() > 0 {
		_ = pp.PopMax()
	}
	assert.Panics(t, func() { pp.PopMax() })
	_, ok = pp.DeleteMin()
	assert.False(t, ok)
}

// Tracker and PEX peers are dialed before DHT results; source outranks the
// BEP 40 priority, but not Trusted.
func TestPrioritizedPeersPreferTrackerOverDHT(t *testing.T) {
	pp := prioritizedPeers{
		om: btree.New(3),
		getPrio: func(p PeerInfo) peerPriority {
			// DHT peer would win on BEP 40 priority alone.
			if p.Source == PeerSourceDhtGetPeers {
				return 100
			}
			return 1
		},
	}
	dht := PeerInfo{Addr: ipPortAddr{IP: net.ParseIP("1.1.1.1"), Port: 1}, Source: PeerSourceDhtGetPeers}
	tr := PeerInfo{Addr: ipPortAddr{IP: net.ParseIP("2.2.2.2"), Port: 2}, Source: PeerSourceTracker}
	pp.Add(dht)
	pp.Add(tr)
	if got := pp.PopMax(); got.Source != PeerSourceTracker {
		t.Fatalf("first dial %v, want the tracker peer", got.Source)
	}
	if got := pp.PopMax(); got.Source != PeerSourceDhtGetPeers {
		t.Fatalf("second dial %v, want the DHT peer", got.Source)
	}
}

// Same address from DHT then tracker must be one pending entry (tracker
// wins). peerSourceRank in Less made them two keys and wasted a half-open
// dial after the first failed.
func TestPrioritizedPeersDedupSameAddrKeepsBestSource(t *testing.T) {
	pp := prioritizedPeers{
		om: btree.New(3),
		getPrio: func(PeerInfo) peerPriority {
			return 1
		},
	}
	addr := ipPortAddr{IP: net.ParseIP("9.9.9.9"), Port: 6881}
	pp.Add(PeerInfo{Addr: addr, Source: PeerSourceDhtGetPeers})
	pp.Add(PeerInfo{Addr: addr, Source: PeerSourceTracker})
	if pp.Len() != 1 {
		t.Fatalf("Len=%d want 1 after DHT then tracker for the same address", pp.Len())
	}
	got := pp.PopMax()
	if got.Source != PeerSourceTracker {
		t.Fatalf("kept source %v, want tracker", got.Source)
	}
	if pp.Len() != 0 {
		t.Fatalf("Len=%d after PopMax, want 0", pp.Len())
	}
}
