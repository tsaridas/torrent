package torrent

import (
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
)

func TestStealAllowedBySpeed(t *testing.T) {
	t0 := time.Unix(1000, 0)
	factor := DefaultNowPriorityStealSpeedFactor
	stall := DefaultNowPriorityStealStallThreshold
	var never time.Time
	for _, c := range []struct {
		name         string
		stealerRate  float64
		existingRate float64
		stealerLast  time.Time
		existingLast time.Time
		now          time.Time
		factor       float64
		stall        time.Duration
		want         bool
	}{
		{
			name:         "similar speed, recently useful holder is not stolen from",
			stealerRate:  100,
			existingRate: 100,
			stealerLast:  t0,
			existingLast: t0,
			now:          t0.Add(time.Second),
			factor:       factor,
			stall:        stall,
			want:         false,
		},
		{
			name:         "meaningfully faster stealer steals",
			stealerRate:  200,
			existingRate: 100,
			stealerLast:  t0,
			existingLast: t0,
			now:          t0.Add(time.Second),
			factor:       factor,
			stall:        stall,
			want:         true,
		},
		{
			name:         "only marginally faster stealer does not steal",
			stealerRate:  110,
			existingRate: 100,
			stealerLast:  t0,
			existingLast: t0,
			now:          t0.Add(time.Second),
			factor:       factor,
			stall:        stall,
			want:         false,
		},
		{
			name:         "silent-so-far holder is stolen from by a peer that has delivered",
			stealerRate:  10,
			existingRate: 10,
			stealerLast:  t0,
			existingLast: never,
			now:          t0,
			factor:       factor,
			stall:        stall,
			want:         true,
		},
		{
			name:         "holder past stall threshold is stealable even if it was once fast",
			stealerRate:  10,
			existingRate: 1000,
			stealerLast:  t0.Add(stall),
			existingLast: t0,
			now:          t0.Add(stall + time.Millisecond),
			factor:       factor,
			stall:        stall,
			want:         true,
		},
		{
			name:         "holder still within stall threshold is not stolen from on rate alone",
			stealerRate:  10,
			existingRate: 1000,
			stealerLast:  t0,
			existingLast: t0,
			now:          t0.Add(stall - time.Millisecond),
			factor:       factor,
			stall:        stall,
			want:         false,
		},
		{
			name:         "zero-rate stealer never counts as faster",
			stealerRate:  0,
			existingRate: 0,
			stealerLast:  t0,
			existingLast: t0,
			now:          t0.Add(time.Millisecond),
			factor:       factor,
			stall:        stall,
			want:         false,
		},
		{
			name:         "factor <= 1 disables the speed check",
			stealerRate:  1000,
			existingRate: 1,
			stealerLast:  t0,
			existingLast: t0,
			now:          t0.Add(time.Millisecond),
			factor:       1,
			stall:        stall,
			want:         false,
		},
		{
			name:         "stall <= 0 disables the quiet-holder check",
			stealerRate:  10,
			existingRate: 10,
			stealerLast:  t0,
			existingLast: never,
			now:          t0,
			factor:       factor,
			stall:        0,
			want:         false,
		},

		// The carousel. At connection time neither peer has delivered a chunk,
		// so the holder looks quiet and every peer looks like a valid stealer.
		// Observed in production: one head piece passed to five distinct peers
		// inside a second, all at 0 B/s, the previous stealer becoming the next
		// victim. Only StealRequestGrace bounded the churn.
		{
			name:         "two unproven peers must not swap the block at cold start",
			stealerRate:  0,
			existingRate: 0,
			stealerLast:  never,
			existingLast: never,
			now:          t0,
			factor:       factor,
			stall:        stall,
			want:         false,
		},
		{
			name:         "a delivering peer still displaces a silent holder",
			stealerRate:  500,
			existingRate: 0,
			stealerLast:  t0,
			existingLast: never,
			now:          t0,
			factor:       factor,
			stall:        stall,
			want:         true,
		},
		{
			name:         "an unproven peer cannot displace a holder that did deliver",
			stealerRate:  0,
			existingRate: 0,
			stealerLast:  never,
			existingLast: t0,
			now:          t0.Add(stall + time.Millisecond),
			factor:       factor,
			stall:        stall,
			want:         false,
		},
		{
			name:         "more recent deliverer displaces a quiet holder at equal rate",
			stealerRate:  100,
			existingRate: 100,
			stealerLast:  t0.Add(3 * time.Second),
			existingLast: t0,
			now:          t0.Add(3 * time.Second),
			factor:       factor,
			stall:        stall,
			want:         true,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := stealAllowedBySpeed(
				c.stealerRate, c.existingRate,
				c.stealerLast, c.existingLast, c.now,
				c.factor, c.stall,
			)
			qt.Check(t, qt.Equals(got, c.want))
		})
	}
}

// TestStealerBetter covers the guard on its own: either a higher measured rate
// or a more recent useful chunk qualifies, and two peers that have delivered
// nothing qualify on neither.
func TestStealerBetter(t *testing.T) {
	t0 := time.Unix(1000, 0)
	var never time.Time
	qt.Check(t, qt.IsTrue(stealerBetter(200, 100, never, never)))   // faster wins alone
	qt.Check(t, qt.IsTrue(stealerBetter(100, 100, t0, never)))      // delivered vs never
	qt.Check(t, qt.IsTrue(stealerBetter(100, 100, t0.Add(1), t0)))  // more recent
	qt.Check(t, qt.IsFalse(stealerBetter(0, 0, never, never)))      // the carousel case
	qt.Check(t, qt.IsFalse(stealerBetter(100, 100, never, t0)))     // unproven stealer
	qt.Check(t, qt.IsFalse(stealerBetter(100, 100, t0, t0.Add(1)))) // holder more recent
}
