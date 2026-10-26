/*
Package storage verifies bounded, validated and idempotent notification outbox
consumption without starting a dispatcher or external delivery service.
*/
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

func TestNotificationOutboxRepositoryReadsAndAcknowledgesOldestIntent(t *testing.T) {
	databases := openTestDatabases(t)
	incidents, err := NewIncidentRepositoryWithNotificationOutbox(databases.Core, 1, 1, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	rule := storedIncidentRule(1)
	if _, err := incidents.Advance(context.Background(), rule, storedIncidentResult(1, alerts.StateTrue), base, storedIncidentPolicy(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	outbox, err := NewNotificationOutboxRepository(databases.Core, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Pending(context.Background(), 2); !errors.Is(err, ErrInvalidNotificationOutboxRequest) {
		t.Fatalf("over-limit read error=%v", err)
	}
	intents, err := outbox.Pending(context.Background(), 1)
	if err != nil || len(intents) != 1 || intents[0].Kind != notifications.IntentFiring {
		t.Fatalf("pending=%+v err=%v", intents, err)
	}
	if err := outbox.Acknowledge(context.Background(), intents[0].ID); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Acknowledge(context.Background(), intents[0].ID); err != nil {
		t.Fatalf("idempotent acknowledgement: %v", err)
	}
	intents, err = outbox.Pending(context.Background(), 1)
	if err != nil || len(intents) != 0 {
		t.Fatalf("pending after acknowledgement=%+v err=%v", intents, err)
	}
	if err := outbox.Acknowledge(context.Background(), "bad"); !errors.Is(err, ErrInvalidNotificationOutboxRequest) {
		t.Fatalf("invalid acknowledgement error=%v", err)
	}
}

func TestNotificationOutboxRepositoryRejectsCorruptStoredIdentity(t *testing.T) {
	databases := openTestDatabases(t)
	incidents, err := NewIncidentRepositoryWithNotificationOutbox(databases.Core, 1, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 26, 15, 0, 0, 0, time.UTC)
	rule := storedIncidentRule(1)
	if _, err := incidents.Advance(context.Background(), rule, storedIncidentResult(1, alerts.StateTrue), base, storedIncidentPolicy(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := databases.Core.Exec(`UPDATE notification_intent_outbox SET rule_id = 'other'`); err != nil {
		t.Fatal(err)
	}
	outbox, err := NewNotificationOutboxRepository(databases.Core, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Pending(context.Background(), 1); err == nil {
		t.Fatal("corrupt notification intent was accepted")
	}
}
