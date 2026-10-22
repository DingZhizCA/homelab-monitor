/*
Package storage tests the approved dual-database settings and transactional
migration behavior without connecting to any production service or device.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestOpenCreatesIsolatedDatabasesWithApprovedSettings(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })

	if databases.CorePath == databases.MetricsPath {
		t.Fatal("core and metrics databases use the same file")
	}
	if maximum := databases.Core.Stats().MaxOpenConnections; maximum != 1 {
		t.Fatalf("core max open connections=%d", maximum)
	}
	for _, path := range []string{databases.CorePath, databases.MetricsPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("database file %s: %v", path, err)
		}
	}
	assertSQLiteSettings(t, databases.Core, "wal", 2)
	assertSQLiteSettings(t, databases.Metrics, "wal", 1)
	for name, fixture := range map[string]struct {
		database *sql.DB
		want     int
	}{"core": {databases.Core, 2}, "metrics": {databases.Metrics, 2}} {
		var count int
		if err := fixture.database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
			t.Fatalf("%s migration ledger: %v", name, err)
		}
		if count != fixture.want {
			t.Fatalf("%s has %d migrations, want %d", name, count, fixture.want)
		}
	}
}

func TestOpenRejectsNonPrivateStorageDirectory(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(context.Background(), directory); err == nil {
		t.Fatal("group-readable storage directory was accepted")
	}
}

func TestOpenBackfillsLegacyMetricBatchReceiptTime(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	database, err := openSQLite(context.Background(), filepath.Join(directory, "metrics.db"), "NORMAL")
	if err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(context.Background(), database, metricMigrations[:1]); err != nil {
		t.Fatal(err)
	}
	sampledAt := time.Date(2026, 10, 22, 1, 2, 3, 4, time.UTC)
	envelope := metrics.MetricEnvelope{
		SchemaVersion: 1, Timestamp: sampledAt, SamplingMode: "idle", SamplingIntervalSeconds: 30,
		Device: metrics.DeviceInfo{ID: "node-a", DisplayName: "Node A", Hostname: "node-a"},
		CPU:    metrics.CPUMetrics{UsagePercent: 10, LogicalCores: 4},
		Memory: metrics.MemoryMetrics{TotalBytes: 1024, UsedBytes: 512, AvailableBytes: 512, UsagePercent: 50},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO metric_batches (batch_id, device_id, content_sha256, snapshot_count) VALUES ('legacy', 'node-a', zeroblob(32), 1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(`INSERT INTO metric_snapshots (batch_id, sequence, device_id, sampled_at, envelope_json) VALUES ('legacy', 0, 'node-a', ?, ?)`, storageTimestamp(sampledAt), encoded); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}

	databases, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	var receivedAt string
	if err := databases.Metrics.QueryRow(`SELECT received_at FROM metric_batches WHERE batch_id = 'legacy'`).Scan(&receivedAt); err != nil {
		t.Fatal(err)
	}
	if receivedAt != storageTimestamp(sampledAt) {
		t.Fatalf("received_at=%q", receivedAt)
	}
	source, err := NewMetricCurrentSource(databases.Metrics)
	if err != nil {
		t.Fatal(err)
	}
	latest, err := source.LoadLatest(context.Background())
	if err != nil || len(latest) != 1 || !latest[0].ReceivedAt.Equal(sampledAt) {
		t.Fatalf("latest=%+v err=%v", latest, err)
	}
}

func TestApplyMigrationsIsTransactionalAndIdempotent(t *testing.T) {
	databases, err := Open(context.Background(), filepath.Join(testutil.TempDir(t), "storage"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	migrations := []Migration{{Version: 1001, Name: "create_fixture", SQL: `CREATE TABLE fixture (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`}}
	if err := ApplyMigrations(context.Background(), databases.Core, migrations); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(context.Background(), databases.Core, migrations); err != nil {
		t.Fatalf("repeat migrations: %v", err)
	}
	var count int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}

	failing := append(migrations, Migration{Version: 1002, Name: "rollback_fixture", SQL: `CREATE TABLE must_rollback (id INTEGER); INSERT INTO missing_table VALUES (1)`})
	if err := ApplyMigrations(context.Background(), databases.Core, failing); err == nil {
		t.Fatal("invalid migration succeeded")
	}
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='must_rollback'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("failed migration left table count=%d err=%v", count, err)
	}
}

func TestApplyMigrationsRejectsChangedHistoryAndInvalidOrdering(t *testing.T) {
	databases, err := Open(context.Background(), filepath.Join(testutil.TempDir(t), "storage"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	if err := ApplyMigrations(context.Background(), databases.Core, []Migration{{Version: 1001, Name: "original", SQL: `CREATE TABLE fixture (id INTEGER)`}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(context.Background(), databases.Core, []Migration{{Version: 1001, Name: "changed", SQL: `SELECT 1`}}); err == nil {
		t.Fatal("changed migration identity was accepted")
	}
	if err := ApplyMigrations(context.Background(), databases.Core, []Migration{{Version: 1002, Name: "second", SQL: `SELECT 1`}, {Version: 1001, Name: "first", SQL: `SELECT 1`}}); err == nil {
		t.Fatal("out-of-order migrations were accepted")
	}
}

func assertSQLiteSettings(t *testing.T, database *sql.DB, wantJournal string, wantSynchronous int) {
	t.Helper()
	var journal string
	var synchronous, foreignKeys, autoCheckpoint int
	if err := database.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA wal_autocheckpoint`).Scan(&autoCheckpoint); err != nil {
		t.Fatal(err)
	}
	if journal != wantJournal || synchronous != wantSynchronous || foreignKeys != 1 || autoCheckpoint != 0 {
		t.Fatalf("journal=%s synchronous=%d foreign_keys=%d wal_autocheckpoint=%d", journal, synchronous, foreignKeys, autoCheckpoint)
	}
}

func TestSQLiteDriverDisablesAutoCheckpointOnEveryPooledConnection(t *testing.T) {
	databases := openTestDatabases(t)
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
		var autoCheckpoint int
		if err := connection.QueryRowContext(context.Background(), `PRAGMA wal_autocheckpoint`).Scan(&autoCheckpoint); err != nil {
			t.Fatal(err)
		}
		if autoCheckpoint != 0 {
			t.Fatalf("connection %d wal_autocheckpoint=%d", len(connections), autoCheckpoint)
		}
	}
}
