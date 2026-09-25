package torrent

import (
	"testing"
	"time"
)

// A now-priority request parked behind a busy holder's queue is invisible to
// both rate-based overrides, because lastUsefulChunkReceived is per-peer and
// the holder keeps refreshing it on other pieces. Only the deadline sees it.
func TestNowPriorityRequestOverdue(t *testing.T) {
	const deadline = 2 * time.Second
	for _, c := range []struct {
		name        string
		age         time.Duration
		deadline    time.Duration
		stealerRate float64
		want        bool
	}{
		{"parked past deadline, stealer delivering", 3 * time.Second, deadline, 500000, true},
		{"within deadline", time.Second, deadline, 500000, false},
		{"exactly at deadline is not past it", deadline, deadline, 500000, false},
		// The carousel guard: at connection time every peer is silent, and
		// passing a block between silent peers makes no progress.
		{"stealer silent", 3 * time.Second, deadline, 0, false},
		{"disabled by zero deadline", time.Hour, 0, 500000, false},
		{"disabled by negative deadline", time.Hour, -1, 500000, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := nowPriorityRequestOverdue(c.age, c.deadline, c.stealerRate); got != c.want {
				t.Fatalf("nowPriorityRequestOverdue(%v, %v, %v) = %v, want %v",
					c.age, c.deadline, c.stealerRate, got, c.want)
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
	stealerRate, existingRate := 1_000_000.0, 900_000.0

	if stealAllowedBySpeed(
		stealerRate, existingRate, now, busyHolder, now,
		DefaultNowPriorityStealSpeedFactor, DefaultNowPriorityStealStallThreshold,
	) {
		t.Fatal("precondition: speed/stall must NOT allow this steal")
	}
	if !nowPriorityRequestOverdue(30*time.Second, DefaultNowPriorityRequestDeadline, stealerRate) {
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
