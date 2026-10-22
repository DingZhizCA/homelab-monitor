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
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/mattn/go-sqlite3"
)

const (
	sqliteDriver        = "homelab-monitor-sqlite3"
	sqliteMaxBytesParam = "_homelab_max_bytes"
)

func init() {
	sql.Register(sqliteDriver, &configuredSQLiteDriver{})
}

type configuredSQLiteDriver struct{}

func (*configuredSQLiteDriver) Open(dataSourceName string) (driver.Conn, error) {
	parsed, err := url.Parse(dataSourceName)
	if err != nil {
		return nil, err
	}
	query := parsed.Query()
	maxBytesText := query.Get(sqliteMaxBytesParam)
	query.Del(sqliteMaxBytesParam)
	parsed.RawQuery = query.Encode()
	connection, err := (&sqlite3.SQLiteDriver{}).Open(parsed.String())
	if err != nil {
		return nil, err
	}
	sqliteConnection, ok := connection.(*sqlite3.SQLiteConn)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("SQLite driver returned an unexpected connection type")
	}
	closeOnError := true
	defer func() {
		if closeOnError {
			_ = sqliteConnection.Close()
		}
	}()
	if _, err := sqliteConnection.Exec("PRAGMA wal_autocheckpoint=0", nil); err != nil {
		return nil, err
	}
	if maxBytesText != "" {
		maxBytes, err := strconv.ParseInt(maxBytesText, 10, 64)
		if err != nil || maxBytes < 1 {
			return nil, errors.New("SQLite maximum bytes must be a positive integer")
		}
		pageSize, err := sqliteConnectionPragmaInt(sqliteConnection, "page_size")
		if err != nil {
			return nil, err
		}
		pageCount, err := sqliteConnectionPragmaInt(sqliteConnection, "page_count")
		if err != nil {
			return nil, err
		}
		maxPages := maxBytes / pageSize
		if maxPages < 1 || pageCount > maxPages {
			return nil, fmt.Errorf("SQLite database uses %d pages but configured limit allows %d", pageCount, maxPages)
		}
		if _, err := sqliteConnection.Exec(fmt.Sprintf("PRAGMA max_page_count=%d", maxPages), nil); err != nil {
			return nil, err
		}
		applied, err := sqliteConnectionPragmaInt(sqliteConnection, "max_page_count")
		if err != nil {
			return nil, err
		}
		if applied != maxPages {
			return nil, fmt.Errorf("SQLite applied page limit %d instead of %d", applied, maxPages)
		}
	}
	closeOnError = false
	return sqliteConnection, nil
}

func sqliteConnectionPragmaInt(connection *sqlite3.SQLiteConn, name string) (int64, error) {
	rows, err := connection.Query("PRAGMA "+name, nil)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	values := make([]driver.Value, 1)
	if err := rows.Next(values); err != nil {
		if errors.Is(err, io.EOF) {
			return 0, fmt.Errorf("PRAGMA %s returned no value", name)
		}
		return 0, err
	}
	value, ok := values[0].(int64)
	if !ok || value < 0 {
		return 0, fmt.Errorf("PRAGMA %s returned invalid value %v", name, values[0])
	}
	return value, nil
}

// Databases is the Server's isolated pair of control-plane and metric stores.
type Databases struct {
	Core        *sql.DB
	Metrics     *sql.DB
	CorePath    string
	MetricsPath string
}

// Open creates or opens core.db and metrics.db below one private local directory.
func Open(ctx context.Context, directory string) (*Databases, error) {
	return OpenWithPageLimits(ctx, directory, 0, 0)
}

// OpenWithPageLimits applies explicit byte limits to every pooled connection.
func OpenWithPageLimits(ctx context.Context, directory string, coreMaxBytes, metricsMaxBytes int64) (*Databases, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if coreMaxBytes < 0 || metricsMaxBytes < 0 || (coreMaxBytes == 0) != (metricsMaxBytes == 0) {
		return nil, errors.New("SQLite page limits must both be zero or both be positive")
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
	databases.Core, err = openSQLite(ctx, databases.CorePath, "FULL", coreMaxBytes)
	if err != nil {
		return nil, fmt.Errorf("open core database: %w", err)
	}
	// Serialize core.db access across independently constructed domain repositories.
	// This avoids cross-repository SQLite writer collisions without a hidden retry timeout.
	databases.Core.SetMaxOpenConns(1)
	databases.Core.SetMaxIdleConns(1)
	databases.Metrics, err = openSQLite(ctx, databases.MetricsPath, "NORMAL", metricsMaxBytes)
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

func openSQLite(ctx context.Context, path, synchronous string, maxBytes ...int64) (*sql.DB, error) {
	fileURL := &url.URL{Scheme: "file", Path: path}
	query := fileURL.Query()
	query.Set("_foreign_keys", "on")
	query.Set("_journal_mode", "WAL")
	query.Set("_synchronous", synchronous)
	configuredMaxBytes := int64(0)
	if len(maxBytes) > 0 {
		configuredMaxBytes = maxBytes[0]
	}
	if configuredMaxBytes < 0 {
		return nil, errors.New("SQLite maximum bytes must not be negative")
	}
	if configuredMaxBytes > 0 {
		query.Set(sqliteMaxBytesParam, strconv.FormatInt(configuredMaxBytes, 10))
	}
	fileURL.RawQuery = query.Encode()
	database, err := sql.Open(sqliteDriver, fileURL.String())
	if err != nil {
		return nil, err
	}
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, err
	}
	if err := verifySettings(ctx, database, synchronous, configuredMaxBytes); err != nil {
		_ = database.Close()
		return nil, err
	}
	return database, nil
}

func verifySettings(ctx context.Context, database *sql.DB, synchronous string, maxBytes int64) error {
	var journalMode, actualSynchronous string
	var foreignKeys, autoCheckpoint int
	if err := database.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode); err != nil {
		return fmt.Errorf("read journal mode: %w", err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&actualSynchronous); err != nil {
		return fmt.Errorf("read synchronous mode: %w", err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign-key mode: %w", err)
	}
	if err := database.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&autoCheckpoint); err != nil {
		return fmt.Errorf("read WAL auto-checkpoint mode: %w", err)
	}
	expectedSynchronous := map[string]string{"FULL": "2", "NORMAL": "1"}[synchronous]
	if journalMode != "wal" || actualSynchronous != expectedSynchronous || foreignKeys != 1 || autoCheckpoint != 0 {
		return fmt.Errorf("unexpected SQLite settings: journal_mode=%s synchronous=%s foreign_keys=%d wal_autocheckpoint=%d", journalMode, actualSynchronous, foreignKeys, autoCheckpoint)
	}
	if maxBytes > 0 {
		var pageSize, maxPages int64
		if err := database.QueryRowContext(ctx, "PRAGMA page_size").Scan(&pageSize); err != nil {
			return fmt.Errorf("read SQLite page size: %w", err)
		}
		if err := database.QueryRowContext(ctx, "PRAGMA max_page_count").Scan(&maxPages); err != nil {
			return fmt.Errorf("read SQLite page limit: %w", err)
		}
		if maxPages != maxBytes/pageSize {
			return fmt.Errorf("unexpected SQLite page limit: max_page_count=%d page_size=%d max_bytes=%d", maxPages, pageSize, maxBytes)
		}
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
