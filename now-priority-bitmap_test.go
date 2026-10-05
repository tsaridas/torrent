package torrent

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestNowPriorityBitmapTracksSetPriorityNow(t *testing.T) {
	tor := greetingTorrent(t)
	tor.cl.config.NowPieceBitmap = true
	qt.Check(t, qt.IsFalse(tor._nowPriorityPieces.Contains(0)))
	tor.piece(0).SetPriorityNow()
	qt.Check(t, qt.IsTrue(tor._nowPriorityPieces.Contains(0)))
	tor.piece(0).SetPriority(PiecePriorityNormal)
	qt.Check(t, qt.IsFalse(tor._nowPriorityPieces.Contains(0)))
}

func TestHasWantedNowUsesBitmapWhenEnabled(t *testing.T) {
	tor := greetingTorrent(t)
	tor.cl.config.NowPieceBitmap = true
	a, _ := endgameTestPeers(t, tor)
	tor.piece(0).SetPriorityNow()
	tor.cl.lock()
	qt.Check(t, qt.IsTrue(a.hasWantedNowPriorityPiece()))
	a._peerPieces.Clear()
	qt.Check(t, qt.IsFalse(a.hasWantedNowPriorityPiece()))
	tor.cl.unlock()
}

// CountFreshNowPeers is the probe's "fresh peers holding Now at 1s" metric;
// a peer still choking us cannot serve those pieces and must not count.
func TestCountFreshNowPeersSkipsChoking(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	a, _ := endgameTestPeers(t, tor)
	qt.Check(t, qt.Equals(tor.CountFreshNowPeers(), 2))
	tor.cl.lock()
	a.peerChoking = true
	tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.CountFreshNowPeers(), 1))
}
