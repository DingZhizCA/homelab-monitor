/*
Package storage verifies explicit SQLite limits, capacity transitions, bounded
recovery, and the metric-writer hard-cap error contract.
*/
package storage

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
	"github.com/mattn/go-sqlite3"
)

func TestCapacityManagerRequiresExplicitLimitsAndInstallsPageCaps(t *testing.T) {
	valid := CapacityConfig{
		CoreMaxBytes: 1 << 30, MetricsSoftBytes: 1 << 29,
		MetricsHardBytes: 1 << 30, FilesystemMinBytes: 1,
	}
	databases := openCapacityDatabases(t, valid.CoreMaxBytes, valid.MetricsHardBytes)
	retention, checkpoint := capacityMaintenance(t, databases, func(RetentionObservation) {}, func(CheckpointObservation) {})
	if _, err := NewCapacityManager(context.Background(), nil, valid, retention, checkpoint, func(CapacityObservation) {}); err == nil {
		t.Fatal("nil databases were accepted")
	}
	if _, err := NewCapacityManager(context.Background(), databases, CapacityConfig{}, retention, checkpoint, func(CapacityObservation) {}); err == nil {
		t.Fatal("implicit limits were accepted")
	}
	manager, err := NewCapacityManager(context.Background(), databases, valid, retention, checkpoint, func(CapacityObservation) {})
	if err != nil || manager == nil {
		t.Fatalf("manager=%v err=%v", manager, err)
	}
	for _, fixture := range []struct {
		database *sql.DB
		bytes    int64
	}{
		{databases.Core, valid.CoreMaxBytes},
		{databases.Metrics, valid.MetricsHardBytes},
	} {
		var pageSize, maxPages int64
		if err := fixture.database.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
			t.Fatal(err)
		}
		if err := fixture.database.QueryRow(`PRAGMA max_page_count`).Scan(&maxPages); err != nil {
			t.Fatal(err)
		}
		if maxPages != fixture.bytes/pageSize {
			t.Fatalf("max pages=%d want=%d", maxPages, fixture.bytes/pageSize)
		}
	}
}

func TestCapacityManagerReportsSoftStateWithoutRejecting(t *testing.T) {
	databases := openCapacityDatabases(t, 1<<30, 1<<30)
	retention, checkpoint := capacityMaintenance(t, databases, func(RetentionObservation) {}, func(CheckpointObservation) {})
	var observations []CapacityObservation
	manager, err := NewCapacityManager(context.Background(), databases, CapacityConfig{
		CoreMaxBytes: 1 << 30, MetricsSoftBytes: 1,
		MetricsHardBytes: 1 << 30, FilesystemMinBytes: 1,
	}, retention, checkpoint, func(observation CapacityObservation) { observations = append(observations, observation) })
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.PrepareMetricWrite(context.Background(), 1024); err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 || observations[0].Level != "soft" || observations[0].CleanupAttempted {
		t.Fatalf("observations=%+v", observations)
	}
	if err := manager.PrepareMetricWrite(context.Background(), 1024); err != nil {
		t.Fatal(err)
	}
	if len(observations) != 1 {
		t.Fatalf("unchanged soft state was reported again: %+v", observations)
	}
}

func TestOpenWithPageLimitsReappliesPageCapsAcrossReopen(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := OpenWithPageLimits(context.Background(), directory, 64<<20, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	retention, checkpoint := capacityMaintenance(t, databases, func(RetentionObservation) {}, func(CheckpointObservation) {})
	config := CapacityConfig{
		CoreMaxBytes: 64 << 20, MetricsSoftBytes: 32 << 20,
		MetricsHardBytes: 64 << 20, FilesystemMinBytes: 1,
	}
	if _, err := NewCapacityManager(context.Background(), databases, config, retention, checkpoint, func(CapacityObservation) {}); err != nil {
		t.Fatal(err)
	}
	var pageSize int64
	if err := databases.Metrics.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if err := databases.Close(); err != nil {
		t.Fatal(err)
	}
	databases, err = OpenWithPageLimits(context.Background(), directory, config.CoreMaxBytes, config.MetricsHardBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	var maxPages int64
	if err := databases.Metrics.QueryRow(`PRAGMA max_page_count`).Scan(&maxPages); err != nil {
		t.Fatal(err)
	}
	if maxPages != config.MetricsHardBytes/pageSize {
		t.Fatalf("reopened max pages=%d want=%d", maxPages, config.MetricsHardBytes/pageSize)
	}
}

func TestPageLimitsApplyToEveryPooledConnection(t *testing.T) {
	const metricsMaxBytes int64 = 64 << 20
	databases := openCapacityDatabases(t, 64<<20, metricsMaxBytes)
	databases.Metrics.SetMaxOpenConns(4)
	connections := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for range 4 {
		connection, err := databases.Metrics.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, connection)
		var pageSize, maxPages int64
		if err := connection.QueryRowContext(context.Background(), `PRAGMA page_size`).Scan(&pageSize); err != nil {
			t.Fatal(err)
		}
		if err := connection.QueryRowContext(context.Background(), `PRAGMA max_page_count`).Scan(&maxPages); err != nil {
			t.Fatal(err)
		}
		if maxPages != metricsMaxBytes/pageSize {
			t.Fatalf("connection %d max pages=%d want=%d", len(connections), maxPages, metricsMaxBytes/pageSize)
		}
	}
}

func TestPageLimitMapsDatabaseGrowthToCapacityError(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := OpenWithPageLimits(context.Background(), directory, 64<<20, 64<<20)
	if err != nil {
		t.Fatal(err)
	}
	var pageSize, pageCount int64
	if err := databases.Metrics.QueryRow(`PRAGMA page_size`).Scan(&pageSize); err != nil {
		t.Fatal(err)
	}
	if err := databases.Metrics.QueryRow(`PRAGMA page_count`).Scan(&pageCount); err != nil {
		t.Fatal(err)
	}
	if err := databases.Close(); err != nil {
		t.Fatal(err)
	}
	databases, err = OpenWithPageLimits(context.Background(), directory, 64<<20, pageSize*pageCount)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	guard := &metricWriteGuardFake{}
	writer, err := NewMetricBatchWriterWithGuard(databases.Metrics, 1, guard)
	if err != nil {
		t.Fatal(err)
	}
	batch := storageBatch("full", time.Now().UTC(), 10)
	batch.Snapshots[0].Device.DisplayName = strings.Repeat("x", 1<<20)
	if inserted, err := writer.AppendBatch(context.Background(), batch); !errors.Is(err, metrics.ErrMetricStorageCapacity) || inserted {
		t.Fatalf("page-limited append inserted=%t err=%v", inserted, err)
	}
	if guard.calls != 1 || guard.recoveryCalls != 1 {
		t.Fatalf("guard calls=%d recovery_calls=%d", guard.calls, guard.recoveryCalls)
	}
	assertMetricCounts(t, databases, 0, 0)
}

func TestCapacityManagerCleansThenRejectsBreachedFilesystemReserve(t *testing.T) {
	databases := openCapacityDatabases(t, 1<<30, 1<<30)
	writer, err := NewMetricBatchWriter(databases.Metrics, 1)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	batch := storageBatch("expired", now.Add(-2*time.Hour), 10)
	if inserted, err := writer.AppendBatch(context.Background(), batch); err != nil || !inserted {
		t.Fatalf("append inserted=%t err=%v", inserted, err)
	}
	var retentionObservations []RetentionObservation
	var checkpointObservations []CheckpointObservation
	retention, checkpoint := capacityMaintenance(t, databases,
		func(observation RetentionObservation) {
			retentionObservations = append(retentionObservations, observation)
		},
		func(observation CheckpointObservation) {
			checkpointObservations = append(checkpointObservations, observation)
		},
	)
	retention.now = func() time.Time { return now }
	freeBytes, err := filesystemFreeBytes(filepath.Dir(databases.MetricsPath))
	if err != nil {
		t.Fatal(err)
	}
	reserve := saturatingAdd(freeBytes, 1<<20)
	var capacityObservations []CapacityObservation
	manager, err := NewCapacityManager(context.Background(), databases, CapacityConfig{
		CoreMaxBytes: 1 << 30, MetricsSoftBytes: 1 << 29,
		MetricsHardBytes: 1 << 30, FilesystemMinBytes: reserve,
	}, retention, checkpoint, func(observation CapacityObservation) {
		capacityObservations = append(capacityObservations, observation)
	})
	if err != nil {
		t.Fatal(err)
	}
	err = manager.PrepareMetricWrite(context.Background(), 1024)
	if !errors.Is(err, metrics.ErrMetricStorageCapacity) {
		t.Fatalf("capacity error=%v", err)
	}
	if len(retentionObservations) != 1 || retentionObservations[0].DeletedSnapshots != 1 {
		t.Fatalf("retention observations=%+v", retentionObservations)
	}
	if len(checkpointObservations) != 1 || checkpointObservations[0].Reason != "capacity" {
		t.Fatalf("checkpoint observations=%+v", checkpointObservations)
	}
	if len(capacityObservations) != 1 || capacityObservations[0].Level != "hard" || !capacityObservations[0].CleanupAttempted {
		t.Fatalf("capacity observations=%+v", capacityObservations)
	}
	err = manager.PrepareMetricWrite(context.Background(), 1024)
	if !errors.Is(err, metrics.ErrMetricStorageCapacity) || len(retentionObservations) != 1 || len(checkpointObservations) != 1 {
		t.Fatalf("deferred recovery err=%v retention=%d checkpoint=%d", err, len(retentionObservations), len(checkpointObservations))
	}
	if len(capacityObservations) != 2 || !capacityObservations[1].RecoveryDeferred || capacityObservations[1].CleanupAttempted {
		t.Fatalf("deferred capacity observation=%+v", capacityObservations)
	}
}

func TestMetricWriteErrorMapsSQLiteFullToCapacity(t *testing.T) {
	err := metricWriteError("fixture", sqlite3.Error{Code: sqlite3.ErrFull})
	if !errors.Is(err, metrics.ErrMetricStorageCapacity) {
		t.Fatalf("mapped error=%v", err)
	}
}

func capacityMaintenance(t *testing.T, databases *Databases, retentionObserver RetentionObserver, checkpointObserver CheckpointObserver) (*MetricRetentionManager, *CheckpointManager) {
	t.Helper()
	retention, err := NewMetricRetentionManager(databases.Metrics, RetentionConfig{
		Interval: time.Hour, RawRetention: time.Hour, ReceiptRetention: 2 * time.Hour,
		DeleteLimit: 100, Timeout: time.Second,
	}, retentionObserver)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint, err := NewCheckpointManager(databases, CheckpointConfig{
		Interval: time.Hour, WALThresholdBytes: 4096, Timeout: time.Second,
	}, checkpointObserver)
	if err != nil {
		t.Fatal(err)
	}
	return retention, checkpoint
}

func openCapacityDatabases(t *testing.T, coreMaxBytes, metricsMaxBytes int64) *Databases {
	t.Helper()
	databases, err := OpenWithPageLimits(context.Background(), filepath.Join(testutil.TempDir(t), "storage"), coreMaxBytes, metricsMaxBytes)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	return databases
}
