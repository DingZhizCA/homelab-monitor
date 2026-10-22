/*
Package alerts tests bounded Incident storage, CAS, and retry behavior.
*/
package alerts

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestMemoryIncidentRepositoryAdvancesWithCASAndIdempotentRetry(t *testing.T) {
	repository, err := NewMemoryIncidentRepository(2)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	rule := incidentTestRule(true, 1)
	policy := IncidentPolicy{PendingFor: incidentDuration(0), RecoveryFor: incidentDuration(0), UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold}
	result := incidentEvaluation(1, StateTrue)
	first, err := repository.Advance(ctx, rule, result, base, policy, time.Time{})
	if err != nil || first.Status != IncidentFiring {
		t.Fatalf("first advance=%+v err=%v", first, err)
	}
	retry, err := repository.Advance(ctx, rule, result, base, policy, time.Time{})
	if err != nil || retry != first {
		t.Fatalf("idempotent retry=%+v err=%v", retry, err)
	}
	if _, err := repository.Advance(ctx, rule, incidentEvaluation(1, StateFalse), base.Add(time.Second), policy, time.Time{}); !errors.Is(err, ErrIncidentVersionConflict) {
		t.Fatalf("stale expected state error=%v", err)
	}
	resolved, err := repository.Advance(ctx, rule, incidentEvaluation(1, StateFalse), base.Add(time.Second), policy, base)
	if err != nil || resolved.Status != IncidentResolved {
		t.Fatalf("resolved state=%+v err=%v", resolved, err)
	}
	stored, ok, err := repository.Current(ctx, rule.DeviceID, rule.ID)
	if err != nil || !ok || stored != resolved {
		t.Fatalf("stored state=%+v found=%t err=%v", stored, ok, err)
	}
}

func TestMemoryIncidentRepositorySerializesConcurrentExpectedState(t *testing.T) {
	repository, err := NewMemoryIncidentRepository(1)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	rule := incidentTestRule(true, 1)
	policy := IncidentPolicy{PendingFor: incidentDuration(time.Hour), RecoveryFor: incidentDuration(0), UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold}
	first, err := repository.Advance(context.Background(), rule, incidentEvaluation(1, StateTrue), base, policy, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, offset := range []time.Duration{time.Second, 2 * time.Second} {
		group.Add(1)
		go func(offset time.Duration) {
			defer group.Done()
			_, advanceErr := repository.Advance(context.Background(), rule, incidentEvaluation(1, StateTrue), base.Add(offset), policy, first.LastEvaluatedAt)
			results <- advanceErr
		}(offset)
	}
	group.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrIncidentVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent advance error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent advances: successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestMemoryIncidentRepositoryEnforcesCapacityAndRevisionIsolation(t *testing.T) {
	repository, err := NewMemoryIncidentRepository(1)
	if err != nil {
		t.Fatal(err)
	}
	policy := IncidentPolicy{PendingFor: incidentDuration(0), RecoveryFor: incidentDuration(0), UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold}
	base := time.Date(2026, 10, 22, 8, 0, 0, 0, time.UTC)
	rule := incidentTestRule(true, 1)
	if _, err := repository.Advance(context.Background(), rule, incidentEvaluation(1, StateTrue), base, policy, time.Time{}); err != nil {
		t.Fatal(err)
	}
	other := rule
	other.ID = "other-rule"
	otherResult := EvaluationResult{RuleID: other.ID, Revision: other.Revision, State: StateTrue}
	if _, err := repository.Advance(context.Background(), other, otherResult, base, policy, time.Time{}); !errors.Is(err, ErrIncidentCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	if _, err := repository.Advance(context.Background(), incidentTestRule(true, 2), incidentEvaluation(2, StateTrue), base.Add(time.Second), policy, base); !errors.Is(err, ErrIncidentRevision) {
		t.Fatalf("revision migration error=%v", err)
	}
	states, err := repository.ListCurrent(context.Background(), rule.DeviceID)
	if err != nil || len(states) != 1 || states[0].RuleID != rule.ID {
		t.Fatalf("current states=%+v err=%v", states, err)
	}
}
