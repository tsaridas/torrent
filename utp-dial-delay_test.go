package torrent

import (
	"expvar"
	"net"
	"os"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/mse"
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

func expvarTorrentInt(key string) int64 {
	if v, ok := torrent.Get(key).(*expvar.Int); ok {
		return v.Value()
	}
	return 0
}

const iaHandshakesKey = "initiated handshakes with bittorrent handshake in mse initial payload"

func TestMSEInitialPayloadHandshakeSeederRequiresObfuscation(t *testing.T) {
	before := expvarTorrentInt(iaHandshakesKey)
	testSeederLeecherPair(
		t,
		func(cfg *ClientConfig) {
			cfg.HeaderObfuscationPolicy = HeaderObfuscationPolicy{Preferred: true, RequirePreferred: true}
		},
		func(cfg *ClientConfig) {
			cfg.HeaderObfuscationPolicy = HeaderObfuscationPolicy{Preferred: true, RequirePreferred: true}
			cfg.MSEInitialPayloadHandshake = true
		},
	)
	qt.Check(t, qt.IsTrue(expvarTorrentInt(iaHandshakesKey) > before))
}

func TestMSEInitialPayloadHandshakeFallsBackToPlaintext(t *testing.T) {
	// The seeder refuses obfuscation, so the leecher's MSE attempt with the handshake in its
	// initial payload fails and the plaintext retry has to send the handshake normally.
	testSeederLeecherPair(
		t,
		func(cfg *ClientConfig) {
			cfg.HeaderObfuscationPolicy = HeaderObfuscationPolicy{Preferred: false, RequirePreferred: true}
		},
		func(cfg *ClientConfig) {
			cfg.HeaderObfuscationPolicy = HeaderObfuscationPolicy{Preferred: true, RequirePreferred: false}
			cfg.MSEInitialPayloadHandshake = true
		},
	)
}

func TestMSEInitialPayloadHandshakeRC4(t *testing.T) {
	// The seeder only accepts RC4, so the stream after the initial payload stays encrypted.
	before := expvarTorrentInt(iaHandshakesKey)
	defer func() { qt.Check(t, qt.IsTrue(expvarTorrentInt(iaHandshakesKey) > before)) }()
	testSeederLeecherPair(
		t,
		func(cfg *ClientConfig) {
			cfg.HeaderObfuscationPolicy = HeaderObfuscationPolicy{Preferred: true, RequirePreferred: true}
			cfg.CryptoSelector = func(provided mse.CryptoMethod) mse.CryptoMethod { return mse.CryptoMethodRC4 }
		},
		func(cfg *ClientConfig) {
			cfg.HeaderObfuscationPolicy = HeaderObfuscationPolicy{Preferred: true, RequirePreferred: true}
			cfg.MSEInitialPayloadHandshake = true
		},
	)
}
