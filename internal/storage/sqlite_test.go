/*
Package storage tests the approved dual-database settings and transactional
migration behavior without connecting to any production service or device.
*/
package storage

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

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
	for _, path := range []string{databases.CorePath, databases.MetricsPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("database file %s: %v", path, err)
		}
	}
	assertSQLiteSettings(t, databases.Core, "wal", 2)
	assertSQLiteSettings(t, databases.Metrics, "wal", 1)
	for name, database := range map[string]*sql.DB{"core": databases.Core, "metrics": databases.Metrics} {
		var count int
		if err := database.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil {
			t.Fatalf("%s migration ledger: %v", name, err)
		}
		if count != 0 {
			t.Fatalf("%s has %d unexpected domain migrations", name, count)
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

func TestApplyMigrationsIsTransactionalAndIdempotent(t *testing.T) {
	databases, err := Open(context.Background(), filepath.Join(testutil.TempDir(t), "storage"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	migrations := []Migration{{Version: 1, Name: "create_fixture", SQL: `CREATE TABLE fixture (id INTEGER PRIMARY KEY, value TEXT NOT NULL)`}}
	if err := ApplyMigrations(context.Background(), databases.Core, migrations); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(context.Background(), databases.Core, migrations); err != nil {
		t.Fatalf("repeat migrations: %v", err)
	}
	var count int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("migration count=%d err=%v", count, err)
	}

	failing := append(migrations, Migration{Version: 2, Name: "rollback_fixture", SQL: `CREATE TABLE must_rollback (id INTEGER); INSERT INTO missing_table VALUES (1)`})
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
	if err := ApplyMigrations(context.Background(), databases.Core, []Migration{{Version: 1, Name: "original", SQL: `CREATE TABLE fixture (id INTEGER)`}}); err != nil {
		t.Fatal(err)
	}
	if err := ApplyMigrations(context.Background(), databases.Core, []Migration{{Version: 1, Name: "changed", SQL: `SELECT 1`}}); err == nil {
		t.Fatal("changed migration identity was accepted")
	}
	if err := ApplyMigrations(context.Background(), databases.Core, []Migration{{Version: 2, Name: "second", SQL: `SELECT 1`}, {Version: 1, Name: "first", SQL: `SELECT 1`}}); err == nil {
		t.Fatal("out-of-order migrations were accepted")
	}
}

func assertSQLiteSettings(t *testing.T, database *sql.DB, wantJournal string, wantSynchronous int) {
	t.Helper()
	var journal string
	var synchronous, foreignKeys int
	if err := database.QueryRow(`PRAGMA journal_mode`).Scan(&journal); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA synchronous`).Scan(&synchronous); err != nil {
		t.Fatal(err)
	}
	if err := database.QueryRow(`PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if journal != wantJournal || synchronous != wantSynchronous || foreignKeys != 1 {
		t.Fatalf("journal=%s synchronous=%d foreign_keys=%d", journal, synchronous, foreignKeys)
	}
}
