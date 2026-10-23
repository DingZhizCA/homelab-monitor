/*
Package agent tests plugin queue crash state recovery and single-writer locking.
*/
package agent

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestPluginBatchQueuePersistsPendingBatchAcrossReopen(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "plugin-queue")
	limits := testPluginQueueLimits()
	queue, err := OpenPluginBatchQueue(directory, "node-a", limits)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Append(testPluginObservation(time.Now().UTC())); err != nil {
		t.Fatal(err)
	}
	pending, _, err := queue.PendingBatch()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := OpenPluginBatchQueue(directory, "node-a", limits); err == nil {
		t.Fatal("concurrent queue open succeeded")
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenPluginBatchQueue(directory, "node-a", limits)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restored, ok, err := reopened.PendingBatch()
	if err != nil || !ok || restored.ID != pending.ID {
		t.Fatalf("restored=%+v pending=%t err=%v", restored, ok, err)
	}
}
