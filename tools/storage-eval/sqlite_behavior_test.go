/*
Package storageeval verifies SQLite candidate transaction behavior separately
from its throughput benchmarks. The fixture schema is not a production schema.
*/
package storageeval

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
	_ "modernc.org/sqlite"
)

func TestSQLiteCandidatesProvideAtomicIdempotentBatchTransactions(t *testing.T) {
	for _, candidate := range candidates {
		t.Run(candidate.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "storage.db")
			db, err := sql.Open(candidate.driver, dbPath)
			if err != nil {
				t.Fatalf("open candidate database: %v", err)
			}
			t.Cleanup(func() { _ = db.Close() })
			db.SetMaxOpenConns(1)
			if err := db.Ping(); err != nil {
				t.Fatalf("ping candidate database: %v", err)
			}
			if _, err := db.Exec(`PRAGMA journal_mode=WAL; PRAGMA synchronous=NORMAL;
				CREATE TABLE batches (batch_id TEXT PRIMARY KEY, content_hash TEXT NOT NULL);
				CREATE TABLE samples (
					batch_id TEXT NOT NULL REFERENCES batches(batch_id),
					sequence INTEGER NOT NULL,
					value REAL NOT NULL CHECK (value >= 0),
					PRIMARY KEY (batch_id, sequence)
				);`); err != nil {
				t.Fatalf("prepare candidate schema: %v", err)
			}

			ctx := context.Background()
			if inserted, err := appendEvalBatch(ctx, db, "batch-a", "hash-a", []float64{1, 2}); err != nil || !inserted {
				t.Fatalf("commit valid batch inserted=%t err=%v", inserted, err)
			}
			if inserted, err := appendEvalBatch(ctx, db, "batch-a", "hash-a", []float64{1, 2}); err != nil || inserted {
				t.Fatalf("identical retry inserted=%t err=%v, want deduplicated", inserted, err)
			}
			if _, err := appendEvalBatch(ctx, db, "batch-a", "different-hash", []float64{3}); !errors.Is(err, errEvalBatchConflict) {
				t.Fatalf("conflicting retry error=%v, want ErrEvalBatchConflict", err)
			}
			if _, err := appendEvalBatch(ctx, db, "batch-b", "hash-b", []float64{4, -1}); err == nil {
				t.Fatal("invalid sample unexpectedly committed")
			}

			var batchCount, sampleCount int
			if err := db.QueryRow(`SELECT COUNT(*) FROM batches`).Scan(&batchCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM samples`).Scan(&sampleCount); err != nil {
				t.Fatal(err)
			}
			if batchCount != 1 || sampleCount != 2 {
				t.Fatalf("stored batches=%d samples=%d after duplicate and rollback, want 1 and 2", batchCount, sampleCount)
			}
			if err := db.Close(); err != nil {
				t.Fatalf("close candidate database: %v", err)
			}
			db, err = sql.Open(candidate.driver, dbPath)
			if err != nil {
				t.Fatalf("reopen candidate database: %v", err)
			}
			db.SetMaxOpenConns(1)
			if err := db.Ping(); err != nil {
				t.Fatalf("ping reopened candidate database: %v", err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM batches`).Scan(&batchCount); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRow(`SELECT COUNT(*) FROM samples`).Scan(&sampleCount); err != nil {
				t.Fatal(err)
			}
			if batchCount != 1 || sampleCount != 2 {
				t.Fatalf("reopened database contains batches=%d samples=%d, want 1 and 2", batchCount, sampleCount)
			}
		})
	}
}

var errEvalBatchConflict = errors.New("batch ID conflicts with committed content")

func appendEvalBatch(ctx context.Context, db *sql.DB, batchID, contentHash string, values []float64) (bool, error) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var existingHash string
	err = tx.QueryRowContext(ctx, `SELECT content_hash FROM batches WHERE batch_id = ?`, batchID).Scan(&existingHash)
	if err == nil {
		if existingHash != contentHash {
			return false, errEvalBatchConflict
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("inspect previous batch: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO batches (batch_id, content_hash) VALUES (?, ?)`, batchID, contentHash); err != nil {
		return false, fmt.Errorf("insert batch identity: %w", err)
	}
	for sequence, value := range values {
		if _, err := tx.ExecContext(ctx, `INSERT INTO samples (batch_id, sequence, value) VALUES (?, ?, ?)`, batchID, sequence, value); err != nil {
			return false, fmt.Errorf("insert sample %d: %w", sequence, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit batch: %w", err)
	}
	return true, nil
}
