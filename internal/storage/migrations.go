/*
Package storage applies ordered, transactional SQLite schema migrations.

Domain packages provide migration contents; this file only enforces ordering,
records immutable version names, and prevents partially applied migrations.
*/
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// Migration is one immutable, monotonically increasing schema change.
type Migration struct {
	Version uint64
	Name    string
	SQL     string
}

// ApplyMigrations applies each missing migration once in its own transaction.
func ApplyMigrations(ctx context.Context, database *sql.DB, migrations []Migration) error {
	if database == nil {
		return errors.New("database is required")
	}
	if err := validateMigrations(migrations); err != nil {
		return err
	}
	if _, err := database.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return fmt.Errorf("create migration ledger: %w", err)
	}
	applied, err := appliedMigrations(ctx, database)
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		if existingName, exists := applied[migration.Version]; exists {
			if existingName != migration.Name {
				return fmt.Errorf("migration %d was applied as %q, not %q", migration.Version, existingName, migration.Name)
			}
			continue
		}
		transaction, err := database.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %d: %w", migration.Version, err)
		}
		if _, err := transaction.ExecContext(ctx, migration.SQL); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply migration %d (%s): %w", migration.Version, migration.Name, err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, migration.Version, migration.Name); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("record migration %d: %w", migration.Version, err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit migration %d: %w", migration.Version, err)
		}
	}
	return nil
}

func validateMigrations(migrations []Migration) error {
	var previous uint64
	for index, migration := range migrations {
		if migration.Version == 0 || migration.Version <= previous {
			return fmt.Errorf("migration at index %d must have a positive, increasing version", index)
		}
		if strings.TrimSpace(migration.Name) == "" || strings.TrimSpace(migration.SQL) == "" {
			return fmt.Errorf("migration %d requires a name and SQL", migration.Version)
		}
		previous = migration.Version
	}
	return nil
}

func appliedMigrations(ctx context.Context, database *sql.DB) (map[uint64]string, error) {
	rows, err := database.QueryContext(ctx, `SELECT version, name FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()
	applied := make(map[uint64]string)
	for rows.Next() {
		var version uint64
		var name string
		if err := rows.Scan(&version, &name); err != nil {
			return nil, fmt.Errorf("scan migration ledger: %w", err)
		}
		applied[version] = name
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate migration ledger: %w", err)
	}
	return applied, nil
}
