package server

import (
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func TestStoreLiveness(t *testing.T) {
	base := time.Date(2026, 9, 2, 8, 0, 0, 0, time.UTC)
	now := base
	store := NewStore()
	store.now = func() time.Time { return now }
	store.Update(metrics.MetricEnvelope{SamplingIntervalSeconds: 5, Device: metrics.DeviceInfo{ID: "n1"}})
	for _, tc := range []struct {
		age  time.Duration
		want string
	}{{15 * time.Second, "online"}, {15*time.Second + time.Nanosecond, "stale"}, {60 * time.Second, "stale"}, {60*time.Second + time.Nanosecond, "offline"}} {
		now = base.Add(tc.age)
		got, ok := store.Get("n1")
		if !ok || got.Status != tc.want {
			t.Errorf("age %v: got (%v, %q), want status %q", tc.age, ok, got.Status, tc.want)
		}
	}
}

func TestStoreRestoreLatestPreservesReceiptTimeAndOrdering(t *testing.T) {
	base := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	store := NewStore()
	store.now = func() time.Time { return base.Add(2 * time.Minute) }
	newer := metrics.MetricEnvelope{
		Timestamp: base.Add(-time.Minute), SamplingIntervalSeconds: 5,
		Device: metrics.DeviceInfo{ID: "n1"},
	}
	state, restored := store.RestoreLatest(newer, base)
	if !restored || !state.ReceivedAt.Equal(base) || state.Status != "offline" {
		t.Fatalf("restored=%t state=%+v", restored, state)
	}
	older := newer
	older.Timestamp = newer.Timestamp.Add(-time.Minute)
	if state, restored = store.RestoreLatest(older, base.Add(time.Minute)); restored || !state.Snapshot.Timestamp.Equal(newer.Timestamp) {
		t.Fatalf("older restored=%t state=%+v", restored, state)
	}
	if _, restored = store.RestoreLatest(newer, time.Time{}); restored {
		t.Fatal("zero receipt time was restored")
	}
}
