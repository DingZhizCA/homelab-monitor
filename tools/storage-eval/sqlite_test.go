/*
Package storageeval compares SQLite driver candidates for the storage PoC.

The benchmarks exercise only local synthetic metric batches. They do not select
production schemas, retention policies, quotas, or target-device parameters.
*/
package storageeval

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	_ "modernc.org/sqlite"
)

const batchSize = 500

type candidate struct {
	name   string
	driver string
}

var candidates = []candidate{
	{name: "mattn_cgo", driver: "sqlite3"},
	{name: "modernc_purego", driver: "sqlite"},
}

func BenchmarkSQLiteBatchWrite(b *testing.B) {
	for _, candidate := range candidates {
		b.Run(candidate.name, func(b *testing.B) {
			db := openDatabase(b, candidate)
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, `CREATE TABLE samples (
				device_id TEXT NOT NULL,
				metric_name TEXT NOT NULL,
				observed_at INTEGER NOT NULL,
				value REAL NOT NULL
			)`); err != nil {
				b.Fatal(err)
			}

			stmt, err := db.PrepareContext(ctx, `INSERT INTO samples
				(device_id, metric_name, observed_at, value) VALUES (?, ?, ?, ?)`)
			if err != nil {
				b.Fatal(err)
			}
			defer stmt.Close()

			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					b.Fatal(err)
				}
				txStmt := tx.StmtContext(ctx, stmt)
				base := time.Now().UnixNano() + int64(iteration*batchSize)
				for row := 0; row < batchSize; row++ {
					if _, err := txStmt.ExecContext(ctx,
						"device-benchmark", "cpu.utilization", base+int64(row), float64(row)/batchSize); err != nil {
						_ = txStmt.Close()
						_ = tx.Rollback()
						b.Fatal(err)
					}
				}
				if err := txStmt.Close(); err != nil {
					_ = tx.Rollback()
					b.Fatal(err)
				}
				if err := tx.Commit(); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(batchSize*b.N)/b.Elapsed().Seconds(), "rows/s")
		})
	}
}

func BenchmarkSQLiteRangeRead(b *testing.B) {
	for _, candidate := range candidates {
		b.Run(candidate.name, func(b *testing.B) {
			db := openDatabase(b, candidate)
			ctx := context.Background()
			if _, err := db.ExecContext(ctx, `CREATE TABLE samples (
				device_id TEXT NOT NULL,
				metric_name TEXT NOT NULL,
				observed_at INTEGER NOT NULL,
				value REAL NOT NULL,
				PRIMARY KEY (device_id, metric_name, observed_at)
			)`); err != nil {
				b.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `CREATE INDEX samples_range
				ON samples (device_id, metric_name, observed_at)`); err != nil {
				b.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `BEGIN`); err != nil {
				b.Fatal(err)
			}
			insert, err := db.PrepareContext(ctx, `INSERT INTO samples
				(device_id, metric_name, observed_at, value) VALUES (?, ?, ?, ?)`)
			if err != nil {
				b.Fatal(err)
			}
			base := time.Now().UnixNano()
			for row := 0; row < 10000; row++ {
				if _, err := insert.ExecContext(ctx, "device-benchmark", "cpu.utilization", base+int64(row), float64(row)/10000); err != nil {
					b.Fatal(err)
				}
			}
			if err := insert.Close(); err != nil {
				b.Fatal(err)
			}
			if _, err := db.ExecContext(ctx, `COMMIT`); err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for iteration := 0; iteration < b.N; iteration++ {
				rows, err := db.QueryContext(ctx, `SELECT observed_at, value FROM samples
					WHERE device_id = ? AND metric_name = ? AND observed_at >= ? AND observed_at < ?
					ORDER BY observed_at`, "device-benchmark", "cpu.utilization", base+4000, base+5000)
				if err != nil {
					b.Fatal(err)
				}
				count := 0
				for rows.Next() {
					var observedAt int64
					var value float64
					if err := rows.Scan(&observedAt, &value); err != nil {
						b.Fatal(err)
					}
					count++
				}
				if err := rows.Close(); err != nil {
					b.Fatal(err)
				}
				if count != 1000 {
					b.Fatalf("range returned %d rows, want 1000", count)
				}
			}
		})
	}
}

func openDatabase(tb testing.TB, candidate candidate) *sql.DB {
	tb.Helper()
	root, err := os.Getwd()
	if err != nil {
		tb.Fatal(err)
	}
	dir, err := os.MkdirTemp(root, ".storage-eval-")
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := os.RemoveAll(dir); err != nil {
			tb.Errorf("remove benchmark database: %v", err)
		}
	})
	dsn := filepath.Join(dir, fmt.Sprintf("%s.db", candidate.name))
	db, err := sql.Open(candidate.driver, dsn)
	if err != nil {
		tb.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	tb.Cleanup(func() {
		if err := db.Close(); err != nil {
			tb.Errorf("close benchmark database: %v", err)
		}
	})
	if err := db.Ping(); err != nil {
		tb.Fatalf("open %s: %v", candidate.name, err)
	}
	if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;`); err != nil {
		tb.Fatalf("configure %s benchmark database: %v", candidate.name, err)
	}
	return db
}
