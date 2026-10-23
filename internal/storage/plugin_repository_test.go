/*
Package storage verifies durable, atomic plugin desired and reported state.
*/
package storage

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestPluginRepositoryPersistsDesiredStateAndAuditIdempotently(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := createPluginOwner(t, databases, "owner-a")
	repository, err := NewPluginRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	state := pluginDesiredFixture(1, false)
	event := pluginAuditFixture(t, ownerID, state)
	if err := repository.SetDesiredWithAuditContext(context.Background(), state, event); err != nil {
		t.Fatal(err)
	}
	if err := repository.SetDesiredWithAuditContext(context.Background(), state, event); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	states, err := repository.DesiredForDeviceContext(context.Background(), "node-a")
	if err != nil || len(states) != 1 || states[0] != state {
		t.Fatalf("states=%+v err=%v", states, err)
	}
	var events int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE action = ?`, plugins.DesiredStateAuditAction).Scan(&events); err != nil || events != 1 {
		t.Fatalf("events=%d err=%v", events, err)
	}

	conflict := state
	conflict.Enabled = true
	conflict.Version = "1.0.0"
	conflict.Artifact = pluginArtifactFixture()
	if err := repository.SetDesiredWithAuditContext(context.Background(), conflict, event); !errors.Is(err, plugins.ErrStateConflict) {
		t.Fatalf("same-revision conflict error=%v", err)
	}
	newer := conflict
	newer.Revision = 2
	if err := repository.SetDesiredWithAuditContext(context.Background(), newer, pluginAuditFixture(t, ownerID, newer)); err != nil {
		t.Fatal(err)
	}
	states, err = repository.DesiredForDeviceContext(context.Background(), "node-a")
	if err != nil || len(states) != 1 || states[0] != newer {
		t.Fatalf("updated states=%+v err=%v", states, err)
	}
	if err := databases.Close(); err != nil {
		t.Fatal(err)
	}
	databases, err = Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	repository, err = NewPluginRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	states, err = repository.DesiredForDeviceContext(context.Background(), "node-a")
	if err != nil || len(states) != 1 || states[0] != newer {
		t.Fatalf("states after reopen=%+v err=%v", states, err)
	}
}

func TestPluginRepositoryRechecksCurrentOwnerBeforeDesiredMutation(t *testing.T) {
	databases := openTestDatabases(t)
	ownerID := createPluginOwner(t, databases, "owner-a")
	repository, err := NewPluginRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := databases.Core.Exec(`UPDATE users SET role = 'viewer' WHERE user_id = ?`, ownerID); err != nil {
		t.Fatal(err)
	}
	state := pluginDesiredFixture(1, false)
	if err := repository.SetDesiredWithAuditContext(context.Background(), state, pluginAuditFixture(t, ownerID, state)); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("demoted owner error=%v", err)
	}
	states, err := repository.DesiredForDeviceContext(context.Background(), "node-a")
	if err != nil || len(states) != 0 {
		t.Fatalf("states=%+v err=%v", states, err)
	}
}

func TestPluginRepositoryListsOnlyEnabledDesiredArtifactReferences(t *testing.T) {
	databases := openTestDatabases(t)
	ownerID := createPluginOwner(t, databases, "owner-a")
	repository, err := NewPluginRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	enabled := pluginDesiredFixture(1, true)
	if err := repository.SetDesiredWithAuditContext(context.Background(), enabled, pluginAuditFixture(t, ownerID, enabled)); err != nil {
		t.Fatal(err)
	}
	keys, err := repository.DesiredArtifactKeysContext(context.Background())
	want := enabled.Artifact.Key(enabled.PluginID, enabled.Version)
	if err != nil || len(keys) != 1 || keys[0] != want {
		t.Fatalf("keys=%+v want=%+v err=%v", keys, want, err)
	}
	disabled := pluginDesiredFixture(2, false)
	if err := repository.SetDesiredWithAuditContext(context.Background(), disabled, pluginAuditFixture(t, ownerID, disabled)); err != nil {
		t.Fatal(err)
	}
	keys, err = repository.DesiredArtifactKeysContext(context.Background())
	if err != nil || len(keys) != 0 {
		t.Fatalf("disabled Desired references=%+v err=%v", keys, err)
	}
}

func TestPluginRepositoryReportedBatchIsAtomicAndDurable(t *testing.T) {
	databases := openTestDatabases(t)
	repository, err := NewPluginRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	initial := plugins.ReportedState{DeviceID: "node-a", PluginID: "existing", ObservedRev: 2, AppliedRev: 2, Lifecycle: plugins.StateRunning}
	if err := repository.SetReportedBatchContext(context.Background(), []plugins.ReportedState{initial}); err != nil {
		t.Fatal(err)
	}
	batch := []plugins.ReportedState{
		{DeviceID: "node-a", PluginID: "new", ObservedRev: 1, AppliedRev: 1, Lifecycle: plugins.StateRunning},
		{DeviceID: "node-a", PluginID: "existing", ObservedRev: 1, AppliedRev: 1, Lifecycle: plugins.StateFailed},
	}
	if err := repository.SetReportedBatchContext(context.Background(), batch); !errors.Is(err, plugins.ErrStateConflict) {
		t.Fatalf("stale batch error=%v", err)
	}
	states, err := repository.ReportedForDeviceContext(context.Background(), "node-a")
	if err != nil || len(states) != 1 || states[0] != initial {
		t.Fatalf("states after rejected batch=%+v err=%v", states, err)
	}
	batch[1] = initial
	if err := repository.SetReportedBatchContext(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewPluginRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	states, err = reopened.ReportedForDeviceContext(context.Background(), "node-a")
	if err != nil || len(states) != 2 || states[0].PluginID != "existing" || states[1].PluginID != "new" {
		t.Fatalf("durable states=%+v err=%v", states, err)
	}
}

func TestPluginRepositoryRejectsInvalidInputsAndCorruptRows(t *testing.T) {
	if _, err := NewPluginRepository(nil); err == nil {
		t.Fatal("nil database was accepted")
	}
	databases := openTestDatabases(t)
	repository, err := NewPluginRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DesiredForDeviceContext(context.Background(), " node-a "); err == nil {
		t.Fatal("untrimmed device ID was accepted")
	}
	if _, err := databases.Core.Exec(`INSERT INTO plugin_desired_states
		(device_id, plugin_id, revision, version, artifact_os, artifact_arch, artifact_sha256, enabled)
		VALUES ('node-a', 'health', 1, '', '', '', '', 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DesiredForDeviceContext(context.Background(), "node-a"); err == nil {
		t.Fatal("corrupt desired state was accepted")
	}
}

func createPluginOwner(t *testing.T, databases *Databases, ownerID string) string {
	t.Helper()
	repository, err := NewAuthRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	event, err := audit.NewEvent(time.Now(), ownerID, "auth.user.bootstrap", "user", ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateInitialOwner(context.Background(), auth.User{ID: ownerID, Username: ownerID, Role: auth.RoleOwner, PasswordHash: "fixture"}, event); err != nil {
		t.Fatal(err)
	}
	return ownerID
}

func pluginDesiredFixture(revision uint64, enabled bool) plugins.DesiredState {
	state := plugins.DesiredState{DeviceID: "node-a", PluginID: "health", Revision: revision, Enabled: enabled}
	if enabled {
		state.Version = "1.0.0"
		state.Artifact = pluginArtifactFixture()
	}
	return state
}

func pluginArtifactFixture() plugins.ArtifactReference {
	return plugins.ArtifactReference{
		Platform: plugins.Platform{OS: "linux", Arch: "arm64"},
		SHA256:   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func pluginAuditFixture(t *testing.T, ownerID string, state plugins.DesiredState) audit.Event {
	t.Helper()
	event, err := audit.NewEvent(time.Now(), ownerID, plugins.DesiredStateAuditAction, plugins.DesiredStateAuditResourceType, plugins.DesiredStateAuditResourceID(state))
	if err != nil {
		t.Fatal(err)
	}
	return event
}
