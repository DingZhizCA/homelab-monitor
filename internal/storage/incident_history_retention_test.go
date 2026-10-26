/*
Package storage verifies explicit and bounded Incident history retention.
*/
package storage

import (
	"context"
	"testing"
	"time"
)

func TestIncidentHistoryRetentionRequiresExplicitPolicy(t *testing.T) {
	databases := openTestDatabases(t)
	valid := IncidentHistoryRetentionConfig{Interval: time.Hour, Retention: 24 * time.Hour, DeleteLimit: 2, Timeout: time.Second}
	if _, err := NewIncidentHistoryRetentionManager(nil, valid, func(IncidentHistoryRetentionObservation) {}); err == nil {
		t.Fatal("nil database was accepted")
	}
	if _, err := NewIncidentHistoryRetentionManager(databases.Core, IncidentHistoryRetentionConfig{}, func(IncidentHistoryRetentionObservation) {}); err == nil {
		t.Fatal("implicit policy was accepted")
	}
	if _, err := NewIncidentHistoryRetentionManager(databases.Core, valid, nil); err == nil {
		t.Fatal("missing observer was accepted")
	}
}

func TestIncidentHistoryRetentionDeletesOldestExpiredRowsWithinLimit(t *testing.T) {
	databases := openTestDatabases(t)
	now := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for revision, archivedAt := range []time.Time{now.Add(-72 * time.Hour), now.Add(-48 * time.Hour), now.Add(-time.Hour)} {
		if _, err := databases.Core.Exec(`INSERT INTO incident_revision_archives
			(device_id, rule_id, rule_revision, superseded_by_revision, superseded_at, archive_json)
			VALUES (?, ?, ?, ?, ?, '{}')`, "node-a", "high-cpu", revision+1, revision+2, storageTimestamp(archivedAt)); err != nil {
			t.Fatal(err)
		}
	}
	for id, occurredAt := range map[string]time.Time{"old-event": now.Add(-48 * time.Hour), "current-event": now.Add(-time.Hour)} {
		if _, err := databases.Core.Exec(`INSERT INTO incident_transitions
			(event_id, device_id, rule_id, rule_revision, occurred_at, from_status, to_status, transition_json)
			VALUES (?, 'node-a', 'high-cpu', 1, ?, 'inactive', 'firing', '{}')`, id, storageTimestamp(occurredAt)); err != nil {
			t.Fatal(err)
		}
	}
	var observed IncidentHistoryRetentionObservation
	manager, err := NewIncidentHistoryRetentionManager(databases.Core, IncidentHistoryRetentionConfig{
		Interval: time.Hour, Retention: 24 * time.Hour, DeleteLimit: 1, Timeout: time.Second,
	}, func(observation IncidentHistoryRetentionObservation) { observed = observation })
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now }
	result := manager.Cleanup(context.Background())
	if result.Err != nil || result.Deleted != 2 || result.DeletedArchives != 1 || result.DeletedTransitions != 1 || observed.Deleted != 2 || !result.Cutoff.Equal(now.Add(-24*time.Hour)) {
		t.Fatalf("cleanup result=%+v observed=%+v", result, observed)
	}
	var count int
	var oldestRevision uint64
	if err := databases.Core.QueryRow(`SELECT COUNT(*), MIN(rule_revision) FROM incident_revision_archives`).Scan(&count, &oldestRevision); err != nil || count != 2 || oldestRevision != 2 {
		t.Fatalf("remaining count=%d oldest_revision=%d err=%v", count, oldestRevision, err)
	}
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM incident_transitions`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("remaining transition count=%d err=%v", count, err)
	}
}

func TestIncidentHistoryRetentionRunStopsWithContext(t *testing.T) {
	databases := openTestDatabases(t)
	manager, err := NewIncidentHistoryRetentionManager(databases.Core, IncidentHistoryRetentionConfig{
		Interval: time.Millisecond, Retention: time.Hour, DeleteLimit: 1, Timeout: time.Second,
	}, func(IncidentHistoryRetentionObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { manager.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Incident history retention loop did not stop")
	}
}
