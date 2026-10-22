/*
Package alerts tests explicit incident transition policies without persistence
or notification side effects.
*/
package alerts

import (
	"errors"
	"testing"
	"time"
)

func TestAdvanceIncidentUsesCallerSuppliedPendingAndRecoveryWindows(t *testing.T) {
	base := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	rule := incidentTestRule(true, 1)
	policy := IncidentPolicy{PendingFor: incidentDuration(10 * time.Second), RecoveryFor: incidentDuration(5 * time.Second), UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold}
	state := IncidentState{}

	state = advanceTestIncident(t, state, rule, StateTrue, base, policy)
	if state.Status != IncidentPending || !state.PendingSince.Equal(base) {
		t.Fatalf("first true result=%+v", state)
	}
	state = advanceTestIncident(t, state, rule, StateTrue, base.Add(9*time.Second), policy)
	if state.Status != IncidentPending {
		t.Fatalf("incident fired before pending window elapsed: %+v", state)
	}
	state = advanceTestIncident(t, state, rule, StateTrue, base.Add(10*time.Second), policy)
	if state.Status != IncidentFiring || !state.FiringSince.Equal(base.Add(10*time.Second)) {
		t.Fatalf("incident did not fire at configured boundary: %+v", state)
	}
	state = advanceTestIncident(t, state, rule, StateFalse, base.Add(11*time.Second), policy)
	if state.Status != IncidentRecovering || !state.RecoveringSince.Equal(base.Add(11*time.Second)) {
		t.Fatalf("false result did not enter recovery: %+v", state)
	}
	state = advanceTestIncident(t, state, rule, StateTrue, base.Add(12*time.Second), policy)
	if state.Status != IncidentFiring || !state.RecoveringSince.IsZero() {
		t.Fatalf("true result did not cancel recovery: %+v", state)
	}
	state = advanceTestIncident(t, state, rule, StateFalse, base.Add(13*time.Second), policy)
	state = advanceTestIncident(t, state, rule, StateFalse, base.Add(17*time.Second), policy)
	if state.Status != IncidentRecovering {
		t.Fatalf("incident resolved before recovery window elapsed: %+v", state)
	}
	state = advanceTestIncident(t, state, rule, StateFalse, base.Add(18*time.Second), policy)
	if state.Status != IncidentResolved || !state.ResolvedAt.Equal(base.Add(18*time.Second)) {
		t.Fatalf("incident did not resolve at configured boundary: %+v", state)
	}
}

func TestAdvanceIncidentMakesUnknownBehaviorExplicit(t *testing.T) {
	base := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	rule := incidentTestRule(true, 1)
	policy := IncidentPolicy{PendingFor: incidentDuration(10 * time.Second), RecoveryFor: incidentDuration(time.Minute), UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold}
	state := advanceTestIncident(t, IncidentState{}, rule, StateTrue, base, policy)
	state = advanceTestIncident(t, state, rule, StateUnknown, base.Add(5*time.Second), policy)
	if state.Status != IncidentPending || !state.PendingSince.Equal(base) {
		t.Fatalf("hold behavior changed pending state: %+v", state)
	}
	state = advanceTestIncident(t, state, rule, StateTrue, base.Add(10*time.Second), policy)
	if state.Status != IncidentFiring {
		t.Fatalf("hold behavior did not count elapsed wall time: %+v", state)
	}

	policy.UnknownBehavior = UncertainResetPending
	state = advanceTestIncident(t, IncidentState{}, rule, StateTrue, base, policy)
	state = advanceTestIncident(t, state, rule, StateUnknown, base.Add(time.Second), policy)
	if state.Status != IncidentInactive {
		t.Fatalf("reset behavior retained pending incident: %+v", state)
	}
	state = advanceTestIncident(t, state, rule, StateTrue, base.Add(2*time.Second), policy)
	state = advanceTestIncident(t, state, rule, StateTrue, base.Add(12*time.Second), policy)
	state = advanceTestIncident(t, state, rule, StateUnknown, base.Add(13*time.Second), policy)
	if state.Status != IncidentFiring {
		t.Fatalf("reset behavior unexpectedly cleared firing incident: %+v", state)
	}
	state = advanceTestIncident(t, state, rule, StateFalse, base.Add(14*time.Second), policy)
	state = advanceTestIncident(t, state, rule, StateUnknown, base.Add(15*time.Second), policy)
	if state.Status != IncidentFiring {
		t.Fatalf("reset behavior did not cancel provisional recovery: %+v", state)
	}

	policy.UnknownBehavior = UncertainResolve
	state = advanceTestIncident(t, IncidentState{}, rule, StateTrue, base, policy)
	state = advanceTestIncident(t, state, rule, StateUnknown, base.Add(time.Second), policy)
	if state.Status != IncidentResolved || !state.ResolvedAt.Equal(base.Add(time.Second)) {
		t.Fatalf("resolve behavior was not applied explicitly: %+v", state)
	}
}

func TestAdvanceIncidentAppliesDisabledPolicySeparately(t *testing.T) {
	base := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	enabledRule := incidentTestRule(true, 1)
	policy := IncidentPolicy{PendingFor: incidentDuration(time.Minute), RecoveryFor: incidentDuration(time.Minute), UnknownBehavior: UncertainHold, DisabledBehavior: UncertainResetPending}
	state := advanceTestIncident(t, IncidentState{}, enabledRule, StateTrue, base, policy)
	disabledRule := incidentTestRule(false, 1)
	state = advanceTestIncident(t, state, disabledRule, StateDisabled, base.Add(time.Second), policy)
	if state.Status != IncidentInactive || state.LastEvaluation != StateDisabled {
		t.Fatalf("disabled behavior did not independently reset pending state: %+v", state)
	}
}

func TestAdvanceIncidentRejectsImplicitRevisionAndTimeChanges(t *testing.T) {
	base := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	rule := incidentTestRule(true, 1)
	policy := IncidentPolicy{PendingFor: incidentDuration(0), RecoveryFor: incidentDuration(0), UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold}
	state := advanceTestIncident(t, IncidentState{}, rule, StateTrue, base, policy)
	if _, err := AdvanceIncident(state, incidentTestRule(true, 2), incidentEvaluation(2, StateTrue), base.Add(time.Second), policy); !errors.Is(err, ErrIncidentRevision) {
		t.Fatalf("revision change error=%v", err)
	}
	if _, err := AdvanceIncident(state, rule, incidentEvaluation(1, StateTrue), base.Add(-time.Second), policy); !errors.Is(err, ErrIncidentTime) {
		t.Fatalf("out-of-order evaluation error=%v", err)
	}
	if _, err := AdvanceIncident(IncidentState{}, incidentTestRule(false, 1), incidentEvaluation(1, StateTrue), base, policy); !errors.Is(err, ErrIncidentIdentity) {
		t.Fatalf("enabled/disabled result mismatch error=%v", err)
	}
}

func TestAdvanceIncidentRequiresCompletePolicy(t *testing.T) {
	base := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	rule := incidentTestRule(true, 1)
	for _, policy := range []IncidentPolicy{
		{PendingFor: incidentDuration(-time.Second), RecoveryFor: incidentDuration(0), UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold},
		{PendingFor: incidentDuration(0), UnknownBehavior: "", DisabledBehavior: UncertainHold},
		{PendingFor: incidentDuration(0), RecoveryFor: incidentDuration(0), UnknownBehavior: UncertainHold, DisabledBehavior: "invalid"},
		{RecoveryFor: incidentDuration(0), UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold},
	} {
		if _, err := AdvanceIncident(IncidentState{}, rule, incidentEvaluation(1, StateTrue), base, policy); !errors.Is(err, ErrIncidentPolicy) {
			t.Fatalf("invalid policy %+v error=%v", policy, err)
		}
	}
}

func advanceTestIncident(t *testing.T, state IncidentState, rule Rule, result State, at time.Time, policy IncidentPolicy) IncidentState {
	t.Helper()
	next, err := AdvanceIncident(state, rule, incidentEvaluation(rule.Revision, result), at, policy)
	if err != nil {
		t.Fatalf("advance incident: %v", err)
	}
	return next
}

func incidentEvaluation(revision uint64, state State) EvaluationResult {
	return EvaluationResult{RuleID: "high-cpu", Revision: revision, State: state}
}

func incidentTestRule(enabled bool, revision uint64) Rule {
	return Rule{ID: "high-cpu", DeviceID: "node-a", Revision: revision, Enabled: enabled}
}

func incidentDuration(value time.Duration) *time.Duration { return &value }
