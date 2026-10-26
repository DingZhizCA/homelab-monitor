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
	outbox, err := NewNotificationOutboxRepository(databases.Core, 1, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Pending(context.Background(), base, 2); !errors.Is(err, ErrInvalidNotificationOutboxRequest) {
		t.Fatalf("over-limit read error=%v", err)
	}
	intents, err := outbox.Pending(context.Background(), base, 1)
	if err != nil || len(intents) != 1 || intents[0].Intent.Kind != notifications.IntentFiring {
		t.Fatalf("pending=%+v err=%v", intents, err)
	}
	if err := outbox.Acknowledge(context.Background(), intents[0].Intent.ID); err != nil {
		t.Fatal(err)
	}
	if err := outbox.Acknowledge(context.Background(), intents[0].Intent.ID); err != nil {
		t.Fatalf("idempotent acknowledgement: %v", err)
	}
	intents, err = outbox.Pending(context.Background(), base, 1)
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
	outbox, err := NewNotificationOutboxRepository(databases.Core, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := outbox.Pending(context.Background(), base, 1); err == nil {
		t.Fatal("corrupt notification intent was accepted")
	}
}

func TestNotificationOutboxRepositoryDefersAndDeadLettersAtomically(t *testing.T) {
	databases := openTestDatabases(t)
	incidents, err := NewIncidentRepositoryWithNotificationOutbox(databases.Core, 1, 1, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 10, 26, 19, 0, 0, 0, time.UTC)
	if _, err := incidents.Advance(context.Background(), storedIncidentRule(1), storedIncidentResult(1, alerts.StateTrue), createdAt, storedIncidentPolicy(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	outbox, err := NewNotificationOutboxRepository(databases.Core, 1, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	items, err := outbox.Pending(context.Background(), createdAt, 1)
	if err != nil || len(items) != 1 {
		t.Fatalf("initial pending=%+v err=%v", items, err)
	}
	deferredUntil := createdAt.Add(time.Minute)
	failure := notifications.Failure{IntentID: items[0].Intent.ID, AttemptCount: 1, FailedAt: createdAt, NextAttempt: deferredUntil, LastError: "sink unavailable"}
	if err := outbox.RecordFailure(context.Background(), failure); err != nil {
		t.Fatal(err)
	}
	items, err = outbox.Pending(context.Background(), deferredUntil.Add(-time.Nanosecond), 1)
	if err != nil || len(items) != 0 {
		t.Fatalf("early pending=%+v err=%v", items, err)
	}
	items, err = outbox.Pending(context.Background(), deferredUntil, 1)
	if err != nil || len(items) != 1 || items[0].AttemptCount != 1 {
		t.Fatalf("ready pending=%+v err=%v", items, err)
	}
	terminal := notifications.Failure{IntentID: items[0].Intent.ID, AttemptCount: 2, FailedAt: deferredUntil, LastError: "still unavailable", DeadLetter: true}
	if err := outbox.RecordFailure(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	items, err = outbox.Pending(context.Background(), deferredUntil, 1)
	if err != nil || len(items) != 0 {
		t.Fatalf("pending after dead letter=%+v err=%v", items, err)
	}
	var attempts int
	var lastError string
	if err := databases.Core.QueryRow(`SELECT attempt_count, last_error FROM notification_intent_dead_letters WHERE intent_id = ?`, terminal.IntentID).Scan(&attempts, &lastError); err != nil || attempts != 2 || lastError != terminal.LastError {
		t.Fatalf("dead letter attempts=%d error=%q query=%v", attempts, lastError, err)
	}
	letters, err := outbox.ListDeadLetters(context.Background(), 1)
	if err != nil || len(letters) != 1 || letters[0].Intent.ID != terminal.IntentID || letters[0].AttemptCount != 2 {
		t.Fatalf("dead letters=%+v err=%v", letters, err)
	}
	if err := outbox.RequeueDeadLetter(context.Background(), terminal.IntentID); err != nil {
		t.Fatal(err)
	}
	items, err = outbox.Pending(context.Background(), deferredUntil, 1)
	if err != nil || len(items) != 1 || items[0].AttemptCount != 0 {
		t.Fatalf("requeued pending=%+v err=%v", items, err)
	}
	terminal = notifications.Failure{IntentID: items[0].Intent.ID, AttemptCount: 1, FailedAt: deferredUntil.Add(time.Minute), LastError: "terminal again", DeadLetter: true}
	if err := outbox.RecordFailure(context.Background(), terminal); err != nil {
		t.Fatal(err)
	}
	deleted, err := outbox.DeleteDeadLettersBefore(context.Background(), terminal.FailedAt.Add(time.Nanosecond), 1)
	if err != nil || deleted != 1 {
		t.Fatalf("deleted=%d err=%v", deleted, err)
	}
	letters, err = outbox.ListDeadLetters(context.Background(), 1)
	if err != nil || len(letters) != 0 {
		t.Fatalf("dead letters after retention=%+v err=%v", letters, err)
	}
}
