/*
Package agent tests durable plugin recovery state without starting plugin
processes or contacting a Server.
*/
package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

func TestPluginReconcilerFileStoreRoundTripsAndBindsDevice(t *testing.T) {
	directory := privatePluginStateDirectory(t)
	store, err := openPluginReconcilerStore(directory, "node-a", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := plugins.ReconcilerCheckpoint{Plugins: []plugins.PluginCheckpoint{{
		PluginID: "health",
		Observed: &plugins.DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 3, Enabled: false},
	}}}
	if published, err := store.Save(checkpoint); err != nil || !published {
		t.Fatalf("save published=%v err=%v", published, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := openPluginReconcilerStore(directory, "node-a", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := reopened.Load()
	if err != nil || !reflect.DeepEqual(loaded, checkpoint) {
		t.Fatalf("loaded=%+v want=%+v err=%v", loaded, checkpoint, err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}

	wrongDevice, err := openPluginReconcilerStore(directory, "node-b", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongDevice.Close()
	if _, err := wrongDevice.Load(); err == nil {
		t.Fatal("plugin state was accepted for another device")
	}
}

func TestPluginReconcilerFileStoreLocksAndCleansTemporaryFiles(t *testing.T) {
	directory := privatePluginStateDirectory(t)
	orphan := pluginStatePrefix + strings.Repeat("a", 32)
	if err := os.WriteFile(filepath.Join(directory, orphan), []byte("orphan"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := openPluginReconcilerStore(directory, "node-a", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := os.Stat(filepath.Join(directory, orphan)); !os.IsNotExist(err) {
		t.Fatalf("orphan temporary file was not removed: %v", err)
	}
	if second, err := openPluginReconcilerStore(directory, "node-a", 1<<20); err == nil {
		_ = second.Close()
		t.Fatal("second plugin state owner acquired the lock")
	}
}

func TestPluginReconcilerFileStoreRejectsCorruptionPermissionsAndOversize(t *testing.T) {
	directory := privatePluginStateDirectory(t)
	store, err := openPluginReconcilerStore(directory, "node-a", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Save(plugins.ReconcilerCheckpoint{}); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(directory, pluginStateFile)
	if err := os.WriteFile(statePath, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("corrupt plugin state was accepted")
	}
	if err := os.Chmod(statePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(); err == nil {
		t.Fatal("non-private plugin state was accepted")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	boundedDirectory := privatePluginStateDirectory(t)
	bounded, err := openPluginReconcilerStore(boundedDirectory, "node-a", 16)
	if err != nil {
		t.Fatal(err)
	}
	defer bounded.Close()
	if published, err := bounded.Save(plugins.ReconcilerCheckpoint{}); err == nil || published {
		t.Fatalf("oversize state published=%v err=%v", published, err)
	}
}

func privatePluginStateDirectory(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(t.TempDir(), "state")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}
