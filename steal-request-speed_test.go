package torrent

import (
	"testing"
	"time"

	qt "github.com/go-quicktest/qt"
)

// TestStealAllowedBySpeed exercises the speed-based override that lets a
// faster (or the existing holder having gone silent) peer steal a
// PiecePriorityNow piece even when the count-based "don't steal from the
// poor" check would otherwise leave it in place. This is the Go-server-side
// analogue of the reference torrent-stream engine's otherSpeed < speed
// stealing decision, scoped to pieces needed for immediate HLS playback so
// steady-state background fetching keeps the original fairness behaviour.
func TestStealAllowedBySpeed(t *testing.T) {
	t0 := time.Unix(1000, 0)
	for _, c := range []struct {
		name         string
		stealerRate  float64
		existingRate float64
		existingLast time.Time
		now          time.Time
		want         bool
	}{
		{
			name:         "similar speed, recently useful holder is not stolen from",
			stealerRate:  100,
			existingRate: 100,
			existingLast: t0,
			now:          t0.Add(time.Second),
			want:         false,
		},
		{
			name:         "meaningfully faster stealer steals",
			stealerRate:  200,
			existingRate: 100,
			existingLast: t0,
			now:          t0.Add(time.Second),
			want:         true,
		},
		{
			name:         "only marginally faster stealer does not steal",
			stealerRate:  110,
			existingRate: 100,
			existingLast: t0,
			now:          t0.Add(time.Second),
			want:         false,
		},
		{
			name:         "silent-so-far holder (zero last-useful) is stealable regardless of rate",
			stealerRate:  10,
			existingRate: 10,
			existingLast: time.Time{},
			now:          t0,
			want:         true,
		},
		{
			name:         "holder gone quiet past the stall threshold is stealable even if it was once fast",
			stealerRate:  10,
			existingRate: 1000,
			existingLast: t0,
			now:          t0.Add(stealSpeedStallThreshold + time.Millisecond),
			want:         true,
		},
		{
			name:         "holder still within the stall threshold is not stolen from on rate alone",
			stealerRate:  10,
			existingRate: 1000,
			existingLast: t0,
			now:          t0.Add(stealSpeedStallThreshold - time.Millisecond),
			want:         false,
		},
		{
			name:         "zero-rate stealer never counts as faster",
			stealerRate:  0,
			existingRate: 0,
			existingLast: t0,
			now:          t0.Add(time.Millisecond),
			want:         false,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := stealAllowedBySpeed(c.stealerRate, c.existingRate, c.existingLast, c.now)
			qt.Check(t, qt.Equals(got, c.want))
		})
	}
}
