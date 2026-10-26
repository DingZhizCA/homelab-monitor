/*
Package storage verifies durable Incident transition state, timestamp CAS,
idempotent retries, revision isolation, and explicit row capacity.
*/
package storage

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestIncidentRepositoryPersistsTransitionsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewIncidentRepository(databases.Core, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	rule := storedIncidentRule(1)
	policy := storedIncidentPolicy()
	first, err := repository.Advance(ctx, rule, storedIncidentResult(1, alerts.StateTrue), base, policy, time.Time{})
	if err != nil || first.Status != alerts.IncidentFiring {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	retry, err := repository.Advance(ctx, rule, storedIncidentResult(1, alerts.StateTrue), base, policy, time.Time{})
	if err != nil || retry != first {
		t.Fatalf("retry=%+v err=%v", retry, err)
	}
	if err := databases.Close(); err != nil {
		t.Fatal(err)
	}
	databases, err = Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	repository, err = NewIncidentRepository(databases.Core, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := repository.Advance(ctx, rule, storedIncidentResult(1, alerts.StateFalse), base.Add(time.Second), policy, base)
	if err != nil || resolved.Status != alerts.IncidentResolved {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	loaded, found, err := repository.Current(ctx, rule.DeviceID, rule.ID)
	if err != nil || !found || loaded != resolved {
		t.Fatalf("loaded=%+v found=%t err=%v", loaded, found, err)
	}
}

func TestIncidentRepositoryExplicitlyArchivesAndActivatesNewRuleRevision(t *testing.T) {
	databases := openTestDatabases(t)
	ownerID := insertDashboardTestUser(t, databases, "incident-owner", auth.RoleOwner)
	rules, err := NewAlertRuleRepository(databases.Core, dashboard.NewMetricRegistry(), alerts.Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	v1 := storedIncidentRule(1)
	if err := rules.SaveRevisionWithAudit(context.Background(), ownerID, v1, 0, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, v1)); err != nil {
		t.Fatal(err)
	}
	repository, err := NewIncidentRepository(databases.Core, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 26, 8, 0, 0, 0, time.UTC)
	if _, err := repository.Advance(context.Background(), v1, storedIncidentResult(1, alerts.StateTrue), at, storedIncidentPolicy(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	v2 := storedRule(2, 90)
	if err := rules.SaveRevisionWithAudit(context.Background(), ownerID, v2, 1, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, v2)); err != nil {
		t.Fatal(err)
	}
	event, err := audit.NewEvent(at.Add(time.Minute), ownerID, alerts.IncidentSupersedeAuditAction, alerts.IncidentAuditResourceType, alerts.IncidentAuditResourceID(v1.DeviceID, v1.ID))
	if err != nil {
		t.Fatal(err)
	}
	result, err := repository.ActivateRuleRevisionWithAudit(context.Background(), ownerID, v1.DeviceID, v1.ID, 2, event)
	if err != nil || !result.Ready || result.Archived == nil || result.Archived.Disposition != alerts.IncidentArchiveSuperseded || result.Archived.State.RuleRevision != 1 || result.Archived.SupersededByRevision != 2 {
		t.Fatalf("activation result=%+v err=%v", result, err)
	}
	if _, found, err := repository.Current(context.Background(), v1.DeviceID, v1.ID); err != nil || found {
		t.Fatalf("current after activation found=%t err=%v", found, err)
	}
	var archives, activationEvents int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM incident_revision_archives`).Scan(&archives); err != nil || archives != 1 {
		t.Fatalf("archives=%d err=%v", archives, err)
	}
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action = ?`, alerts.IncidentSupersedeAuditAction).Scan(&activationEvents); err != nil || activationEvents != 1 {
		t.Fatalf("activation events=%d err=%v", activationEvents, err)
	}
	listed, err := repository.ListArchives(context.Background(), v1.DeviceID, v1.ID, 1)
	if err != nil || len(listed) != 1 || listed[0].Disposition != alerts.IncidentArchiveSuperseded || listed[0].State.RuleRevision != 1 {
		t.Fatalf("listed archives=%+v err=%v", listed, err)
	}
	if _, err := repository.ListArchives(context.Background(), v1.DeviceID, v1.ID, 2); !errors.Is(err, alerts.ErrInvalidIncidentRequest) {
		t.Fatalf("over-limit archive query error=%v", err)
	}
	retry, err := repository.ActivateRuleRevisionWithAudit(context.Background(), ownerID, v1.DeviceID, v1.ID, 2, event)
	if err != nil || !retry.Ready || retry.Archived != nil {
		t.Fatalf("idempotent activation=%+v err=%v", retry, err)
	}
	if _, err := repository.Advance(context.Background(), v2, storedIncidentResult(2, alerts.StateTrue), at.Add(2*time.Minute), storedIncidentPolicy(), time.Time{}); err != nil {
		t.Fatalf("new revision did not start cleanly: %v", err)
	}
	otherV1 := storedRule(1, 70)
	otherV1.ID = "high-memory"
	if err := rules.SaveRevisionWithAudit(context.Background(), ownerID, otherV1, 0, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, otherV1)); err != nil {
		t.Fatal(err)
	}
	otherResult := alerts.EvaluationResult{RuleID: otherV1.ID, Revision: 1, State: alerts.StateTrue}
	if _, err := repository.Advance(context.Background(), otherV1, otherResult, at, storedIncidentPolicy(), time.Time{}); err != nil {
		t.Fatal(err)
	}
	otherV2 := otherV1
	otherV2.Revision = 2
	if err := rules.SaveRevisionWithAudit(context.Background(), ownerID, otherV2, 1, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, otherV2)); err != nil {
		t.Fatal(err)
	}
	otherEvent, err := audit.NewEvent(at.Add(3*time.Minute), ownerID, alerts.IncidentSupersedeAuditAction, alerts.IncidentAuditResourceType, alerts.IncidentAuditResourceID(otherV1.DeviceID, otherV1.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ActivateRuleRevisionWithAudit(context.Background(), ownerID, otherV1.DeviceID, otherV1.ID, 2, otherEvent); !errors.Is(err, alerts.ErrIncidentArchiveCapacity) {
		t.Fatalf("archive capacity error=%v", err)
	}
	if current, found, err := repository.Current(context.Background(), otherV1.DeviceID, otherV1.ID); err != nil || !found || current.RuleRevision != 1 {
		t.Fatalf("capacity failure changed current=%+v found=%t err=%v", current, found, err)
	}
}

func TestIncidentRepositoryEnforcesCASCapacityAndRevisionIsolation(t *testing.T) {
	databases := openTestDatabases(t)
	repository, err := NewIncidentRepository(databases.Core, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	rule := storedIncidentRule(1)
	policy := storedIncidentPolicy()
	if _, err := repository.Advance(context.Background(), rule, storedIncidentResult(1, alerts.StateTrue), base, policy, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Advance(context.Background(), rule, storedIncidentResult(1, alerts.StateFalse), base.Add(time.Second), policy, time.Time{}); !errors.Is(err, alerts.ErrIncidentVersionConflict) {
		t.Fatalf("CAS error=%v", err)
	}
	other := rule
	other.ID = "other"
	if _, err := repository.Advance(context.Background(), other, alerts.EvaluationResult{RuleID: other.ID, Revision: 1, State: alerts.StateTrue}, base, policy, time.Time{}); !errors.Is(err, alerts.ErrIncidentCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	newRevision := storedIncidentRule(2)
	if _, err := repository.Advance(context.Background(), newRevision, storedIncidentResult(2, alerts.StateTrue), base.Add(time.Second), policy, base); !errors.Is(err, alerts.ErrIncidentRevision) {
		t.Fatalf("revision error=%v", err)
	}
}

func TestIncidentRepositoryPersistsStatusChangesAtomicallyWithinCapacity(t *testing.T) {
	databases := openTestDatabases(t)
	repository, err := NewIncidentRepositoryWithTransitions(databases.Core, 1, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	rule := storedIncidentRule(1)
	policy := storedIncidentPolicy()
	firing, err := repository.Advance(context.Background(), rule, storedIncidentResult(1, alerts.StateTrue), base, policy, time.Time{})
	if err != nil || firing.Status != alerts.IncidentFiring {
		t.Fatalf("firing=%+v err=%v", firing, err)
	}
	held, err := repository.Advance(context.Background(), rule, storedIncidentResult(1, alerts.StateTrue), base.Add(time.Second), policy, base)
	if err != nil || held.Status != alerts.IncidentFiring {
		t.Fatalf("held=%+v err=%v", held, err)
	}
	if _, err := repository.Advance(context.Background(), rule, storedIncidentResult(1, alerts.StateFalse), base.Add(2*time.Second), policy, held.LastEvaluatedAt); !errors.Is(err, alerts.ErrIncidentTransitionCapacity) {
		t.Fatalf("transition capacity error=%v", err)
	}
	current, found, err := repository.Current(context.Background(), rule.DeviceID, rule.ID)
	if err != nil || !found || current != held {
		t.Fatalf("capacity failure changed current=%+v found=%t err=%v", current, found, err)
	}
	var count int
	var encoded []byte
	if err := databases.Core.QueryRow(`SELECT COUNT(*), transition_json FROM incident_transitions`).Scan(&count, &encoded); err != nil || count != 1 {
		t.Fatalf("transition count=%d err=%v", count, err)
	}
	var transition alerts.IncidentTransition
	if err := json.Unmarshal(encoded, &transition); err != nil || alerts.ValidateIncidentTransition(transition) != nil || transition.ToStatus != alerts.IncidentFiring {
		t.Fatalf("stored transition=%+v decode_err=%v", transition, err)
	}
	listed, err := repository.ListTransitions(context.Background(), rule.DeviceID, rule.ID, 1)
	if err != nil || len(listed) != 1 || listed[0].ID != transition.ID {
		t.Fatalf("listed transitions=%+v err=%v", listed, err)
	}
}

func TestIncidentRepositoryPersistsNotificationIntentAtomically(t *testing.T) {
	databases := openTestDatabases(t)
	repository, err := NewIncidentRepositoryWithNotificationOutbox(databases.Core, 1, 1, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 26, 14, 0, 0, 0, time.UTC)
	rule := storedIncidentRule(1)
	policy := storedIncidentPolicy()
	firing, err := repository.Advance(context.Background(), rule, storedIncidentResult(1, alerts.StateTrue), base, policy, time.Time{})
	if err != nil || firing.Status != alerts.IncidentFiring {
		t.Fatalf("firing=%+v err=%v", firing, err)
	}
	var encoded []byte
	if err := databases.Core.QueryRow(`SELECT intent_json FROM notification_intent_outbox`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	var intent notifications.Intent
	if err := json.Unmarshal(encoded, &intent); err != nil || notifications.ValidateIntent(intent) != nil || intent.Kind != notifications.IntentFiring || intent.Severity != rule.Severity {
		t.Fatalf("stored intent=%+v decode_err=%v", intent, err)
	}
	if _, err := repository.Advance(context.Background(), rule, storedIncidentResult(1, alerts.StateFalse), base.Add(time.Second), policy, base); !errors.Is(err, notifications.ErrIntentCapacity) {
		t.Fatalf("notification capacity error=%v", err)
	}
	current, found, err := repository.Current(context.Background(), rule.DeviceID, rule.ID)
	if err != nil || !found || current != firing {
		t.Fatalf("capacity failure changed current=%+v found=%t err=%v", current, found, err)
	}
	var transitions, intents int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM incident_transitions`).Scan(&transitions); err != nil || transitions != 1 {
		t.Fatalf("transition count=%d err=%v", transitions, err)
	}
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM notification_intent_outbox`).Scan(&intents); err != nil || intents != 1 {
		t.Fatalf("intent count=%d err=%v", intents, err)
	}
}

func storedIncidentRule(revision uint64) alerts.Rule {
	rule := storedRule(revision, 80)
	return rule
}

func storedIncidentResult(revision uint64, state alerts.State) alerts.EvaluationResult {
	return alerts.EvaluationResult{RuleID: "high-cpu", Revision: revision, State: state}
}

func storedIncidentPolicy() alerts.IncidentPolicy {
	zero := time.Duration(0)
	return alerts.IncidentPolicy{PendingFor: &zero, RecoveryFor: &zero, UnknownBehavior: alerts.UncertainHold, DisabledBehavior: alerts.UncertainHold}
}
