/*
Package auth tests bounded fixed-window login throttling.

The clock and all resource limits are supplied by the test rather than relying
on an implicit policy or wall-clock delay.
*/
package auth

import (
	"sync"
	"testing"
	"time"
)

func TestFixedWindowLimiterEnforcesAttemptsCapacityAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	limiter, err := NewFixedWindowLimiter(2, 2, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if limiter.Allow("") {
		t.Fatal("empty limiter key was accepted")
	}
	if !limiter.Allow("client-a") || !limiter.Allow("client-a") || limiter.Allow("client-a") {
		t.Fatal("per-key attempt limit was not enforced")
	}
	if !limiter.Allow("client-b") || limiter.Allow("client-c") {
		t.Fatal("bounded key capacity did not fail closed")
	}
	now = now.Add(time.Minute)
	if !limiter.Allow("client-c") {
		t.Fatal("expired entries did not free capacity")
	}
}

func TestFixedWindowLimiterIsSafeForConcurrentCallers(t *testing.T) {
	now := time.Now()
	limiter, err := NewFixedWindowLimiter(8, 1, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	var accepted int
	var mu sync.Mutex
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if limiter.Allow("same-client") {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}()
	}
	wait.Wait()
	if accepted != 1 {
		t.Fatalf("concurrent accepted attempts = %d, want 1", accepted)
	}
}
