package torrent

import (
	"net"
	"os"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
)

// Starts a seeder of the greeting torrent and returns it with its data dir removed on cleanup.
func utpDialDelaySeeder(t *testing.T, configure func(*ClientConfig)) *Client {
	dir, mi := testutil.GreetingTestTorrent()
	t.Cleanup(func() { os.RemoveAll(dir) })
	cfg := TestingConfig(t)
	cfg.Seed = true
	cfg.DataDir = dir
	cfg.DisableUTP = false
	configure(cfg)
	cl, err := NewClient(cfg)
	qt.Assert(t, qt.IsNil(err))
	t.Cleanup(func() { cl.Close() })
	tt, err := cl.AddTorrent(mi)
	qt.Assert(t, qt.IsNil(err))
	qt.Assert(t, qt.IsTrue(tt.Seeding()))
	return cl
}

// Connects a fresh leecher to seeder's port and returns the network of the first connection that
// completed its handshake.
func utpDialDelayConnect(t *testing.T, seeder *Client, delay time.Duration) string {
	cfg := TestingConfig(t)
	cfg.DisableUTP = false
	cfg.UTPDialDelay = delay
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
	tt, _ := cl.AddTorrent(testutil.GreetingMetaInfo())
	tt.DownloadAll()
	tt.AddPeers([]PeerInfo{{Addr: ipPortAddr{net.IPv4(127, 0, 0, 1), seeder.LocalPort()}}})
	select {
	case network := <-handshook:
		return network
	case <-time.After(10 * time.Second):
		t.Fatal("leecher did not connect")
		return ""
	}
}

func TestUTPDialDelayKeepsTCPPeersOnTCP(t *testing.T) {
	// Dialing both at once, about one loopback connection in six lands on uTP, so 20 all-TCP
	// connections would happen by chance about 3% of the time.
	seeder := utpDialDelaySeeder(t, func(*ClientConfig) {})
	for range 20 {
		qt.Check(t, qt.Equals(parseNetworkString(utpDialDelayConnect(t, seeder, time.Hour)).Tcp, true))
	}
}

func TestUTPDialDelayFallsBackToUTPOnceTCPFails(t *testing.T) {
	// The TCP dial is refused at once, so uTP starts without waiting out the delay.
	seeder := utpDialDelaySeeder(t, func(cfg *ClientConfig) { cfg.DisableTCP = true })
	start := time.Now()
	network := utpDialDelayConnect(t, seeder, time.Hour)
	qt.Check(t, qt.IsTrue(parseNetworkString(network).Udp))
	qt.Check(t, qt.IsTrue(time.Since(start) < 5*time.Second))
}
