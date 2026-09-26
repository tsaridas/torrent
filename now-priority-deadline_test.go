package torrent

import (
	"testing"
	"time"
)

// A now-priority request parked behind a busy holder's queue is invisible to
// both rate-based overrides, because they judge the holder by its per-peer
// lastUsefulChunkReceived, which it keeps refreshing on other pieces. Only
// the deadline sees it.
func TestNowPriorityRequestOverdue(t *testing.T) {
	const deadline = 2 * time.Second
	now := time.Now()
	fresh := now.Add(-200 * time.Millisecond)
	for _, c := range []struct {
		name         string
		age          time.Duration
		deadline     time.Duration
		stealerLast  time.Time
		stealerQueue int64
		holderQueue  int64
		want         bool
	}{
		{"parked past deadline, stealer delivering, shallower queue", 3 * time.Second, deadline, fresh, 2, 40, true},
		{"within deadline", time.Second, deadline, fresh, 2, 40, false},
		{"exactly at deadline is not past it", deadline, deadline, fresh, 2, 40, false},
		// downloadRate is a lifetime average and stays > 0 forever after one
		// chunk. A peer that delivered once a minute ago and has been choked
		// since must not qualify; that is why the gate is recency.
		{"stealer delivered once, long ago", 3 * time.Second, deadline, now.Add(-time.Minute), 2, 40, false},
		{"stealer just past the recency window", 3 * time.Second, deadline, now.Add(-nowPriorityRecentDelivery - time.Millisecond), 2, 40, false},
		// The carousel guard: at connection time no peer has delivered.
		{"stealer never delivered", 3 * time.Second, deadline, time.Time{}, 0, 40, false},
		// Stands in for the bypassed "don't steal from the poor" check.
		{"stealer queue as deep as holder's", 3 * time.Second, deadline, fresh, 40, 40, false},
		{"stealer queue deeper than holder's", 3 * time.Second, deadline, fresh, 50, 40, false},
		{"disabled by zero deadline", time.Hour, 0, fresh, 2, 40, false},
		{"disabled by negative deadline", time.Hour, -1, fresh, 2, 40, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := nowPriorityRequestOverdue(c.age, c.deadline, c.stealerLast, now.Add(-10*time.Millisecond), now, c.stealerQueue, c.holderQueue)
			if got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

// The deadline must be reachable independently of the speed/stall overrides:
// a holder delivering steadily at a rate the stealer cannot beat by
// NowPriorityStealSpeedFactor is exactly the case that hung for 30s.
func TestDeadlineCoversWhatSpeedAndStallMiss(t *testing.T) {
	now := time.Now()
	busyHolder := now.Add(-10 * time.Millisecond) // refreshed on other pieces
	stealerLast := now.Add(-100 * time.Millisecond)
	stealerRate, existingRate := 1_000_000.0, 900_000.0

	if stealAllowedBySpeed(
		stealerRate, existingRate, stealerLast, busyHolder, now,
		DefaultNowPriorityStealSpeedFactor, DefaultNowPriorityStealStallThreshold,
	) {
		t.Fatal("precondition: speed/stall must NOT allow this steal")
	}
	if !nowPriorityRequestOverdue(30*time.Second, DefaultNowPriorityRequestDeadline, stealerLast, busyHolder, now, 4, 64) {
		t.Fatal("deadline must allow a request parked for 30s")
	}
}

func TestSetNowPriorityRequestDefaultsSetsDeadline(t *testing.T) {
	var cc ClientConfig
	cc.SetNowPriorityRequestDefaults()
	if cc.NowPriorityRequestDeadline != DefaultNowPriorityRequestDeadline {
		t.Fatalf("deadline = %v, want %v", cc.NowPriorityRequestDeadline, DefaultNowPriorityRequestDeadline)
	}
}
