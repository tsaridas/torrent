package torrent

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
)

// fakeDialer connects, fails or blocks until ctx is done, after an optional wait, and records when
// it was asked to dial.
type fakeDialer struct {
	network string
	wait    time.Duration
	fail    bool
	block   bool
	dials   atomic.Int32
	started atomic.Int64 // unix nanos of the first Dial call
}

func (d *fakeDialer) DialerNetwork() string { return d.network }

func (d *fakeDialer) Dial(ctx context.Context, _ string) (net.Conn, error) {
	if d.dials.Add(1) == 1 {
		d.started.Store(time.Now().UnixNano())
	}
	if d.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	select {
	case <-time.After(d.wait):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if d.fail {
		return nil, errors.New("refused")
	}
	c, _ := net.Pipe()
	return c, nil
}

func newTestDialPool() *dialPool {
	return &dialPool{resCh: make(chan DialResult), addr: "192.0.2.1:6881"}
}

func TestDialPoolDropsHeldDialWhenAStartedOneConnects(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tcp := &fakeDialer{network: "tcp4"}
	utp := &fakeDialer{network: "udp4"}
	p := newTestDialPool()
	p.add(ctx, tcp)
	p.holdBack(ctx, utp, 50*time.Millisecond)
	res := p.getFirst()
	qt.Assert(t, qt.IsNotNil(res.Conn))
	qt.Check(t, qt.Equals(res.Dialer, Dialer(tcp)))
	res.Conn.Close()
	p.startDrainer()
	time.Sleep(100 * time.Millisecond)
	qt.Check(t, qt.Equals(utp.dials.Load(), int32(0)))
}

func TestDialPoolStartsHeldDialOnceStartedOnesFail(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tcp4 := &fakeDialer{network: "tcp4", fail: true}
	tcp6 := &fakeDialer{network: "tcp6", fail: true, wait: 10 * time.Millisecond}
	utp := &fakeDialer{network: "udp4"}
	p := newTestDialPool()
	start := time.Now()
	p.add(ctx, tcp4)
	p.add(ctx, tcp6)
	p.holdBack(ctx, utp, time.Hour)
	res := p.getFirst()
	qt.Assert(t, qt.IsNotNil(res.Conn))
	qt.Check(t, qt.Equals(res.Dialer, Dialer(utp)))
	res.Conn.Close()
	// Released by the failures, not the timer, and only after both of them.
	qt.Check(t, qt.IsTrue(time.Since(start) < time.Second))
	qt.Check(t, qt.IsTrue(utp.started.Load() >= start.Add(10*time.Millisecond).UnixNano()))
	p.startDrainer()
}

func TestDialPoolStartsHeldDialAfterTheDelay(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tcp := &fakeDialer{network: "tcp4", block: true}
	utp := &fakeDialer{network: "udp4"}
	p := newTestDialPool()
	start := time.Now()
	p.add(ctx, tcp)
	const delay = 50 * time.Millisecond
	p.holdBack(ctx, utp, delay)
	res := p.getFirst()
	qt.Assert(t, qt.IsNotNil(res.Conn))
	qt.Check(t, qt.Equals(res.Dialer, Dialer(utp)))
	res.Conn.Close()
	qt.Check(t, qt.IsTrue(utp.started.Load() >= start.Add(delay).UnixNano()))
	// The blocked TCP dial is still outstanding; the drainer collects it once ctx ends.
	qt.Check(t, qt.Equals(p.left, 1))
	cancel()
	p.startDrainer()
}

func TestDialPoolAllFailIncludingHeld(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tcp := &fakeDialer{network: "tcp4", fail: true}
	utp := &fakeDialer{network: "udp4", fail: true}
	p := newTestDialPool()
	p.add(ctx, tcp)
	p.holdBack(ctx, utp, time.Hour)
	res := p.getFirst()
	qt.Check(t, qt.IsNil(res.Conn))
	qt.Check(t, qt.Equals(utp.dials.Load(), int32(1)))
	qt.Check(t, qt.Equals(p.left, 0))
	// A second getFirst, as for the header-obfuscation retry, has nothing left to wait for.
	qt.Check(t, qt.IsNil(p.getFirst().Conn))
}

func TestDialPoolHoldsNothingAfterAConnection(t *testing.T) {
	// The header-obfuscation retry calls add and getFirst again after a connection: dials dropped
	// by the first getFirst must not come back.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tcp := &fakeDialer{network: "tcp4"}
	utp := &fakeDialer{network: "udp4"}
	p := newTestDialPool()
	p.add(ctx, tcp)
	p.holdBack(ctx, utp, time.Millisecond)
	first := p.getFirst()
	qt.Assert(t, qt.IsNotNil(first.Conn))
	first.Conn.Close()
	retry := &fakeDialer{network: "tcp4", fail: true}
	p.add(ctx, retry)
	qt.Check(t, qt.IsNil(p.getFirst().Conn))
	qt.Check(t, qt.Equals(utp.dials.Load(), int32(0)))
}
