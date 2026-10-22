/*
Package storage owns the Server's SQLite connection and migration foundation.

It keeps control-plane and metric data in separate local files, applies the
approved WAL durability settings on every pooled connection, and does not
define retention, quota, or domain business policy.
*/
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "github.com/mattn/go-sqlite3"
)

const sqliteDriver = "sqlite3"

// Databases is the Server's isolated pair of control-plane and metric stores.
type Databases struct {
	Core        *sql.DB
	Metrics     *sql.DB
	CorePath    string
	MetricsPath string
}

// Open creates or opens core.db and metrics.db below one private local directory.
func Open(ctx context.Context, directory string) (*Databases, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if directory == "" {
		return nil, errors.New("storage directory is required")
	}
	absoluteDirectory, err := filepath.Abs(directory)
	if err != nil {
		return nil, fmt.Errorf("resolve storage directory: %w", err)
	}
	if err := os.MkdirAll(absoluteDirectory, 0o700); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	info, err := os.Stat(absoluteDirectory)
	if err != nil {
		return nil, fmt.Errorf("inspect storage directory: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("storage path is not a directory")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("storage directory permissions %04o allow group or other access", info.Mode().Perm())
	}

	databases := &Databases{
		CorePath:    filepath.Join(absoluteDirectory, "core.db"),
		MetricsPath: filepath.Join(absoluteDirectory, "metrics.db"),
	}
	databases.Core, err = openSQLite(ctx, databases.CorePath, "FULL")
	if err != nil {
		return nil, fmt.Errorf("open core database: %w", err)
	}
	databases.Metrics, err = openSQLite(ctx, databases.MetricsPath, "NORMAL")
	if err != nil {
		_ = databases.Core.Close()
		return nil, fmt.Errorf("open metrics database: %w", err)
	}
	if err := ApplyMigrations(ctx, databases.Core, coreMigrations); err != nil {
		_ = databases.Close()
		return nil, fmt.Errorf("initialize core migrations: %w", err)
	}
	if err := ApplyMigrations(ctx, databases.Metrics, metricMigrations); err != nil {
		_ = databases.Close()
		return nil, fmt.Errorf("initialize metrics migrations: %w", err)
	}
	return databases, nil
}

func openSQLite(ctx context.Context, path, synchronous string) (*sql.DB, error) {
	fileURL := &url.URL{Scheme: "file", Path: path}
	query := fileURL.Query()
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", synchronous)
	fileURL.RawQuery = query.Encode()
	database, err := sql.Open(sqliteDriver, fileURL.String())
	if err != nil {
		return nil, err
	}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := verifySettings(ctx, database, synchronous); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func verifySettings(ctx context.Context, database *sql.DB, synchronous string) error {
	var journalMode, actualSynchronous string
	var foreignKeys int
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal mode: %w", err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&actualSynchronous); err != nil {
		return fmt.Errorf("read synchronous mode: %w", err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign-key mode: %w", err)
	}
	expectedSynchronous := map[string]string{"FULL": "2", "NORMAL": "1"}[synchronous]
	if journalMode != "wal" || actualSynchronous != expectedSynchronous || foreignKeys != 1 {
		return fmt.Errorf("unexpected SQLite settings: journal_mode=%s synchronous=%s foreign_keys=%d", journalMode, actualSynchronous, foreignKeys)
	}
	return nil
}

// Close releases both database pools and returns every close failure.
func (d *Databases) Close() error {
	if d == nil {
		return nil
	}
	var errs []error
	if d.Metrics != nil {
		errs = append(errs, d.Metrics.Close())
	}
	if d.Core != nil {
		errs = append(errs, d.Core.Close())
	}
	return errors.Join(errs...)
}
