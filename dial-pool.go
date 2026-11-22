package torrent

import (
	"context"
	"time"
)

type dialPool struct {
	resCh chan DialResult
	addr  string
	left  int
	// Dials held back by holdBack, not started yet. They start when holdTimer fires or every
	// started dial has failed, and are dropped once a started dial connects.
	held      []heldDial
	holdTimer *time.Timer
}

type heldDial struct {
	ctx    context.Context
	dialer Dialer
}

// Returns the first dial that connects, or a zero result once every dial, held ones included, has
// failed.
func (me *dialPool) getFirst() (res DialResult) {
	for res.Conn == nil {
		if me.left == 0 {
			if len(me.held) == 0 {
				return
			}
			// Everything started so far failed, so there is nothing left to wait for.
			me.releaseHeld()
		}
		var fired <-chan time.Time
		if me.holdTimer != nil {
			fired = me.holdTimer.C
		}
		select {
		case res = <-me.resCh:
			me.left--
		case <-fired:
			me.releaseHeld()
		}
	}
	// A started dial connected: the held ones are not needed.
	me.dropHeld()
	return
}

func (me *dialPool) add(ctx context.Context, dialer Dialer) {
	me.left++
	go func() {
		me.resCh <- DialResult{
			dialFromSocket(ctx, dialer, me.addr),
			dialer,
		}
	}()
}

// holdBack queues dialer to start once delay has passed since the first held dial was queued, or as
// soon as every dial started with add has failed. getFirst drops it unstarted if a started dial
// connects first.
func (me *dialPool) holdBack(ctx context.Context, dialer Dialer, delay time.Duration) {
	me.held = append(me.held, heldDial{ctx, dialer})
	if me.holdTimer == nil {
		me.holdTimer = time.NewTimer(delay)
	}
}

func (me *dialPool) releaseHeld() {
	held := me.held
	me.dropHeld()
	for _, h := range held {
		me.add(h.ctx, h.dialer)
	}
}

func (me *dialPool) dropHeld() {
	me.held = nil
	if me.holdTimer != nil {
		me.holdTimer.Stop()
		me.holdTimer = nil
	}
}

func (me *dialPool) startDrainer() {
	me.dropHeld()
	go me.drainAndCloseRemainingDials()
}

func (me *dialPool) drainAndCloseRemainingDials() {
	for me.left > 0 {
		conn := (<-me.resCh).Conn
		me.left--
		if conn != nil {
			conn.Close()
		}
	}
}
