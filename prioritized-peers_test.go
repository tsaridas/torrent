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
		{Addr: ipPortAddr{IP: net.ParseIP("")}},
		{Addr: ipPortAddr{IP: net.ParseIP("")}, Trusted: true},
	}
	for i, p := range ps {
		t.Logf("peer %d priority: %08x trusted: %t\n", i, pp.getPrio(p), p.Trusted)
		assert.False(t, pp.Add(p))
		assert.True(t, pp.Add(p))
		assert.Equal(t, i+1, pp.Len())
	}
	pop := func(expected *PeerInfo) {
		if expected == nil {
			assert.Panics(t, func() { pp.PopMax() })
		} else {
			assert.Equal(t, *expected, pp.PopMax())
		}
	}
	min := func(expected *PeerInfo) {
		i, ok := pp.DeleteMin()
		if expected == nil {
			assert.False(t, ok)
		} else {
			assert.True(t, ok)
			assert.Equal(t, *expected, i.p)
		}
	}
	pop(&ps[3])
	pop(&ps[1])
	min(&ps[2])
	pop(&ps[0])
	min(nil)
	pop(nil)
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
