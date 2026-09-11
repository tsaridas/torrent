package torrent

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

// Tests the PiecePriorityNow slow-start bypass in nominalMaxRequestsForPeakRequests.
func TestNominalMaxRequestsForPeakRequests(t *testing.T) {
	bypass := maxRequests(DefaultNowPrioritySlowStartRequests)
	for _, c := range []struct {
		name                                            string
		peerMaxRequests, peakRequests, maxLocalToRemote maxRequests
		bypass                                          maxRequests
		hasNowPiece                                     bool
		want                                            maxRequests
	}{
		{
			name:             "fresh peer without a now-priority piece gets the ordinary slow-start floor of 1",
			peerMaxRequests:  250,
			peakRequests:     0,
			maxLocalToRemote: 1000,
			bypass:           bypass,
			hasNowPiece:      false,
			want:             1,
		},
		{
			name:             "fresh peer with a now-priority piece bypasses slow start",
			peerMaxRequests:  250,
			peakRequests:     0,
			maxLocalToRemote: 1000,
			bypass:           bypass,
			hasNowPiece:      true,
			want:             bypass,
		},
		{
			name:             "bypass of 0 keeps the ordinary slow-start floor",
			peerMaxRequests:  250,
			peakRequests:     0,
			maxLocalToRemote: 1000,
			bypass:           0,
			hasNowPiece:      true,
			want:             1,
		},
		{
			name:             "bypass never exceeds the peer's own advertised max",
			peerMaxRequests:  3,
			peakRequests:     0,
			maxLocalToRemote: 1000,
			bypass:           bypass,
			hasNowPiece:      true,
			want:             3,
		},
		{
			name:             "bypass never exceeds the write-buffer-derived ceiling",
			peerMaxRequests:  250,
			peakRequests:     0,
			maxLocalToRemote: 4,
			bypass:           bypass,
			hasNowPiece:      true,
			want:             4,
		},
		{
			name:             "a peer that has already ramped up keeps the doubling curve",
			peerMaxRequests:  250,
			peakRequests:     20,
			maxLocalToRemote: 1000,
			bypass:           bypass,
			hasNowPiece:      true,
			want:             40,
		},
		{
			name:             "a proven peer's ramped-up value is never reduced by lacking a now-priority piece",
			peerMaxRequests:  250,
			peakRequests:     20,
			maxLocalToRemote: 1000,
			bypass:           bypass,
			hasNowPiece:      false,
			want:             40,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := nominalMaxRequestsForPeakRequests(
				c.peerMaxRequests, c.peakRequests, c.maxLocalToRemote, c.bypass, c.hasNowPiece,
			)
			qt.Check(t, qt.Equals(got, c.want))
		})
	}
}

func TestNewDefaultClientConfigNowPriorityDisabled(t *testing.T) {
	cc := NewDefaultClientConfig()
	qt.Check(t, qt.Equals(cc.NowPriorityStealSpeedFactor, 0.0))
	qt.Check(t, qt.Equals(cc.NowPriorityStealStallThreshold, 0))
	qt.Check(t, qt.Equals(cc.NowPrioritySlowStartRequests, maxRequests(0)))
}

func TestSetNowPriorityRequestDefaults(t *testing.T) {
	cc := NewDefaultClientConfig()
	cc.SetNowPriorityRequestDefaults()
	qt.Check(t, qt.Equals(cc.NowPriorityStealSpeedFactor, DefaultNowPriorityStealSpeedFactor))
	qt.Check(t, qt.Equals(cc.NowPriorityStealStallThreshold, DefaultNowPriorityStealStallThreshold))
	qt.Check(t, qt.Equals(cc.NowPrioritySlowStartRequests, maxRequests(DefaultNowPrioritySlowStartRequests)))
}
