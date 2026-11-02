package torrent

import "time"

// Recommended now-priority request tuning. NewDefaultClientConfig leaves the
// corresponding ClientConfig fields disabled (zero); call
// ClientConfig.SetNowPriorityRequestDefaults to apply these.
const (
	DefaultNowPriorityStealSpeedFactor    = 1.5
	DefaultNowPriorityStealStallThreshold = 2 * time.Second
	DefaultNowPrioritySlowStartRequests   = 8
)

const (
	nowPriorityStealSpeedFactorEnvKey    = "TORRENT_NOW_PRIORITY_STEAL_SPEED_FACTOR"
	nowPriorityStealStallThresholdEnvKey = "TORRENT_NOW_PRIORITY_STEAL_STALL"
	nowPrioritySlowStartRequestsEnvKey   = "TORRENT_NOW_PRIORITY_SLOW_START"
)

// SetNowPriorityRequestDefaults enables the recommended PiecePriorityNow steal
// and slow-start tuning.
func (cc *ClientConfig) SetNowPriorityRequestDefaults() {
	cc.NowPriorityStealSpeedFactor = DefaultNowPriorityStealSpeedFactor
	cc.NowPriorityStealStallThreshold = DefaultNowPriorityStealStallThreshold
	cc.NowPrioritySlowStartRequests = DefaultNowPrioritySlowStartRequests
}
