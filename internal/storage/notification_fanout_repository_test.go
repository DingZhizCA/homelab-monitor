/*
Package storage verifies atomic, bounded and idempotent materialization of
per-user notification records without contacting a Push service.
*/
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

func TestNotificationFanoutRepositoryMaterializesReplayIdempotently(t *testing.T) {
	databases := openTestDatabases(t)
	first := insertDashboardTestUser(t, databases, "recipient-first", auth.RoleOwner)
	second := insertDashboardTestUser(t, databases, "recipient-second", auth.RoleViewer)
	repository, err := NewNotificationFanoutRepository(databases.Core, 2)
	if err != nil {
		t.Fatal(err)
	}
	intent := fanoutTestIntent(t)
	recipients := []notifications.Recipient{
		{UserID: first, Channels: notifications.Channels{InApp: true}},
		{UserID: second, Channels: notifications.Channels{Push: true}},
	}
	if err := repository.Materialize(context.Background(), intent, recipients); err != nil {
		t.Fatal(err)
	}
	if err := repository.Materialize(context.Background(), intent, recipients); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	var count, inApp, push int
	if err := databases.Core.QueryRow(`SELECT COUNT(*), SUM(in_app), SUM(push_enabled) FROM recipient_notifications`).Scan(&count, &inApp, &push); err != nil || count != 2 || inApp != 1 || push != 1 {
		t.Fatalf("count=%d in_app=%d push=%d err=%v", count, inApp, push, err)
	}
}

func TestNotificationFanoutRepositoryRollsBackWholeFanoutAtCapacity(t *testing.T) {
	databases := openTestDatabases(t)
	first := insertDashboardTestUser(t, databases, "capacity-first", auth.RoleOwner)
	second := insertDashboardTestUser(t, databases, "capacity-second", auth.RoleViewer)
	repository, err := NewNotificationFanoutRepository(databases.Core, 1)
	if err != nil {
		t.Fatal(err)
	}
	intent := fanoutTestIntent(t)
	err = repository.Materialize(context.Background(), intent, []notifications.Recipient{
		{UserID: first, Channels: notifications.Channels{InApp: true}},
		{UserID: second, Channels: notifications.Channels{InApp: true}},
	})
	if !errors.Is(err, notifications.ErrNotificationCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	var count int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM recipient_notifications`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("partial fanout count=%d err=%v", count, err)
	}
}

func fanoutTestIntent(t *testing.T) notifications.Intent {
	t.Helper()
	base := time.Date(2026, 10, 26, 20, 0, 0, 0, time.UTC)
	pending := alerts.IncidentState{DeviceID: "node-a", RuleID: "high-cpu", RuleRevision: 1, Status: alerts.IncidentPending, PendingSince: base.Add(-time.Second), LastEvaluatedAt: base.Add(-time.Second), LastEvaluation: alerts.StateTrue}
	firing := alerts.IncidentState{DeviceID: "node-a", RuleID: "high-cpu", RuleRevision: 1, Status: alerts.IncidentFiring, FiringSince: base, LastEvaluatedAt: base, LastEvaluation: alerts.StateTrue}
	transition, changed, err := alerts.BuildIncidentTransition(pending, true, firing)
	if err != nil || !changed {
		t.Fatalf("transition=%+v changed=%t err=%v", transition, changed, err)
	}
	intent, planned, err := notifications.Plan(transition, alerts.SeverityWarning)
	if err != nil || !planned {
		t.Fatalf("intent=%+v planned=%t err=%v", intent, planned, err)
	}
	return intent
}
