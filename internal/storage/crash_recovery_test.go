/*
Package storage verifies committed WAL recovery after an ungraceful Server
process exit. The test uses only temporary local databases and fake records.
*/
package storage

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

const crashRecoveryHelperEnv = "HOMELAB_MONITOR_STORAGE_CRASH_HELPER"

func TestStorageRecoversCommittedWALAfterProcessKill(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	command := exec.Command(os.Args[0], "-test.run=^TestStorageCrashRecoveryHelper$")
	command.Env = append(os.Environ(), crashRecoveryHelperEnv+"=1", "HOMELAB_MONITOR_STORAGE_CRASH_DIR="+directory)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			ready <- scanner.Text()
			return
		}
		ready <- ""
	}()
	select {
	case line := <-ready:
		if line != "READY" {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("storage helper readiness=%q", line)
		}
	case <-time.After(10 * time.Second):
		_ = command.Process.Kill()
		_ = command.Wait()
		t.Fatal("storage helper did not become ready")
	}
	for _, name := range []string{"core.db-wal", "metrics.db-wal"} {
		info, err := os.Stat(filepath.Join(directory, name))
		if err != nil || info.Size() == 0 {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("%s was not populated before kill: size=%d err=%v", name, fileSize(info), err)
		}
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("killed storage helper exited successfully")
	}

	databases, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	for name, database := range map[string]*sql.DB{"core": databases.Core, "metrics": databases.Metrics} {
		var result string
		if err := database.QueryRow(`PRAGMA quick_check`).Scan(&result); err != nil || result != "ok" {
			t.Fatalf("%s quick_check=%q err=%v", name, result, err)
		}
	}
	repository, err := NewPluginRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	desired, err := repository.DesiredForDeviceContext(context.Background(), "node-a")
	if err != nil || len(desired) != 1 || desired[0].Revision != 1 {
		t.Fatalf("desired=%+v err=%v", desired, err)
	}
	current, err := NewMetricCurrentSource(databases.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	snapshots, err := current.LoadLatest(context.Background())
	if err != nil || len(snapshots) != 1 || snapshots[0].Envelope.Device.ID != "node-a" {
		t.Fatalf("snapshots=%+v err=%v", snapshots, err)
	}
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := writer.AppendBatch(context.Background(), storageBatch("crash-batch", snapshots[0].Envelope.Timestamp, 10)); err != nil || inserted {
		t.Fatalf("retry after recovery inserted=%t err=%v", inserted, err)
	}
}

func TestStorageCrashRecoveryHelper(t *testing.T) {
	if os.Getenv(crashRecoveryHelperEnv) != "1" {
		return
	}
	directory := os.Getenv("HOMELAB_MONITOR_STORAGE_CRASH_DIR")
	databases, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := createPluginOwner(t, databases, "owner-crash")
	repository, err := NewPluginRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	desired := pluginDesiredFixture(1, false)
	if err := repository.SetDesiredWithAuditContext(context.Background(), desired, pluginAuditFixture(t, ownerID, desired)); err != nil {
		t.Fatal(err)
	}
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	if inserted, err := writer.AppendBatch(context.Background(), storageBatch("crash-batch", time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC), 10)); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "READY"); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func fileSize(info os.FileInfo) int64 {
	if info == nil {
		return 0
	}
	return info.Size()
}
