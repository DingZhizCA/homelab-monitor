package server

import (
	"testing"
	"time"
)

func TestPresenceGracePeriod(t *testing.T) {
	base := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	now := base
	p := NewPresence()
	p.now = func() time.Time { return now }
	if p.Active() {
		t.Fatal("presence is active before first heartbeat")
	}
	expires := p.Touch()
	if !expires.Equal(base.Add(30 * time.Minute)) {
		t.Fatalf("expires = %v", expires)
	}
	now = base.Add(30 * time.Minute)
	if !p.Active() {
		t.Fatal("presence expired before grace-period boundary")
	}
	now = now.Add(time.Nanosecond)
	if p.Active() {
		t.Fatal("presence remained active after grace period")
	}
}
