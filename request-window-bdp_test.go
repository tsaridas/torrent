package torrent

import (
	"testing"
	"time"
)

func TestRequestWindowBDPUsesUsefulDeliveryAndRemoteLimit(t *testing.T) {
	t0 := time.Now()
	var estimator requestWindowBDP
	estimator.sample(t0, 40*time.Millisecond, 1<<14)
	if n := estimator.target(1<<14, 50, 256); n != 0 {
		t.Fatalf("one sample should keep the existing request policy, got %d", n)
	}
	const bytes = 8 << 20 // 40 MiB/s over 200 ms
	estimator.sample(t0.Add(199*time.Millisecond), 240*time.Millisecond, bytes-(1<<14))
	estimator.sample(t0.Add(200*time.Millisecond), 50*time.Millisecond, 1<<14)
	if n := estimator.target(1<<14, 50, 256); n < 200 || n > 210 {
		t.Fatalf("expected ~205 blocks with 40 ms base delay, got %d", n)
	}
	if n := estimator.target(1<<14, 50, 64); n != 64 {
		t.Fatalf("peer limit = 64, got %d", n)
	}
	if n := estimator.target(1<<14, 250, 256); n != 250 {
		t.Fatalf("floor = 250, got %d", n)
	}
	estimator.sample(t0.Add(5*time.Second), 40*time.Millisecond, 1<<14)
	if n := estimator.target(1<<14, 50, 256); n != 0 {
		t.Fatalf("stale capacity estimate carried through idle, got %d", n)
	}
}
