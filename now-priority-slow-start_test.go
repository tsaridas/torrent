package torrent

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

// TestNominalMaxRequestsForPeakRequests exercises the request-pipeline
// slow-start bypass for peers holding a PiecePriorityNow piece: a freshly
// connected/unchoked peer (peakRequests still zero) that might hold the
// piece blocking playback right now gets nowPrioritySlowStartBypass
// in-flight requests immediately instead of the ordinary slow-start floor
// of 1, while a peer without such a piece, or one that has already proven
// itself (peakRequests > 0), keeps the original doubling-ramp behaviour
// unchanged.
func TestNominalMaxRequestsForPeakRequests(t *testing.T) {
	for _, c := range []struct {
		name                                            string
		peerMaxRequests, peakRequests, maxLocalToRemote maxRequests
		hasNowPiece                                     bool
		want                                            maxRequests
	}{
		{
			name:             "fresh peer without a now-priority piece gets the ordinary slow-start floor of 1",
			peerMaxRequests:  250,
			peakRequests:     0,
			maxLocalToRemote: 1000,
			hasNowPiece:      false,
			want:             1,
		},
		{
			name:             "fresh peer with a now-priority piece bypasses slow start",
			peerMaxRequests:  250,
			peakRequests:     0,
			maxLocalToRemote: 1000,
			hasNowPiece:      true,
			want:             nowPrioritySlowStartBypass,
		},
		{
			name:             "bypass never exceeds the peer's own advertised max",
			peerMaxRequests:  3,
			peakRequests:     0,
			maxLocalToRemote: 1000,
			hasNowPiece:      true,
			want:             3,
		},
		{
			name:             "bypass never exceeds the write-buffer-derived ceiling",
			peerMaxRequests:  250,
			peakRequests:     0,
			maxLocalToRemote: 4,
			hasNowPiece:      true,
			want:             4,
		},
		{
			name:             "a peer that has already ramped up keeps the doubling curve, now-priority piece or not",
			peerMaxRequests:  250,
			peakRequests:     20,
			maxLocalToRemote: 1000,
			hasNowPiece:      true,
			want:             40,
		},
		{
			name:             "a proven peer's ramped-up value is never reduced by lacking a now-priority piece",
			peerMaxRequests:  250,
			peakRequests:     20,
			maxLocalToRemote: 1000,
			hasNowPiece:      false,
			want:             40,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := nominalMaxRequestsForPeakRequests(c.peerMaxRequests, c.peakRequests, c.maxLocalToRemote, c.hasNowPiece)
			qt.Check(t, qt.Equals(got, c.want))
		})
	}
}
