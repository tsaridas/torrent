package torrent

import "time"

// Recommended now-priority request tuning. NewDefaultClientConfig leaves the
// corresponding ClientConfig fields disabled (zero); call
// ClientConfig.SetNowPriorityRequestDefaults to apply these.
const (
	DefaultNowPriorityStealSpeedFactor    = 1.5
	DefaultNowPriorityStealStallThreshold = 2 * time.Second
	DefaultNowPrioritySlowStartRequests   = 8
	DefaultNowPriorityRequestDeadline     = 2 * time.Second
	// Two blocks: enough for an untested peer to prove itself on the head of
	// the stream without holding a meaningful share of it.
	DefaultNowPriorityRestrictedPeerRequests = 2
	// torrent-stream's SPEED_THRESHOLD: 3 * BLOCK_SIZE per second.
	DefaultNowPrioritySlowPeerRate = 3 * 16 * 1024
)

// SetNowPriorityRequestDefaults enables the recommended PiecePriorityNow steal
// and slow-start tuning.
func (cc *ClientConfig) SetNowPriorityRequestDefaults() {
	cc.NowPriorityStealSpeedFactor = DefaultNowPriorityStealSpeedFactor
	cc.NowPriorityStealStallThreshold = DefaultNowPriorityStealStallThreshold
	cc.NowPrioritySlowStartRequests = DefaultNowPrioritySlowStartRequests
	cc.NowPriorityRequestDeadline = DefaultNowPriorityRequestDeadline
	cc.NowPriorityRestrictedPeerRequests = DefaultNowPriorityRestrictedPeerRequests
	cc.NowPrioritySlowPeerRate = DefaultNowPrioritySlowPeerRate
}
