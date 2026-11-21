package torrent

import (
	"bytes"
	"io"
	"net"
	"testing"
	"time"

	"github.com/anacrolix/chansync"
	"github.com/dustin/go-humanize"

	pp "github.com/anacrolix/torrent/peer_protocol"
)

func TestPeerConnMsgWriterWakesAfterEmptyFill(t *testing.T) {
	sender, receiver := net.Pipe()
	defer receiver.Close()
	if err := receiver.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	msg := pp.Message{Type: pp.Unchoke}
	writer := &peerConnMsgWriter{
		closed:      new(chansync.SetOnce),
		w:           sender,
		writeBuffer: new(bytes.Buffer),
	}
	announced, pending := false, false
	writer.fillWriteBuffer = func() {
		if pending {
			pending = false
			writer.write(msg)
		}
	}
	writer.keepAlive = func() bool {
		if !announced {
			// Work becomes ready after the empty fill, but only the next
			// fill can put it in the buffer.
			announced, pending = true, true
			writer.writeCond.Broadcast()
		}
		return false
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		writer.run(time.Hour)
	}()
	defer func() {
		writer.closed.Set()
		_ = sender.Close()
		<-done
	}()

	want := msg.MustMarshalBinary()
	got := make([]byte, len(want))
	if _, err := io.ReadFull(receiver, got); err != nil {
		t.Fatalf("read work queued after empty fill: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}

func PieceMsg(length int64) pp.Message {
	return pp.Message{
		Type:  pp.Piece,
		Index: pp.Integer(0),
		Begin: pp.Integer(0),
		Piece: make([]byte, length),
	}
}

var benchmarkPieceLengths = []int{defaultChunkSize, 1 << 20, 4 << 20, 8 << 20}

func runBenchmarkWriteToBuffer(b *testing.B, length int64) {
	writer := &peerConnMsgWriter{
		writeBuffer: &bytes.Buffer{},
	}
	msg := PieceMsg(length)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		//b.StopTimer()
		writer.writeBuffer.Reset()
		//b.StartTimer()
		writer.writeToBuffer(msg)
	}
}

func BenchmarkWritePieceMsg(b *testing.B) {
	for _, length := range benchmarkPieceLengths {
		b.Run(humanize.IBytes(uint64(length)), func(b *testing.B) {
			b.Run("ToBuffer", func(b *testing.B) {
				b.SetBytes(int64(length))
				runBenchmarkWriteToBuffer(b, int64(length))
			})
			b.Run("MarshalBinary", func(b *testing.B) {
				b.SetBytes(int64(length))
				runBenchmarkMarshalBinaryWrite(b, int64(length))
			})
		})
	}
}

func runBenchmarkMarshalBinaryWrite(b *testing.B, length int64) {
	writer := &peerConnMsgWriter{
		writeBuffer: &bytes.Buffer{},
	}
	msg := PieceMsg(length)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		//b.StopTimer()
		writer.writeBuffer.Reset()
		//b.StartTimer()
		writer.writeBuffer.Write(msg.MustMarshalBinary())
	}
}
