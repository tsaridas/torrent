package torrent

import "time"

// Bandwidth-delay estimation behind ClientConfig.BDPRequestWindow.
//
// Each useful chunk that answers a request this peer holds contributes one
// sample: the bytes delivered and the time since that request was sent. Over
// a period of bdpSamplePeriod the estimator keeps the delivered byte rate and
// the best (lowest) request-to-response latency, which approximates the path
// RTT plus the peer's own service time. The window target is twice their
// product, in blocks, so a refill at half the window still leaves a full
// BDP in flight.

const (
	bdpSamplePeriod = 200 * time.Millisecond
	// bdpIdle resets the estimate after a gap: a rate measured before the
	// peer went quiet says nothing about what it can do now.
	bdpIdle        = 2 * time.Second
	bdpMaxRequests = 256
)

type requestWindowBDP struct {
	start time.Time
	last  time.Time
	bytes int64
	best  time.Duration
	rtt   time.Duration
	rate  float64
}

func (b *requestWindowBDP) sample(now time.Time, wait time.Duration, n int) {
	if wait <= 0 || wait > bdpIdle {
		return
	}
	if b.start.IsZero() || now.Sub(b.last) > bdpIdle {
		*b = requestWindowBDP{start: now, last: now, best: wait}
		return
	}
	b.last = now
	b.bytes += int64(n)
	if b.best == 0 || wait < b.best {
		b.best = wait
	}
	elapsed := now.Sub(b.start)
	if elapsed < bdpSamplePeriod {
		return
	}
	rate := float64(b.bytes) / elapsed.Seconds()
	if b.rate == 0 {
		b.rate, b.rtt = rate, b.best
	} else {
		b.rate = (b.rate + rate) / 2
		b.rtt = (3*b.rtt + b.best) / 4
	}
	b.start, b.bytes, b.best = now, 0, 0
}

// target is the BDP window in blocks, never below floor and never above
// limit. Zero means no estimate yet.
func (b *requestWindowBDP) target(block int, floor, limit maxRequests) maxRequests {
	if b.rate <= 0 || b.rtt <= 0 || block <= 0 || limit <= 0 {
		return 0
	}
	blocks := 2 * b.rate * b.rtt.Seconds() / float64(block)
	return min(limit, maxInt(floor, maxRequests(min(float64(limit), blocks+1))))
}

func (cn *Peer) bdpWindowEnabled() bool {
	return cn.t != nil && cn.t.cl != nil && cn.t.cl.config.BDPRequestWindow
}

func (cn *Peer) earlyRefillEnabled() bool {
	return cn.t != nil && cn.t.cl != nil && cn.t.cl.config.EarlyRequestRefill
}

// bdpRequestWindow is the BDP-sized window for this peer, or 0 when the
// feature is off, the peer is choking us, or the estimate is stale.
func (cn *Peer) bdpRequestWindow() maxRequests {
	if !cn.bdpWindowEnabled() || cn.peerChoking || cn.requestBDP.rate == 0 ||
		time.Since(cn.requestBDP.last) > bdpIdle {
		return 0
	}
	return cn.requestBDP.target(int(cn.t.chunkSize), torrentStreamRequests(cn.t.numUnchokedPeers),
		min(cn.PeerMaxRequests, maxLocalToRemoteRequests, bdpMaxRequests))
}
