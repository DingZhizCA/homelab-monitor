/*
Package alerts verifies deterministic Incident status-change events.
*/
package alerts

import (
	"testing"
	"time"
)

func TestBuildIncidentTransitionRecordsOnlyStatusChanges(t *testing.T) {
	base := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
	rule := incidentTestRule(true, 1)
	zero := time.Duration(0)
	policy := IncidentPolicy{PendingFor: &zero, RecoveryFor: &zero, UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold}
	firing := advanceTestIncident(t, IncidentState{}, rule, StateTrue, base, policy)
	event, changed, err := BuildIncidentTransition(IncidentState{}, false, firing)
	if err != nil || !changed || event.FromStatus != IncidentInactive || event.ToStatus != IncidentFiring || event.ID == "" {
		t.Fatalf("initial transition=%+v changed=%t err=%v", event, changed, err)
	}
	if err := ValidateIncidentTransition(event); err != nil {
		t.Fatal(err)
	}
	held := advanceTestIncident(t, firing, rule, StateTrue, base.Add(time.Second), policy)
	if event, changed, err := BuildIncidentTransition(firing, true, held); err != nil || changed || event != (IncidentTransition{}) {
		t.Fatalf("unchanged transition=%+v changed=%t err=%v", event, changed, err)
	}
	resolved := advanceTestIncident(t, held, rule, StateFalse, base.Add(2*time.Second), policy)
	first, changed, err := BuildIncidentTransition(held, true, resolved)
	if err != nil || !changed || first.FromStatus != IncidentFiring || first.ToStatus != IncidentResolved {
		t.Fatalf("resolved transition=%+v changed=%t err=%v", first, changed, err)
	}
	second, _, _ := BuildIncidentTransition(held, true, resolved)
	if first.ID != second.ID {
		t.Fatalf("transition IDs differ: %q != %q", first.ID, second.ID)
	}
}
