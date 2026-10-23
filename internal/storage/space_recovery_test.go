/*
Package storage verifies explicit SQLite format migration, bounded incremental
vacuum, and WAL truncation without running Server background goroutines.
*/
package storage

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
	_ "github.com/mattn/go-sqlite3"
)

func TestNewDatabasesUseIncrementalAutoVacuum(t *testing.T) {
	databases := openTestDatabases(t)
	for name, database := range map[string]*sql.DB{"core": databases.Core, "metrics": databases.Metrics} {
		var mode int
		if err := database.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 2 {
			t.Fatalf("%s auto_vacuum=%d err=%v", name, mode, err)
		}
	}
}

func TestEnableIncrementalVacuumMigratesLegacyDatabases(t *testing.T) {
	directory := testutil.TempDir(t)
	databases := &Databases{CorePath: filepath.Join(directory, "core.db"), MetricsPath: filepath.Join(directory, "metrics.db")}
	var err error
	databases.Core, err = sql.Open("sqlite3", databases.CorePath)
	if err != nil {
		t.Fatal(err)
	}
	databases.Metrics, err = sql.Open("sqlite3", databases.MetricsPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	for _, database := range []*sql.DB{databases.Core, databases.Metrics} {
		if _, err := database.Exec(`CREATE TABLE legacy (id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
			t.Fatal(err)
		}
	}
	config := SpaceRecoveryConfig{Interval: time.Hour, Timeout: time.Second, WALTruncateBytes: 1, VacuumMinFreePages: 1, VacuumMaxPagesPerRun: 1}
	if _, err := NewSpaceRecoveryManager(context.Background(), databases, config, func(SpaceRecoveryObservation) {}); err == nil {
		t.Fatal("legacy databases were accepted without explicit incremental-vacuum migration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := EnableIncrementalVacuum(ctx, databases, 1); err != nil {
		t.Fatal(err)
	}
	for _, database := range []*sql.DB{databases.Core, databases.Metrics} {
		var mode int
		if err := database.QueryRow(`PRAGMA auto_vacuum`).Scan(&mode); err != nil || mode != 2 {
			t.Fatalf("migrated auto_vacuum=%d err=%v", mode, err)
		}
	}
	if _, err := NewSpaceRecoveryManager(context.Background(), databases, config, func(SpaceRecoveryObservation) {}); err != nil {
		t.Fatalf("migrated databases rejected: %v", err)
	}
}

func TestSpaceRecoveryVacuumAndTruncatesWithinConfiguredPass(t *testing.T) {
	databases := openTestDatabases(t)
	if _, err := databases.Metrics.Exec(`CREATE TABLE recovery_fixture (id INTEGER PRIMARY KEY, payload BLOB)`); err != nil {
		t.Fatal(err)
	}
	transaction, err := databases.Metrics.Begin()
	if err != nil {
		t.Fatal(err)
	}
	payload := make([]byte, 4096)
	for index := 0; index < 512; index++ {
		if _, err := transaction.Exec(`INSERT INTO recovery_fixture(payload) VALUES (?)`, payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := databases.Metrics.Exec(`DELETE FROM recovery_fixture`); err != nil {
		t.Fatal(err)
	}
	before, err := readDatabaseUsage(context.Background(), databases.Metrics, databases.MetricsPath)
	if err != nil || before.pageCount-before.usedBytes/before.pageSize < 1 {
		t.Fatalf("before=%+v err=%v", before, err)
	}
	observations := make([]SpaceRecoveryObservation, 0, 2)
	manager, err := NewSpaceRecoveryManager(context.Background(), databases, SpaceRecoveryConfig{
		Interval: time.Hour, Timeout: 5 * time.Second, WALTruncateBytes: 1,
		VacuumMinFreePages: 1, VacuumMaxPagesPerRun: 10_000,
	}, func(observation SpaceRecoveryObservation) { observations = append(observations, observation) })
	if err != nil {
		t.Fatal(err)
	}
	manager.Recover(context.Background())
	if len(observations) != 2 {
		t.Fatalf("observations=%+v", observations)
	}
	var metricsObservation SpaceRecoveryObservation
	for _, observation := range observations {
		if observation.Err != nil {
			t.Fatalf("recovery observation=%+v", observation)
		}
		if observation.Database == "metrics" {
			metricsObservation = observation
		}
	}
	if !metricsObservation.VacuumAttempted || !metricsObservation.TruncateAttempted || metricsObservation.WALBytesAfter != 0 || metricsObservation.AllocatedBytesAfter >= metricsObservation.AllocatedBytesBefore {
		t.Fatalf("metrics recovery=%+v", metricsObservation)
	}
}
