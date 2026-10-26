/*
Package notifications verifies deterministic planning without delivery effects.
*/
package notifications

import (
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
)

func TestPlanCreatesDeduplicatedFiringAndRecoveryIntents(t *testing.T) {
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	pending := transitionState(alerts.IncidentPending, base.Add(-time.Second))
	pending.PendingSince = base.Add(-time.Second)
	firing := transitionState(alerts.IncidentFiring, base)
	firing.FiringSince = base
	firingEvent, changed, err := alerts.BuildIncidentTransition(pending, true, firing)
	if err != nil || !changed {
		t.Fatalf("firing event=%+v changed=%t err=%v", firingEvent, changed, err)
	}
	intent, planned, err := Plan(firingEvent, alerts.SeverityWarning)
	if err != nil || !planned || intent.Kind != IntentFiring || intent.DeliveryPolicy != DeliveryPushEligible || ValidateIntent(intent) != nil {
		t.Fatalf("firing intent=%+v planned=%t err=%v", intent, planned, err)
	}
	retry, _, _ := Plan(firingEvent, alerts.SeverityWarning)
	if retry.ID != intent.ID {
		t.Fatalf("intent IDs differ: %q != %q", retry.ID, intent.ID)
	}
	resolved := transitionState(alerts.IncidentResolved, base.Add(time.Minute))
	resolved.FiringSince = base
	resolved.ResolvedAt = base.Add(time.Minute)
	recoveryEvent, _, err := alerts.BuildIncidentTransition(firing, true, resolved)
	if err != nil {
		t.Fatal(err)
	}
	recovery, planned, err := Plan(recoveryEvent, alerts.SeverityInfo)
	if err != nil || !planned || recovery.Kind != IntentRecovery || recovery.DeliveryPolicy != DeliveryRecordOnly {
		t.Fatalf("recovery intent=%+v planned=%t err=%v", recovery, planned, err)
	}
}

func TestPlanIgnoresIntermediateAndCanceledRecoveryChanges(t *testing.T) {
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	for _, fixture := range []struct {
		from alerts.IncidentState
		to   alerts.IncidentState
	}{
		{transitionState(alerts.IncidentInactive, base.Add(-time.Second)), func() alerts.IncidentState {
			state := transitionState(alerts.IncidentPending, base)
			state.PendingSince = base
			return state
		}()},
		{func() alerts.IncidentState {
			state := transitionState(alerts.IncidentRecovering, base)
			state.FiringSince = base.Add(-time.Minute)
			state.RecoveringSince = base
			return state
		}(), func() alerts.IncidentState {
			state := transitionState(alerts.IncidentFiring, base.Add(time.Second))
			state.FiringSince = base.Add(-time.Minute)
			return state
		}()},
	} {
		event, changed, err := alerts.BuildIncidentTransition(fixture.from, true, fixture.to)
		if err != nil || !changed {
			t.Fatalf("event=%+v changed=%t err=%v", event, changed, err)
		}
		if intent, planned, err := Plan(event, alerts.SeverityCritical); err != nil || planned || intent != (Intent{}) {
			t.Fatalf("intent=%+v planned=%t err=%v", intent, planned, err)
		}
	}
}

func TestPlanRejectsInvalidSeverity(t *testing.T) {
	base := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	pending := transitionState(alerts.IncidentPending, base.Add(-time.Second))
	pending.PendingSince = base.Add(-time.Second)
	firing := transitionState(alerts.IncidentFiring, base)
	firing.FiringSince = base
	event, changed, err := alerts.BuildIncidentTransition(pending, true, firing)
	if err != nil || !changed {
		t.Fatalf("event=%+v changed=%t err=%v", event, changed, err)
	}
	if intent, planned, err := Plan(event, alerts.Severity("unknown")); err == nil || planned || intent != (Intent{}) {
		t.Fatalf("intent=%+v planned=%t err=%v", intent, planned, err)
	}
}

func transitionState(status alerts.IncidentStatus, at time.Time) alerts.IncidentState {
	return alerts.IncidentState{DeviceID: "node-a", RuleID: "high-cpu", RuleRevision: 1, Status: status, LastEvaluatedAt: at, LastEvaluation: alerts.StateTrue}
}
