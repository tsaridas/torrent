package torrent

import (
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
)

func TestNowPriorityHotswapAllowed(t *testing.T) {
	now := time.Now()
	recent := now.Add(-100 * time.Millisecond)
	stale := now.Add(-2 * time.Second)
	const slow = DefaultNowPrioritySlowPeerRate
	for _, c := range []struct {
		name        string
		stealer     float64
		holder      float64
		stealerLast time.Time
		slowRate    float64
		want        bool
	}{
		{"delivering peer takes an untested holder's block", 1 << 20, 0, recent, slow, true},
		{"delivering peer takes a slow holder's block", 1 << 20, 20 << 10, recent, slow, true},
		{"holder at the slow threshold keeps its block", 1 << 20, slow, recent, slow, false},
		{"holder above half the stealer keeps its block", 30 << 10, 20 << 10, recent, slow, false},
		{"stealer below one block per second cannot hotswap", 8 << 10, 0, recent, slow, false},
		{"stealer that has not delivered recently cannot hotswap", 1 << 20, 0, stale, slow, false},
		{"two untested peers never hotswap", 0, 0, time.Time{}, slow, false},
		{"zero slow rate uses the default", 1 << 20, 20 << 10, recent, 0, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := nowPriorityHotswapAllowed(c.stealer, c.holder, c.stealerLast, now, c.slowRate)
			qt.Check(t, qt.Equals(got, c.want))
		})
	}
}
