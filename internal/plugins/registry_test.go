/*
Package plugins tests desired/reported revision isolation and registry validation.
*/
package plugins

import (
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
)

func TestRegistryRevisionsAndDesiredReportedSeparation(t *testing.T) {
	registry := NewRegistry()
	first := DesiredState{DeviceID: "node-1", PluginID: "storage.health", Revision: 1, Version: "1.0.0", Artifact: testArtifactReference(), Enabled: true}
	if err := registry.SetDesired(first); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetDesired(first); err != nil {
		t.Fatalf("identical retry should be idempotent: %v", err)
	}
	if err := registry.SetDesired(DesiredState{DeviceID: "node-1", PluginID: "storage.health", Revision: 1, Version: "2.0.0", Artifact: testArtifactReference(), Enabled: true}); err == nil {
		t.Fatal("conflicting same revision was accepted")
	}
	if err := registry.SetDesired(DesiredState{DeviceID: "node-1", PluginID: "storage.health", Revision: 2, Version: "2.0.0", Artifact: testArtifactReference(), Enabled: true}); err != nil {
		t.Fatal(err)
	}

	reported := ReportedState{DeviceID: "node-1", PluginID: "storage.health", ObservedRev: 2, AppliedRev: 1, Lifecycle: StateRunning}
	if err := registry.SetReported(reported); err != nil {
		t.Fatal(err)
	}
	got, ok := registry.Reported("node-1", "storage.health")
	if !ok || got.AppliedRev != 1 {
		t.Fatalf("reported state not preserved independently: %+v, %v", got, ok)
	}
	if _, ok := registry.Desired("node-1", "storage.health"); !ok {
		t.Fatal("desired state disappeared after reported update")
	}
	if err := registry.SetReported(ReportedState{DeviceID: "node-1", PluginID: "storage.health", ObservedRev: 1, AppliedRev: 1, Lifecycle: StateRunning}); err == nil {
		t.Fatal("stale reported revision was accepted")
	}
}

func TestRegistryRejectsAppliedRevisionAheadOfObserved(t *testing.T) {
	registry := NewRegistry()
	err := registry.SetReported(ReportedState{DeviceID: "node-1", PluginID: "x", ObservedRev: 1, AppliedRev: 2, Lifecycle: StateRunning})
	if err == nil {
		t.Fatal("applied revision ahead of observed revision was accepted")
	}
}

func TestRegistryDeviceSnapshotsAreSortedAndIsolated(t *testing.T) {
	registry := NewRegistry()
	for _, state := range []DesiredState{
		{DeviceID: "node-a", PluginID: "zeta", Revision: 1, Version: "1.0.0", Artifact: testArtifactReference(), Enabled: true},
		{DeviceID: "node-b", PluginID: "other", Revision: 1, Version: "1.0.0", Artifact: testArtifactReference(), Enabled: true},
		{DeviceID: "node-a", PluginID: "alpha", Revision: 1, Version: "1.0.0", Artifact: testArtifactReference(), Enabled: true},
	} {
		if err := registry.SetDesired(state); err != nil {
			t.Fatal(err)
		}
	}
	states := registry.DesiredForDevice("node-a")
	if len(states) != 2 || states[0].PluginID != "alpha" || states[1].PluginID != "zeta" {
		t.Fatalf("desired snapshot = %+v", states)
	}
	states[0].PluginID = "mutated"
	if again := registry.DesiredForDevice("node-a"); again[0].PluginID != "alpha" {
		t.Fatalf("snapshot mutation escaped into registry: %+v", again)
	}
	if got := registry.DesiredForDevice("missing"); len(got) != 0 {
		t.Fatalf("unknown device snapshot = %+v", got)
	}
}

func testArtifactReference() ArtifactReference {
	return ArtifactReference{
		Platform: Platform{OS: "linux", Arch: "arm64"},
		SHA256:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func TestRegistryRejectsUnknownLifecycleAndUnsafeIdentifiers(t *testing.T) {
	registry := NewRegistry()
	if err := registry.SetDesired(DesiredState{
		DeviceID: "node-1", PluginID: "../escape", Revision: 1, Version: "1.0.0", Enabled: true,
	}); err == nil {
		t.Fatal("desired state with an invalid plugin identifier was accepted")
	}
	if err := registry.SetDesired(DesiredState{
		DeviceID: "node-1", PluginID: "health", Revision: 1, Version: "../escape", Enabled: true,
	}); err == nil {
		t.Fatal("desired state with an invalid version was accepted")
	}
	batch := []ReportedState{
		{DeviceID: "node-1", PluginID: "health", ObservedRev: 1, AppliedRev: 1, Lifecycle: StateRunning},
		{DeviceID: "node-1", PluginID: "disk", ObservedRev: 1, AppliedRev: 0, Lifecycle: "unknown"},
	}
	if err := registry.SetReportedBatch(batch); err == nil {
		t.Fatal("batch with an unknown lifecycle state was accepted")
	}
	if _, ok := registry.Reported("node-1", "health"); ok {
		t.Fatal("valid prefix of invalid batch was committed")
	}
}

func TestDesiredStateRequiresImmutableArtifactIdentityWhenEnabled(t *testing.T) {
	state := DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 1, Version: "1.0.0", Enabled: true}
	if err := state.Validate(); err == nil {
		t.Fatal("enabled desired state without artifact identity was accepted")
	}
	state.Artifact = testArtifactReference()
	if err := state.Validate(); err != nil {
		t.Fatalf("valid desired artifact identity rejected: %v", err)
	}
	state.Enabled = false
	state.Artifact = ArtifactReference{}
	if err := state.Validate(); err != nil {
		t.Fatalf("disabled desired state should not require an artifact: %v", err)
	}
}

func TestRegistryRejectsUntrimmedDeviceIDs(t *testing.T) {
	registry := NewRegistry()
	if err := registry.SetDesired(DesiredState{DeviceID: " node-a ", PluginID: "health", Revision: 1}); err == nil {
		t.Fatal("desired state with a padded device ID was accepted")
	}
	if err := registry.SetReported(ReportedState{DeviceID: " node-a ", PluginID: "health", ObservedRev: 1, Lifecycle: StateInstalled}); err == nil {
		t.Fatal("reported state with a padded device ID was accepted")
	}
}

func TestRegistryCommitsDesiredStateAndAuditAtomically(t *testing.T) {
	registry := NewRegistry()
	state := DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 1}
	event, err := audit.NewEvent(time.Unix(10, 0), "owner-id", DesiredStateAuditAction, DesiredStateAuditResourceType, DesiredStateAuditResourceID(state))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SetDesiredWithAudit(state, event); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Desired("node-a", "health"); !ok {
		t.Fatal("desired state was not committed")
	}
	if events := registry.AuditEvents(); len(events) != 1 || events[0] != event {
		t.Fatalf("audit events = %+v", events)
	}
	if err := registry.SetDesiredWithAudit(state, event); err != nil {
		t.Fatalf("identical retry should be idempotent: %v", err)
	}
	if events := registry.AuditEvents(); len(events) != 1 {
		t.Fatalf("identical retry duplicated audit event: %+v", events)
	}
}

func TestRegistryRejectsDesiredStateWhenAuditDoesNotMatch(t *testing.T) {
	registry := NewRegistry()
	state := DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 1}
	wrongTarget, err := audit.NewEvent(time.Unix(10, 0), "owner-id", DesiredStateAuditAction, DesiredStateAuditResourceType, "node-b/health")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SetDesiredWithAudit(state, wrongTarget); err == nil {
		t.Fatal("desired state with mismatched audit target was accepted")
	}
	wrongAction, err := audit.NewEvent(time.Unix(10, 0), "owner-id", "plugin.desired.delete", DesiredStateAuditResourceType, DesiredStateAuditResourceID(state))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SetDesiredWithAudit(state, wrongAction); err == nil {
		t.Fatal("desired state with mismatched audit action was accepted")
	}
	if _, ok := registry.Desired("node-a", "health"); ok {
		t.Fatal("desired state was committed without a matching audit event")
	}
	if events := registry.AuditEvents(); len(events) != 0 {
		t.Fatalf("rejected desired state produced audit events: %+v", events)
	}
}

func TestRegistryRejectsConflictingAuditedRevisionWithoutAudit(t *testing.T) {
	registry := NewRegistry()
	state := DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 1}
	event, err := audit.NewEvent(time.Unix(10, 0), "owner-id", DesiredStateAuditAction, DesiredStateAuditResourceType, DesiredStateAuditResourceID(state))
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SetDesiredWithAudit(state, event); err != nil {
		t.Fatal(err)
	}
	conflict := state
	conflict.Enabled = true
	conflict.Version = "1.0.0"
	conflict.Artifact = testArtifactReference()
	if err := registry.SetDesiredWithAudit(conflict, event); err == nil {
		t.Fatal("conflicting audited revision was accepted")
	}
	if events := registry.AuditEvents(); len(events) != 1 {
		t.Fatalf("conflicting revision produced audit events: %+v", events)
	}
}
