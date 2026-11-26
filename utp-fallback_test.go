package torrent

import (
	"fmt"
	"net"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
)

// TestUTPFallbackDialerPreservation tests that UTPFallback=false leaves fallbackDialers empty
// and keeps exact dialers/listeners parity, while UTPFallback=true populates fallbackDialers
// with the UDP socket without adding it to dialers or listeners or starting an accept loop.
func TestUTPFallbackDialerPreservation(t *testing.T) {
	// Case 1: UTPFallback = false (default)
	{
		cfg := TestingConfig(t)
		cfg.DisableUTP = true
		cfg.UTPFallback = false
		cl, err := NewClient(cfg)
		qt.Assert(t, qt.IsNil(err))
		defer cl.Close()

		qt.Check(t, qt.Equals(len(cl.fallbackDialers), 0))
		for _, d := range cl.dialers {
			qt.Check(t, qt.IsFalse(parseNetworkString(d.DialerNetwork()).Udp))
		}
		for _, l := range cl.listeners {
			qt.Check(t, qt.IsFalse(parseNetworkString(l.Addr().Network()).Udp))
		}
	}

	// Case 2: UTPFallback = true
	{
		cfg := TestingConfig(t)
		cfg.DisableUTP = true
		cfg.UTPFallback = true
		cl, err := NewClient(cfg)
		qt.Assert(t, qt.IsNil(err))
		defer cl.Close()

		qt.Check(t, qt.IsTrue(len(cl.fallbackDialers) > 0))
		for _, fd := range cl.fallbackDialers {
			qt.Check(t, qt.IsTrue(parseNetworkString(fd.DialerNetwork()).Udp))
		}
		// dialers and listeners must remain TCP-only
		for _, d := range cl.dialers {
			qt.Check(t, qt.IsFalse(parseNetworkString(d.DialerNetwork()).Udp))
		}
		for _, l := range cl.listeners {
			qt.Check(t, qt.IsFalse(parseNetworkString(l.Addr().Network()).Udp))
		}
	}
}

// TestUTPFallbackOffNeverDialsUTP tests that when a torrent does not have uTP fallback enabled,
// it never dials over uTP even if UTPFallback=true is set on the Client.
func TestUTPFallbackOffNeverDialsUTP(t *testing.T) {
	seeder := utpDialDelaySeeder(t, func(cfg *ClientConfig) {
		cfg.DisableTCP = true // Seeder accepts uTP only
		cfg.DisableUTP = false
	})

	cfg := TestingConfig(t)
	cfg.DisableUTP = true
	cfg.UTPFallback = true
	handshook := make(chan string, 1)
	cfg.Callbacks.CompletedHandshake = func(pc *PeerConn, _ InfoHash) {
		select {
		case handshook <- pc.Network:
		default:
		}
	}

	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	tt, err := cl.AddTorrent(testutil.GreetingMetaInfo())
	qt.Assert(t, qt.IsNil(err))
	tt.DownloadAll()
	qt.Assert(t, qt.IsFalse(tt.UTPFallback()))

	tt.AddPeers([]PeerInfo{{Addr: ipPortAddr{net.IPv4(127, 0, 0, 1), seeder.LocalPort()}}})

	select {
	case netw := <-handshook:
		t.Fatalf("unexpected connection on %s when fallback is off", netw)
	case <-time.After(1 * time.Second):
		// Expected: no uTP connection made
	}
}

// TestUTPFallbackOnConnectsToUTPOnlyPeer tests that a torrent with SetUTPFallback(true)
// successfully connects to a uTP-only peer.
func TestUTPFallbackOnConnectsToUTPOnlyPeer(t *testing.T) {
	seeder := utpDialDelaySeeder(t, func(cfg *ClientConfig) {
		cfg.DisableTCP = true
		cfg.DisableUTP = false
	})

	cfg := TestingConfig(t)
	cfg.DisableUTP = true
	cfg.UTPFallback = true
	handshook := make(chan string, 1)
	cfg.Callbacks.CompletedHandshake = func(pc *PeerConn, _ InfoHash) {
		select {
		case handshook <- pc.Network:
		default:
		}
	}

	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	tt, err := cl.AddTorrent(testutil.GreetingMetaInfo())
	qt.Assert(t, qt.IsNil(err))
	tt.DownloadAll()
	tt.SetUTPFallback(true)
	qt.Assert(t, qt.IsTrue(tt.UTPFallback()))

	tt.AddPeers([]PeerInfo{{Addr: ipPortAddr{net.IPv4(127, 0, 0, 1), seeder.LocalPort()}}})

	select {
	case netw := <-handshook:
		qt.Check(t, qt.IsTrue(parseNetworkString(netw).Udp))
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for uTP connection with fallback on")
	}
}

// TestUTPFallbackOnDualStackKeepsTCP tests that when connecting to a dual-stack peer,
// UTPDialDelay keeps the connection on TCP.
func TestUTPFallbackOnDualStackKeepsTCP(t *testing.T) {
	seeder := utpDialDelaySeeder(t, func(cfg *ClientConfig) {
		cfg.DisableTCP = false
		cfg.DisableUTP = false
	})

	for range 10 {
		cfg := TestingConfig(t)
		cfg.DisableUTP = true
		cfg.UTPFallback = true
		cfg.UTPDialDelay = time.Hour
		handshook := make(chan string, 1)
		cfg.Callbacks.CompletedHandshake = func(pc *PeerConn, _ InfoHash) {
			select {
			case handshook <- pc.Network:
			default:
			}
		}

		cl, err := NewClient(cfg)
		qt.Assert(t, qt.IsNil(err))

		tt, err := cl.AddTorrent(testutil.GreetingMetaInfo())
		qt.Assert(t, qt.IsNil(err))
		tt.DownloadAll()
		tt.SetUTPFallback(true)

		tt.AddPeers([]PeerInfo{{Addr: ipPortAddr{net.IPv4(127, 0, 0, 1), seeder.LocalPort()}}})

		select {
		case netw := <-handshook:
			qt.Check(t, qt.IsTrue(parseNetworkString(netw).Tcp))
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for dual-stack connection")
		}
		cl.Close()
	}
}

// TestUTPFallbackRedialsFailedCandidates tests that candidate peer addresses that failed initial
// TCP dials are re-queued and dialed over uTP when SetUTPFallback(true) is invoked.
func TestUTPFallbackRedialsFailedCandidates(t *testing.T) {
	seeder := utpDialDelaySeeder(t, func(cfg *ClientConfig) {
		cfg.DisableTCP = true
		cfg.DisableUTP = false
	})

	cfg := TestingConfig(t)
	cfg.DisableUTP = true
	cfg.UTPFallback = true
	handshook := make(chan string, 1)
	cfg.Callbacks.CompletedHandshake = func(pc *PeerConn, _ InfoHash) {
		select {
		case handshook <- pc.Network:
		default:
		}
	}

	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	defer cl.Close()

	tt, err := cl.AddTorrent(testutil.GreetingMetaInfo())
	qt.Assert(t, qt.IsNil(err))
	tt.DownloadAll()
	qt.Assert(t, qt.IsFalse(tt.UTPFallback()))

	// Add peer while fallback is OFF. TCP dial will fail.
	peerAddr := ipPortAddr{net.IPv4(127, 0, 0, 1), seeder.LocalPort()}
	tt.AddPeers([]PeerInfo{{Addr: peerAddr}})

	// Wait for dial to fail and peer queue to empty
	deadline := time.Now().Add(5 * time.Second)
	for {
		cl.lock()
		empty := tt.peers.Len() == 0 && len(tt.halfOpen) == 0 && len(tt.failedDials) > 0
		cl.unlock()
		if empty {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for dial to fail and peer queue to empty")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Now enable uTP fallback. This should re-queue the failed peer and connect over uTP.
	tt.SetUTPFallback(true)

	select {
	case netw := <-handshook:
		qt.Check(t, qt.IsTrue(parseNetworkString(netw).Udp))
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for re-queued peer to connect over uTP")
	}
}

// TestUTPFallbackFailedDialRecording checks that failed dials are only kept
// while a switch could still use them, and that the list stays bounded.
func TestUTPFallbackFailedDialRecording(t *testing.T) {
	record := func(tt *Torrent, n int) {
		tt.cl.lock()
		defer tt.cl.unlock()
		for i := range n {
			tt.recordFailedDialLocked(PeerInfo{Addr: ipPortAddr{net.IPv4(10, 0, byte(i>>8), byte(i)), 6881}})
		}
	}
	count := func(tt *Torrent) int {
		tt.cl.lock()
		defer tt.cl.unlock()
		return len(tt.failedDials)
	}
	newTorrent := func(fallback bool) *Torrent {
		cfg := TestingConfig(t)
		cfg.DisableUTP = true
		cfg.UTPFallback = fallback
		cl, err := NewClient(cfg)
		qt.Assert(t, qt.IsNil(err))
		t.Cleanup(func() { cl.Close() })
		tt, err := cl.AddTorrent(testutil.GreetingMetaInfo())
		qt.Assert(t, qt.IsNil(err))
		return tt
	}

	off := newTorrent(false)
	record(off, 10)
	qt.Check(t, qt.Equals(count(off), 0))
	qt.Check(t, qt.Equals(off.Stats().FailedDials, 0))

	on := newTorrent(true)
	record(on, maxFailedDials+50)
	qt.Check(t, qt.Equals(count(on), maxFailedDials))
	qt.Check(t, qt.Equals(on.Stats().FailedDials, maxFailedDials))

	on.SetUTPFallback(true)
	qt.Check(t, qt.Equals(count(on), 0))
	qt.Check(t, qt.Equals(on.Stats().FailedDials, 0))
	record(on, 10)
	qt.Check(t, qt.Equals(count(on), 0))
	qt.Check(t, qt.Equals(on.Stats().FailedDials, 0))
}

func TestUTPFallbackRequeuesInFlightTCPDialWhenSwitched(t *testing.T) {
	seeder := utpDialDelaySeeder(t, func(cfg *ClientConfig) {
		cfg.DisableTCP = true
		cfg.DisableUTP = false
	})

	// Bind a stalling TCP listener on the same port as the uTP-only seeder so
	// an initial TCP dial sits in halfOpen across SetUTPFallback(true).
	tcpLn, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", seeder.LocalPort()))
	if err != nil {
		t.Skipf("cannot bind matching TCP port %d: %v", seeder.LocalPort(), err)
	}
	defer tcpLn.Close()

	tcpAccepted := make(chan net.Conn, 4)
	go func() {
		for {
			c, err := tcpLn.Accept()
			if err != nil {
				return
			}
			tcpAccepted <- c
		}
	}()

	run := func(requeue bool) string {
		cfg := TestingConfig(t)
		cfg.DisableUTP = true
		cfg.UTPFallback = true
		cfg.UTPFallbackRequeueInflight = requeue
		cfg.UTPDialDelay = 0
		cfg.HandshakesTimeout = 150 * time.Millisecond
		handshook := make(chan string, 1)
		cfg.Callbacks.CompletedHandshake = func(pc *PeerConn, _ InfoHash) {
			select {
			case handshook <- pc.Network:
			default:
			}
		}

		cl, err := NewClient(cfg)
		qt.Assert(t, qt.IsNil(err))
		defer cl.Close()

		tt, err := cl.AddTorrent(testutil.GreetingMetaInfo())
		qt.Assert(t, qt.IsNil(err))
		tt.DownloadAll()

		tt.AddPeers([]PeerInfo{{Addr: ipPortAddr{net.IPv4(127, 0, 0, 1), seeder.LocalPort()}}})

		var held net.Conn
		select {
		case held = <-tcpAccepted:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for initial TCP dial")
		}

		// Flip fallback while the TCP dial is still in-flight (not yet in failedDials).
		cl.lock()
		inHalfOpen := len(tt.halfOpen)
		inFailed := len(tt.failedDials)
		cl.unlock()
		qt.Assert(t, qt.Equals(inHalfOpen, 1))
		qt.Assert(t, qt.Equals(inFailed, 0))

		tt.SetUTPFallback(true)
		// Let the TCP handshake stall/fail after the switch.
		held.Close()
		// Close any fallback-plaintext second attempt on the stalled TCP listener.
		go func() {
			for i := 0; i < 2; i++ {
				select {
				case c := <-tcpAccepted:
					_ = c.Close()
				case <-time.After(500 * time.Millisecond):
					return
				}
			}
		}()

		select {
		case netw := <-handshook:
			return netw
		case <-time.After(1 * time.Second):
			return ""
		}
	}

	qt.Check(t, qt.Equals(run(false), ""))
	got := run(true)
	qt.Assert(t, qt.Not(qt.Equals(got, "")))
	qt.Check(t, qt.IsTrue(parseNetworkString(got).Udp))
}
