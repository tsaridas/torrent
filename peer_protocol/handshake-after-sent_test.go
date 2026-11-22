package peer_protocol

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"

	"github.com/anacrolix/torrent/metainfo"
)

// HandshakeBytes is exactly what Handshake writes first, so an MSE initiator
// that sends it as the initial payload says the same thing to the peer.
func TestHandshakeBytesMatchesHandshake(t *testing.T) {
	ih := metainfo.Hash{1, 2, 3}
	peerID := [20]byte{4, 5, 6}
	var ext PeerExtensionBits
	ext.SetBit(ExtensionBitFast, true)
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	go func() {
		_, _ = Handshake(context.Background(), a, &ih, peerID, ext)
	}()
	got := make([]byte, 68)
	_ = b.SetDeadline(time.Now().Add(5 * time.Second))
	_, err := io.ReadFull(b, got)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.DeepEquals(got, HandshakeBytes(ih, peerID, ext)))
}

// HandshakeAfterSent only reads: it parses the peer's half and writes nothing.
func TestHandshakeAfterSentOnlyReads(t *testing.T) {
	ih := metainfo.Hash{7}
	remoteID := [20]byte{8}
	var remoteExt PeerExtensionBits
	remoteExt.SetBit(ExtensionBitLtep, true)
	var wrote bytes.Buffer
	rw := struct {
		io.Reader
		io.Writer
	}{bytes.NewReader(HandshakeBytes(ih, remoteID, remoteExt)), &wrote}
	res, err := HandshakeAfterSent(context.Background(), rw, ih)
	qt.Assert(t, qt.IsNil(err))
	qt.Check(t, qt.Equals(res.Hash, ih))
	qt.Check(t, qt.Equals(res.PeerID, remoteID))
	qt.Check(t, qt.Equals(res.PeerExtensionBits, remoteExt))
	qt.Check(t, qt.Equals(wrote.Len(), 0))
}
