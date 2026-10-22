/*
Package storage verifies durable Incident transition state, timestamp CAS,
idempotent retries, revision isolation, and explicit row capacity.
*/
package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestIncidentRepositoryPersistsTransitionsAcrossReopen(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewIncidentRepository(databases.Core, 2)
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
	repository, err = NewIncidentRepository(databases.Core, 2)
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

func TestIncidentRepositoryEnforcesCASCapacityAndRevisionIsolation(t *testing.T) {
	databases := openTestDatabases(t)
	repository, err := NewIncidentRepository(databases.Core, 1)
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
