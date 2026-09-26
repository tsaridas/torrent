package torrent

import (
	"testing"

	qt "github.com/go-quicktest/qt"
)

// torrent-stream keeps untested wires, and proven wires slower than
// SPEED_THRESHOLD, off the head of the stream. downloadRate is 0 for a peer
// that has delivered nothing, so "untested" needs no separate signal.
func TestPeerRestrictedForNowPriority(t *testing.T) {
	const slow = DefaultNowPrioritySlowPeerRate
	for _, c := range []struct {
		name     string
		rate     float64
		slowRate float64
		want     bool
	}{
		{"untested peer", 0, slow, true},
		{"proven but slow (8 KB/s holder seen holding piece 0)", 7934, slow, true},
		{"just under the threshold", slow - 1, slow, true},
		{"at the threshold", slow, slow, false},
		{"proven and fast", 742080, slow, false},
		{"no slow threshold still restricts untested", 0, 0, true},
		{"no slow threshold frees any proven peer", 1, 0, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			qt.Check(t, qt.Equals(peerRestrictedForNowPriority(c.rate, c.slowRate), c.want))
		})
	}
}

func TestSetNowPriorityRequestDefaultsRestriction(t *testing.T) {
	cc := NewDefaultClientConfig()
	qt.Check(t, qt.Equals(cc.NowPriorityRestrictedPeerRequests, 0))
	cc.SetNowPriorityRequestDefaults()
	qt.Check(t, qt.Equals(cc.NowPriorityRestrictedPeerRequests, DefaultNowPriorityRestrictedPeerRequests))
	qt.Check(t, qt.Equals(cc.NowPrioritySlowPeerRate, float64(DefaultNowPrioritySlowPeerRate)))
}
