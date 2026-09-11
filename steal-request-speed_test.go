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
	for _, c := range []struct {
		name         string
		stealerRate  float64
		existingRate float64
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
			existingLast: t0,
			now:          t0.Add(time.Second),
			factor:       factor,
			stall:        stall,
			want:         false,
		},
		{
			name:         "silent-so-far holder is stealable regardless of rate",
			stealerRate:  10,
			existingRate: 10,
			existingLast: time.Time{},
			now:          t0,
			factor:       factor,
			stall:        stall,
			want:         true,
		},
		{
			name:         "holder past stall threshold is stealable even if it was once fast",
			stealerRate:  10,
			existingRate: 1000,
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
			existingLast: time.Time{},
			now:          t0,
			factor:       factor,
			stall:        0,
			want:         false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := stealAllowedBySpeed(c.stealerRate, c.existingRate, c.existingLast, c.now, c.factor, c.stall)
			qt.Check(t, qt.Equals(got, c.want))
		})
	}
}
