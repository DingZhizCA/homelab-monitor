/*
Package notifications verifies explicit retry limits, capped exponential
backoff, bounded error text, and terminal dead-letter decisions.
*/
package notifications

import (
	"errors"
	"testing"
	"time"
)

func TestPlanFailureBackoffAndDeadLetter(t *testing.T) {
	policy := RetryPolicy{MaxAttempts: 4, InitialBackoff: 2 * time.Second, MaxBackoff: 5 * time.Second, MaxErrorBytes: 7}
	at := time.Date(2026, 10, 26, 18, 0, 0, 0, time.UTC)
	id := processorIntent(t, "retry", at).ID

	first, err := PlanFailure(id, 0, at, errors.New("  service   unavailable  "), policy)
	if err != nil || first.AttemptCount != 1 || first.DeadLetter || !first.NextAttempt.Equal(at.Add(2*time.Second)) || first.LastError != "service" {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	third, err := PlanFailure(id, 2, at, errors.New("failed"), policy)
	if err != nil || third.AttemptCount != 3 || third.DeadLetter || !third.NextAttempt.Equal(at.Add(5*time.Second)) {
		t.Fatalf("third=%+v err=%v", third, err)
	}
	terminal, err := PlanFailure(id, 3, at, errors.New("failed"), policy)
	if err != nil || !terminal.DeadLetter || !terminal.NextAttempt.IsZero() {
		t.Fatalf("terminal=%+v err=%v", terminal, err)
	}
}

func TestPlanFailureRejectsImplicitLimits(t *testing.T) {
	if _, err := PlanFailure("bad", 0, time.Time{}, nil, RetryPolicy{}); err == nil {
		t.Fatal("invalid retry inputs were accepted")
	}
}
