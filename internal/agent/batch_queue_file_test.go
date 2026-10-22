/*
Package agent tests crash-safe metric queue restoration and mutation ordering.
*/
package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestFileBatchQueueRestoresPendingIdentityAndAcknowledgment(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "queue")
	base := time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC)
	queue := openTestFileBatchQueue(t, directory, "node-a")
	for index := range 3 {
		if err := queue.Append(senderTestEnvelope(base.Add(time.Duration(index) * time.Minute))); err != nil {
			t.Fatal(err)
		}
	}
	pending, found, err := queue.PendingBatch()
	if err != nil || !found || len(pending.Snapshots) != 2 {
		t.Fatalf("pending=%+v found=%t err=%v", pending, found, err)
	}
	if concurrent, err := OpenBatchQueue(directory, "node-a", 4, 64*1024, 2, 32*1024); err == nil {
		_ = concurrent.Close()
		t.Fatal("a second process lock for the same queue was accepted")
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(directory, ".metrics-queue-00000000000000000000000000000000")
	if err := os.WriteFile(orphan, []byte("incomplete"), 0o600); err != nil {
		t.Fatal(err)
	}

	restored := openTestFileBatchQueue(t, directory, "node-a")
	if _, err := os.Stat(orphan); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphaned temporary state was not removed: %v", err)
	}
	retry, found, err := restored.PendingBatch()
	if err != nil || !found || retry.ID != pending.ID || restored.Len() != 3 {
		t.Fatalf("restored pending=%+v found=%t len=%d err=%v", retry, found, restored.Len(), err)
	}
	if err := restored.Acknowledge(retry.ID); err != nil {
		t.Fatal(err)
	}
	if err := restored.Close(); err != nil {
		t.Fatal(err)
	}

	afterAck := openTestFileBatchQueue(t, directory, "node-a")
	t.Cleanup(func() { _ = afterAck.Close() })
	if afterAck.Len() != 1 {
		t.Fatalf("restored length after acknowledgment=%d", afterAck.Len())
	}
	next, found, err := afterAck.PendingBatch()
	if err != nil || !found || next.ID == pending.ID || len(next.Snapshots) != 1 {
		t.Fatalf("next pending=%+v found=%t err=%v", next, found, err)
	}
}

func TestFileBatchQueueRejectsCorruptionIdentityAndUnsafePermissions(t *testing.T) {
	root := testutil.TempDir(t)
	directory := filepath.Join(root, "queue")
	queue := openTestFileBatchQueue(t, directory, "node-a")
	if err := queue.Append(senderTestEnvelope(time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC))); err != nil {
		t.Fatal(err)
	}
	if err := queue.Close(); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenBatchQueue(directory, "node-b", 4, 64*1024, 2, 32*1024); err == nil {
		_ = reopened.Close()
		t.Fatal("queue state belonging to another device was accepted")
	}
	if err := os.WriteFile(filepath.Join(directory, batchQueueStateFile), []byte(`{"version":1`), 0o600); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenBatchQueue(directory, "node-a", 4, 64*1024, 2, 32*1024); err == nil {
		_ = reopened.Close()
		t.Fatal("corrupt queue state was accepted")
	}
	unsafe := filepath.Join(root, "unsafe")
	if err := os.Mkdir(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0o755); err != nil {
		t.Fatal(err)
	}
	if reopened, err := OpenBatchQueue(unsafe, "node-a", 4, 64*1024, 2, 32*1024); err == nil {
		_ = reopened.Close()
		t.Fatal("non-private queue directory was accepted")
	}
}

type controllableQueueStore struct {
	err       error
	published bool
}

func (s *controllableQueueStore) Save(batchQueueDiskState) (bool, error) {
	return s.err == nil || s.published, s.err
}
func (*controllableQueueStore) Close() error { return nil }

func TestPersistentBatchQueueDoesNotExposeUncommittedMutations(t *testing.T) {
	store := &controllableQueueStore{err: errors.New("disk unavailable")}
	queue, err := newBatchQueue("node-a", 2, 64*1024, 1, 32*1024, store, batchQueueDiskState{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := senderTestEnvelope(time.Date(2026, 10, 23, 2, 0, 0, 0, time.UTC))
	if err := queue.Append(snapshot); err == nil || queue.Len() != 0 {
		t.Fatalf("failed append err=%v len=%d", err, queue.Len())
	}
	store.err = nil
	if err := queue.Append(snapshot); err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("disk unavailable")
	if _, found, err := queue.PendingBatch(); err == nil || found {
		t.Fatalf("failed pending found=%t err=%v", found, err)
	}
	store.err = nil
	pending, found, err := queue.PendingBatch()
	if err != nil || !found {
		t.Fatalf("pending found=%t err=%v", found, err)
	}
	store.err = errors.New("disk unavailable")
	if err := queue.Acknowledge(pending.ID); err == nil || queue.Len() != 1 {
		t.Fatalf("failed acknowledgment err=%v len=%d", err, queue.Len())
	}
	store.published = true
	if err := queue.Acknowledge(pending.ID); err == nil || queue.Len() != 0 {
		t.Fatalf("published acknowledgment warning err=%v len=%d", err, queue.Len())
	}
}

func openTestFileBatchQueue(t *testing.T, directory, deviceID string) *BatchQueue {
	t.Helper()
	queue, err := OpenBatchQueue(directory, deviceID, 4, 64*1024, 2, 32*1024)
	if err != nil {
		t.Fatal(err)
	}
	return queue
}
