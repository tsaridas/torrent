package torrent

import (
	"testing"
	"time"

	"github.com/go-quicktest/qt"
)

func TestDhtAnnounceRetryBacksOffToAMinute(t *testing.T) {
	var got []time.Duration
	var d time.Duration
	for range 8 {
		d = dhtAnnounceRetry(d)
		got = append(got, d)
	}
	qt.Check(t, qt.DeepEquals(got, []time.Duration{
		time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second,
		16 * time.Second, 32 * time.Second, time.Minute, time.Minute,
	}))
}
