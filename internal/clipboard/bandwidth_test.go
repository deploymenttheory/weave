//go:build darwin

package clipboard

import (
	"context"
	"testing"
	"time"
)

func TestLimiterNilUnlimited(t *testing.T) {
	var l *limiter // newLimiter(0) result
	if got := newLimiter(0); got != nil {
		t.Fatalf("newLimiter(0) should be nil, got %v", got)
	}
	if err := l.waitN(context.Background(), 1_000_000); err != nil {
		t.Fatalf("nil limiter waitN should be instant: %v", err)
	}
}

func TestLimiterThrottles(t *testing.T) {
	// 1000 bytes/sec budget, burst 1000. Draining the burst then asking for
	// another 1000 bytes should take ~1s.
	l := newLimiter(1000)
	ctx := context.Background()
	if err := l.waitN(ctx, 1000); err != nil { // drains the burst instantly
		t.Fatal(err)
	}
	start := time.Now()
	if err := l.waitN(ctx, 1000); err != nil { // must wait ~1s to refill
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if elapsed < 700*time.Millisecond {
		t.Errorf("expected throttle ~1s, got %v", elapsed)
	}
}

func TestLimiterContextCancel(t *testing.T) {
	l := newLimiter(1000)                   // burst 1000
	_ = l.waitN(context.Background(), 1000) // drain the burst
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	// A sub-burst request that needs ~0.8s of refill must observe the deadline.
	if err := l.waitN(ctx, 800); err == nil {
		t.Error("expected context deadline error")
	}
}

func TestLimiterOversizedRequestProceeds(t *testing.T) {
	// A request larger than the burst must not wedge forever.
	l := newLimiter(1000) // burst 1000
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := l.waitN(ctx, 5000); err != nil {
		t.Errorf("oversized request should proceed, got %v", err)
	}
}
