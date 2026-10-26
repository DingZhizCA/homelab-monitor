/*
Package storage verifies explicit notification subscriptions, atomic audit,
revision CAS, active-user matching, and recipient capacity.
*/
package storage

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

func TestNotificationSubscriptionRepositoryPersistsCASAuditAndMatchesActiveUser(t *testing.T) {
	databases := openTestDatabases(t)
	userID := insertDashboardTestUser(t, databases, "notify-viewer", auth.RoleViewer)
	repository, err := NewNotificationSubscriptionRepository(databases.Core, 4, 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	subscription := notifications.Subscription{UserID: userID, DeviceID: "node-a", RuleID: "high-cpu", Severity: alerts.SeverityWarning, Revision: 1, Enabled: true, Channels: notifications.Channels{InApp: true, Push: true}}
	event := subscriptionAudit(t, subscription, time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC))
	if err := repository.SaveWithAudit(context.Background(), userID, subscription, 0, event); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveWithAudit(context.Background(), userID, subscription, 0, event); err != nil {
		t.Fatalf("idempotent save: %v", err)
	}
	listed, err := repository.ListForUser(context.Background(), userID, "node-a", 2)
	if err != nil || len(listed) != 1 || listed[0] != subscription {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	intent := storedNotificationIntent(t, subscription)
	recipients, err := repository.Recipients(context.Background(), intent, 2)
	if err != nil || len(recipients) != 1 || recipients[0].UserID != userID {
		t.Fatalf("recipients=%+v err=%v", recipients, err)
	}
	subscription.Revision = 2
	subscription.Channels.Push = false
	if err := repository.SaveWithAudit(context.Background(), userID, subscription, 1, subscriptionAudit(t, subscription, event.OccurredAt.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	var audits int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action = ?`, notifications.SubscriptionAuditAction).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit count=%d err=%v", audits, err)
	}
	if _, err := databases.Core.Exec(`UPDATE users SET disabled = 1 WHERE user_id = ?`, userID); err != nil {
		t.Fatal(err)
	}
	recipients, err = repository.Recipients(context.Background(), intent, 2)
	if err != nil || len(recipients) != 0 {
		t.Fatalf("disabled user recipients=%+v err=%v", recipients, err)
	}
}

func TestNotificationSubscriptionRepositoryEnforcesRevisionOwnershipAndCapacity(t *testing.T) {
	databases := openTestDatabases(t)
	firstUser := insertDashboardTestUser(t, databases, "notify-first", auth.RoleViewer)
	secondUser := insertDashboardTestUser(t, databases, "notify-second", auth.RoleOwner)
	repository, err := NewNotificationSubscriptionRepository(databases.Core, 2, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	first := notifications.Subscription{UserID: firstUser, DeviceID: "node-a", RuleID: "high-cpu", Severity: alerts.SeverityCritical, Revision: 1, Enabled: true, Channels: notifications.Channels{InApp: true}}
	if err := repository.SaveWithAudit(context.Background(), firstUser, first, 0, subscriptionAudit(t, first, time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	other := first
	other.RuleID = "high-memory"
	if err := repository.SaveWithAudit(context.Background(), firstUser, other, 0, subscriptionAudit(t, other, time.Now().UTC())); !errors.Is(err, notifications.ErrSubscriptionCapacity) {
		t.Fatalf("per-user capacity error=%v", err)
	}
	second := first
	second.UserID = secondUser
	if err := repository.SaveWithAudit(context.Background(), firstUser, second, 0, subscriptionAudit(t, second, time.Now().UTC())); !errors.Is(err, notifications.ErrInvalidSubscription) {
		t.Fatalf("ownership error=%v", err)
	}
	second.Revision = 2
	if err := repository.SaveWithAudit(context.Background(), secondUser, second, 0, subscriptionAudit(t, second, time.Now().UTC())); !errors.Is(err, notifications.ErrSubscriptionConflict) {
		t.Fatalf("revision error=%v", err)
	}
}

func subscriptionAudit(t *testing.T, subscription notifications.Subscription, at time.Time) audit.Event {
	t.Helper()
	event, err := audit.NewEvent(at, subscription.UserID, notifications.SubscriptionAuditAction, notifications.SubscriptionResourceType, notifications.SubscriptionResourceID(subscription))
	if err != nil {
		t.Fatal(err)
	}
	return event
}

func storedNotificationIntent(t *testing.T, subscription notifications.Subscription) notifications.Intent {
	t.Helper()
	pending := alerts.IncidentState{DeviceID: subscription.DeviceID, RuleID: subscription.RuleID, RuleRevision: 1, Status: alerts.IncidentPending, LastEvaluatedAt: time.Date(2026, 10, 26, 17, 59, 59, 0, time.UTC), LastEvaluation: alerts.StateTrue}
	pending.PendingSince = pending.LastEvaluatedAt
	firing := alerts.IncidentState{DeviceID: subscription.DeviceID, RuleID: subscription.RuleID, RuleRevision: 1, Status: alerts.IncidentFiring, LastEvaluatedAt: time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC), LastEvaluation: alerts.StateTrue}
	firing.FiringSince = firing.LastEvaluatedAt
	transition, changed, err := alerts.BuildIncidentTransition(pending, true, firing)
	if err != nil || !changed {
		t.Fatalf("transition=%+v changed=%t err=%v", transition, changed, err)
	}
	intent, planned, err := notifications.Plan(transition, subscription.Severity)
	if err != nil || !planned {
		t.Fatalf("intent=%+v planned=%t err=%v", intent, planned, err)
	}
	return intent
}
