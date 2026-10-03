package torrent

import (
	"testing"

	"github.com/go-quicktest/qt"
)

func TestEndgamePieceWithoutPeersOrNotNow(t *testing.T) {
	tor := greetingTorrent(t)
	// Piece 0 exists but is not Now and there are no peers.
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 16, 4), 0))
	qt.Check(t, qt.Equals(tor.EndgamePiece(-1, 16, 4), 0))
	qt.Check(t, qt.Equals(tor.EndgamePiece(99, 16, 4), 0))
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 0, 4), 0))
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 16, 0), 0))

	// SetPriorityNow takes the client lock itself.
	tor.piece(0).SetPriorityNow()
	// Now, but still no peers to ask.
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 16, 4), 0))
}

func TestEndgamePieceAllReceivedIsNoop(t *testing.T) {
	tor := greetingTorrent(t)
	tor.piece(0).SetPriorityNow()
	tor.cl.lock()
	p := tor.piece(0)
	// dirtyChunks = received; with every chunk dirty there is nothing to ask for.
	for ci := chunkIndexType(0); ci < p.numChunks(); ci++ {
		p.unpendChunkIndex(ci)
	}
	tor.cl.unlock()
	qt.Check(t, qt.Equals(tor.EndgamePiece(0, 16, 4), 0))
}
