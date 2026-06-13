// Bandwidth throttling for clipboard transfers. The cap is expressed as a
// percentage of a declared session bandwidth budget (clipboardpolicy.Policy):
// SessionMbps * BandwidthPct. A self-contained token bucket keeps the weave
// example free of an extra module dependency.
//go:build darwin

package clipboard

import (
	"context"
	"sync"
	"time"
)

// limiter is a byte-budget token bucket. A nil *limiter (capacity == 0 →
// unlimited) means "no throttling"; all methods are nil-safe so callers need no
// branch.
type limiter struct {
	mu         sync.Mutex
	ratePerSec float64 // bytes per second; 0 → unlimited
	burst      float64 // max accumulated tokens (bytes)
	tokens     float64
	last       time.Time
}

// newLimiter builds a limiter for the given bytes-per-second budget. A budget
// of 0 returns nil (unlimited). The burst is one second of budget, floored so a
// single oversized item can still pass in one refill window.
func newLimiter(bytesPerSec int64) *limiter {
	if bytesPerSec <= 0 {
		return nil
	}
	rate := float64(bytesPerSec)
	return &limiter{
		ratePerSec: rate,
		burst:      rate, // 1s of budget
		tokens:     rate,
		last:       time.Now(),
	}
}

// waitN blocks until n bytes of budget are available or ctx is done. It is
// nil-safe: a nil limiter returns immediately. Requests larger than the burst
// drain the bucket and proceed (so a big file is never permanently blocked); a
// transfer can momentarily exceed the instantaneous burst but averages to the
// configured rate.
func (l *limiter) waitN(ctx context.Context, n int) error {
	if l == nil || n <= 0 {
		return nil
	}
	want := float64(n)
	for {
		l.mu.Lock()
		now := time.Now()
		l.tokens += now.Sub(l.last).Seconds() * l.ratePerSec
		l.last = now
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		// Allow draining below zero for oversized requests so they can't wedge.
		if l.tokens >= want || want > l.burst {
			l.tokens -= want
			l.mu.Unlock()
			return nil
		}
		deficit := want - l.tokens
		wait := time.Duration(deficit / l.ratePerSec * float64(time.Second))
		l.mu.Unlock()

		if wait <= 0 {
			wait = time.Millisecond
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
