package torrent

import (
	"os"
	"testing"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/internal/testutil"
	"github.com/anacrolix/torrent/storage"
)

// greetingTorrent is the 13-byte greeting torrent (pieces of 5, 5, 3 bytes)
// with 2-byte chunks, so piece 0 is three chunks: 2, 2, 1.
func greetingTorrent(t *testing.T) *Torrent {
	t.Helper()
	dir, mi := testutil.GreetingTestTorrent()
	t.Cleanup(func() { os.RemoveAll(dir) })
	var cl Client
	cl.init(TestingConfig(t))
	cl.initLogger()
	tor := cl.newTorrent(mi.HashInfoBytes(),
		storage.NewFileWithCompletion(t.TempDir(), storage.NewMapPieceCompletion()))
	tor.setChunkSize(2)
	tor.cl.lock()
	qt.Assert(t, qt.IsNil(tor.setInfoBytesLocked(mi.InfoBytes)))
	tor.cl.unlock()
	qt.Assert(t, qt.HasLen(tor.pieces, 3))
	return tor
}

func markWritten(tor *Torrent, piece pieceIndex, chunk RequestIndex) {
	tor.cl.lock()
	ri := tor.pieceRequestIndexOffset(piece) + chunk
	tor.dirtyChunks.Add(ri)
	tor.writtenChunks.Add(ri)
	tor.cl.unlock()
}

func TestReadableUnverifiedLen(t *testing.T) {
	tor := greetingTorrent(t)
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 100), int64(0)))

	// Dirty but not yet written: receiveChunk marks a chunk dirty before the
	// write, so dirty alone must not count as readable.
	tor.cl.lock()
	tor.dirtyChunks.Add(tor.pieceRequestIndexOffset(0))
	tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 100), int64(0)))

	markWritten(tor, 0, 0)
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 100), int64(2)))
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(1, 100), int64(1)), qt.Commentf("mid-chunk offset"))

	// A gap stops the contiguous run.
	markWritten(tor, 0, 2)
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 100), int64(2)))
	markWritten(tor, 0, 1)
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 100), int64(5)), qt.Commentf("whole piece 0; piece 1 not written"))
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 3), int64(3)), qt.Commentf("bounded by max"))

	// Runs across a piece boundary.
	markWritten(tor, 1, 0)
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 100), int64(7)))

	// A hash failure re-pends the piece; its bytes are no longer readable.
	tor.cl.lock()
	tor.pendAllChunkSpecs(0)
	tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 100), int64(0)))

	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(-1, 100), int64(0)))
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 0), int64(0)))
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(13, 10), int64(0)), qt.Commentf("past the end"))
}

// pendChunkIndex (a single chunk re-pended) clears its written bit too.
func TestPendChunkIndexClearsWritten(t *testing.T) {
	tor := greetingTorrent(t)
	markWritten(tor, 0, 0)
	markWritten(tor, 0, 1)
	tor.cl.lock()
	tor.piece(0).pendChunkIndex(0)
	tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(0, 100), int64(0)))
	qt.Check(t, qt.Equals(tor.ReadableUnverifiedLen(2, 100), int64(2)))
}

// Only genuine hash failures count. The initial storage check hashes pieces
// that have not been downloaded yet; counting those made the server purge a
// torrent's HLS output six times in one n200 round with no real failure.
func TestPieceHashFailuresCountsOnlyGenuineFailures(t *testing.T) {
	tor := greetingTorrent(t)
	tor.cl.lock()
	p := tor.piece(0)
	p.storageCompletionOk = false
	tor.pieceHashed(0, false, nil)
	tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.PieceHashFailures(0), int64(0)), qt.Commentf("initial check must not count"))

	tor.cl.lock()
	p.storageCompletionOk = true
	tor.pieceHashed(0, false, nil)
	tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.PieceHashFailures(0), int64(1)))

	// Downloaded this session but still behind the initial-check backlog
	// (storageCompletionOk false): must count so unverified readers purge.
	tor.cl.lock()
	p.hashFailures = 0
	p.storageCompletionOk = false
	tor.dirtyChunks.Add(tor.pieceRequestIndexOffset(0))
	tor.pieceHashed(0, false, nil)
	tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.PieceHashFailures(0), int64(1)), qt.Commentf("session-received failure must count"))

	qt.Check(t, qt.Equals(tor.PieceHashFailures(-1), int64(0)))
	qt.Check(t, qt.Equals(tor.PieceHashFailures(99), int64(0)))
}

// With no connections there is nobody to ask; it must not panic or count.
func TestShadowRequestAheadWithoutPeers(t *testing.T) {
	tor := greetingTorrent(t)
	qt.Check(t, qt.Equals(tor.ShadowRequestAhead(0, 4, 2), 0))
	qt.Check(t, qt.Equals(tor.ShadowRequestAhead(-1, 4, 2), 0))
	qt.Check(t, qt.Equals(tor.ShadowRequestAhead(0, 0, 2), 0))
}

// torrent-stream's getRequestsNumber: ~50 while few peers unchoke us,
// falling to 5 at 30+.
func TestTorrentStreamRequests(t *testing.T) {
	for _, c := range []struct {
		unchoked int
		want     maxRequests
	}{
		{0, 50}, {1, 50}, {30, 5}, {100, 5}, {15, 8}, {8, 20},
	} {
		qt.Check(t, qt.Equals(torrentStreamRequests(c.unchoked), c.want), qt.Commentf("unchoked=%d", c.unchoked))
	}
}
