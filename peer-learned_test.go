package torrent

import (
	"net"
	"os"
	"sync"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
)

func TestPeerLearnedCallbackDeduplicatesAndKeepsFirstSource(t *testing.T) {
	cfg := TestingConfig(t)
	var mu sync.Mutex
	var events []PeerLearnedEvent
	cfg.Callbacks.PeerLearned = append(cfg.Callbacks.PeerLearned, func(ev PeerLearnedEvent) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)
	tt, _, err := cl.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	qt.Assert(t, qt.IsNil(err))
	tt.SetMaxEstablishedConns(0)

	addr := &net.TCPAddr{IP: net.IPv4(192, 0, 2, 10), Port: 6881}
	tt.AddPeers([]PeerInfo{{Addr: addr, Source: PeerSourceDhtGetPeers}})
	// Replacing the reserve entry with a higher-priority source must not re-fire
	// PeerLearned or overwrite firstLearned.
	tt.AddPeers([]PeerInfo{{Addr: addr, Source: PeerSourceTracker}})

	mu.Lock()
	defer mu.Unlock()
	qt.Assert(t, qt.HasLen(events, 1))
	qt.Check(t, qt.Equals(events[0].Torrent, tt))
	qt.Check(t, qt.Equals(events[0].PeerInfo.Source, PeerSourceDhtGetPeers))
	qt.Check(t, qt.IsFalse(events[0].At.IsZero()))

	cl.rLock()
	rec, ok := tt.firstLearned[addr.String()]
	cl.rUnlock()
	qt.Assert(t, qt.IsTrue(ok))
	qt.Check(t, qt.Equals(rec.source, PeerSourceDhtGetPeers))
	qt.Check(t, qt.IsTrue(rec.at.Equal(events[0].At)))
}

func TestPeerTimingAccessorsOutgoingAndIncoming(t *testing.T) {
	type peerSnap struct {
		outgoing    bool
		connectedAt time.Time
		learnedAt   time.Time
		learnedSrc  PeerSource
		discovery   PeerSource
	}

	dir, mi := testutil.GreetingTestTorrent()
	defer os.RemoveAll(dir)

	seedCfg := TestingConfig(t)
	seedCfg.Seed = true
	seedCfg.DataDir = dir
	seedCh := make(chan peerSnap, 1)
	seedCfg.Callbacks.PeerConnAdded = append(seedCfg.Callbacks.PeerConnAdded, func(pc *PeerConn) {
		select {
		case seedCh <- peerSnap{
			outgoing:    pc.Outgoing(),
			connectedAt: pc.ConnectedAt(),
			learnedAt:   pc.FirstLearnedAt(),
			learnedSrc:  pc.FirstLearnedSource(),
			discovery:   pc.Discovery,
		}:
		default:
		}
	})
	seeder, err := NewClient(seedCfg)
	qt.Assert(t, qt.IsNil(err))
	defer seeder.Close()

	st, _, err := seeder.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	qt.Assert(t, qt.IsNil(err))
	st.VerifyData()

	leechCfg := TestingConfig(t)
	var learnedMu sync.Mutex
	var learned []PeerLearnedEvent
	leechCfg.Callbacks.PeerLearned = append(leechCfg.Callbacks.PeerLearned, func(ev PeerLearnedEvent) {
		learnedMu.Lock()
		learned = append(learned, ev)
		learnedMu.Unlock()
	})
	leechCh := make(chan peerSnap, 1)
	leechCfg.Callbacks.PeerConnAdded = append(leechCfg.Callbacks.PeerConnAdded, func(pc *PeerConn) {
		select {
		case leechCh <- peerSnap{
			outgoing:    pc.Outgoing(),
			connectedAt: pc.ConnectedAt(),
			learnedAt:   pc.FirstLearnedAt(),
			learnedSrc:  pc.FirstLearnedSource(),
			discovery:   pc.Discovery,
		}:
		default:
		}
	})
	leecher, err := NewClient(leechCfg)
	qt.Assert(t, qt.IsNil(err))
	defer leecher.Close()

	lt, _, err := leecher.AddTorrentSpec(TorrentSpecFromMetaInfo(mi))
	qt.Assert(t, qt.IsNil(err))
	lt.DownloadAll()
	qt.Assert(t, qt.Not(qt.HasLen(seeder.ListenAddrs(), 0)))
	lt.AddPeers([]PeerInfo{{
		Addr:    seeder.ListenAddrs()[0],
		Source:  PeerSourceDhtGetPeers,
		Trusted: true,
	}})

	var leechSnap, seedSnap peerSnap
	select {
	case leechSnap = <-leechCh:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for outgoing PeerConnAdded")
	}
	select {
	case seedSnap = <-seedCh:
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for incoming PeerConnAdded")
	}

	learnedMu.Lock()
	qt.Assert(t, qt.HasLen(learned, 1))
	learnedAt := learned[0].At
	learnedMu.Unlock()

	qt.Check(t, qt.IsTrue(leechSnap.outgoing))
	qt.Check(t, qt.IsFalse(leechSnap.connectedAt.IsZero()))
	qt.Check(t, qt.IsTrue(leechSnap.learnedAt.Equal(learnedAt)))
	qt.Check(t, qt.Equals(leechSnap.learnedSrc, PeerSourceDhtGetPeers))
	qt.Check(t, qt.Equals(leechSnap.discovery, PeerSourceDhtGetPeers))
	qt.Check(t, qt.IsFalse(leechSnap.connectedAt.Before(leechSnap.learnedAt)))

	qt.Check(t, qt.IsFalse(seedSnap.outgoing))
	qt.Check(t, qt.IsFalse(seedSnap.connectedAt.IsZero()))
	qt.Check(t, qt.IsTrue(seedSnap.learnedAt.IsZero()))
	qt.Check(t, qt.Equals(seedSnap.learnedSrc, PeerSource("")))
	qt.Check(t, qt.Equals(seedSnap.discovery, PeerSourceIncoming))
}
