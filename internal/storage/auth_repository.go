/*
Package storage persists human accounts, server-side sessions, and account
audit events in core.db. Account mutations and their audit records commit in
the same transaction; policy and HTTP behavior stay in the auth/server layers.
*/
package storage

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

const storageTimeLayout = "2006-01-02T15:04:05.000000000Z"

var coreMigrations = []Migration{
	{
		Version: 1,
		Name:    "human_accounts_sessions_and_audit",
		SQL: `CREATE TABLE users (
		user_id TEXT PRIMARY KEY,
		username TEXT NOT NULL UNIQUE,
		role TEXT NOT NULL CHECK (role IN ('owner', 'viewer')),
		password_hash TEXT NOT NULL,
		disabled INTEGER NOT NULL CHECK (disabled IN (0, 1))
	);
	CREATE TABLE sessions (
		token_hash BLOB PRIMARY KEY CHECK (length(token_hash) = 32),
		user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
		issued_at TEXT NOT NULL,
		expires_at TEXT NOT NULL
	);
	CREATE INDEX sessions_user ON sessions (user_id);
	CREATE INDEX sessions_expiry ON sessions (expires_at);
	CREATE TABLE audit_events (
		event_id INTEGER PRIMARY KEY AUTOINCREMENT,
		occurred_at TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		action TEXT NOT NULL,
		resource_type TEXT NOT NULL,
		resource_id TEXT NOT NULL
	);
	CREATE INDEX audit_events_time ON audit_events (occurred_at, event_id);`,
	},
	{
		Version: 2,
		Name:    "plugin_desired_and_reported_state",
		SQL: `CREATE TABLE plugin_desired_states (
		device_id TEXT NOT NULL,
		plugin_id TEXT NOT NULL,
		revision INTEGER NOT NULL CHECK (revision > 0),
		version TEXT NOT NULL,
		artifact_os TEXT NOT NULL,
		artifact_arch TEXT NOT NULL,
		artifact_sha256 TEXT NOT NULL,
		enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
		PRIMARY KEY (device_id, plugin_id)
	);
	CREATE INDEX plugin_desired_device ON plugin_desired_states (device_id, plugin_id);
	CREATE TABLE plugin_reported_states (
		device_id TEXT NOT NULL,
		plugin_id TEXT NOT NULL,
		observed_revision INTEGER NOT NULL CHECK (observed_revision >= 0),
		applied_revision INTEGER NOT NULL CHECK (applied_revision >= 0 AND applied_revision <= observed_revision),
		lifecycle_state TEXT NOT NULL,
		refusal_reason TEXT NOT NULL,
		PRIMARY KEY (device_id, plugin_id)
	);
	CREATE INDEX plugin_reported_device ON plugin_reported_states (device_id, plugin_id);`,
	},
	{
		Version: 3,
		Name:    "dashboard_revisions_and_publication",
		SQL: `CREATE TABLE dashboards (
		dashboard_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		scope TEXT NOT NULL CHECK (scope IN ('personal', 'shared')),
		owner_user_id TEXT NOT NULL REFERENCES users(user_id) ON DELETE CASCADE,
		next_revision INTEGER NOT NULL CHECK (next_revision > 1),
		draft_revision INTEGER NOT NULL CHECK (draft_revision > 0),
		published_revision INTEGER CHECK (published_revision IS NULL OR published_revision > 0)
	);
	CREATE INDEX dashboards_owner ON dashboards (owner_user_id, dashboard_id);
	CREATE INDEX dashboards_shared_publication ON dashboards (scope, published_revision, dashboard_id);
	CREATE TABLE dashboard_revisions (
		dashboard_id TEXT NOT NULL REFERENCES dashboards(dashboard_id) ON DELETE CASCADE,
		revision INTEGER NOT NULL CHECK (revision > 0),
		document_json BLOB NOT NULL,
		published INTEGER NOT NULL CHECK (published IN (0, 1)),
		PRIMARY KEY (dashboard_id, revision)
	);
	CREATE INDEX dashboard_published_revisions ON dashboard_revisions (dashboard_id, published, revision DESC);`,
	},
	{
		Version: 4,
		Name:    "alert_rule_revisions",
		SQL: `CREATE TABLE alert_rules (
		device_id TEXT NOT NULL,
		rule_id TEXT NOT NULL,
		current_revision INTEGER NOT NULL CHECK (current_revision > 0),
		PRIMARY KEY (device_id, rule_id)
	);
	CREATE INDEX alert_rules_device ON alert_rules (device_id, rule_id);
	CREATE TABLE alert_rule_revisions (
		device_id TEXT NOT NULL,
		rule_id TEXT NOT NULL,
		revision INTEGER NOT NULL CHECK (revision > 0),
		rule_json BLOB NOT NULL,
		PRIMARY KEY (device_id, rule_id, revision),
		FOREIGN KEY (device_id, rule_id) REFERENCES alert_rules(device_id, rule_id) ON DELETE CASCADE
	);
	CREATE INDEX alert_rule_history ON alert_rule_revisions (device_id, rule_id, revision DESC);`,
	},
	{
		Version: 5,
		Name:    "incident_current_state",
		SQL: `CREATE TABLE incident_states (
		device_id TEXT NOT NULL,
		rule_id TEXT NOT NULL,
		rule_revision INTEGER NOT NULL CHECK (rule_revision > 0),
		last_evaluated_at TEXT NOT NULL,
		state_json BLOB NOT NULL,
		PRIMARY KEY (device_id, rule_id)
	);
	CREATE INDEX incident_states_device ON incident_states (device_id, rule_id);`,
	},
	{
		Version: 6,
		Name:    "plugin_collection_policies",
		SQL: `CREATE TABLE plugin_collection_policies (
		device_id TEXT NOT NULL,
		plugin_id TEXT NOT NULL,
		collector_id TEXT NOT NULL,
		revision INTEGER NOT NULL CHECK (revision > 0),
		plugin_revision INTEGER NOT NULL CHECK (plugin_revision >= 0),
		enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
		interval_seconds INTEGER NOT NULL CHECK (interval_seconds >= 0),
		timeout_milliseconds INTEGER NOT NULL CHECK (timeout_milliseconds >= 0),
		config_json BLOB NOT NULL,
		PRIMARY KEY (device_id, plugin_id, collector_id)
	);
	CREATE INDEX plugin_collection_policy_device
		ON plugin_collection_policies (device_id, plugin_id, collector_id);`,
	},
	{
		Version: 7,
		Name:    "incident_revision_archives",
		SQL: `CREATE TABLE incident_revision_archives (
		device_id TEXT NOT NULL,
		rule_id TEXT NOT NULL,
		rule_revision INTEGER NOT NULL CHECK (rule_revision > 0),
		superseded_by_revision INTEGER NOT NULL CHECK (superseded_by_revision > rule_revision),
		superseded_at TEXT NOT NULL,
		archive_json BLOB NOT NULL,
		PRIMARY KEY (device_id, rule_id, rule_revision)
	);
	CREATE INDEX incident_archives_device_rule
		ON incident_revision_archives (device_id, rule_id, rule_revision DESC);`,
	},
	{
		Version: 8,
		Name:    "incident_archive_retention_index",
		SQL: `CREATE INDEX incident_archives_superseded_at
		ON incident_revision_archives (superseded_at, device_id, rule_id, rule_revision);`,
	},
	{
		Version: 9,
		Name:    "incident_transitions",
		SQL: `CREATE TABLE incident_transitions (
		event_id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		rule_id TEXT NOT NULL,
		rule_revision INTEGER NOT NULL CHECK (rule_revision > 0),
		occurred_at TEXT NOT NULL,
		from_status TEXT NOT NULL,
		to_status TEXT NOT NULL,
		transition_json BLOB NOT NULL
	);
	CREATE INDEX incident_transitions_rule_time
		ON incident_transitions (device_id, rule_id, occurred_at DESC, event_id DESC);
	CREATE INDEX incident_transitions_retention
		ON incident_transitions (occurred_at, event_id);`,
	},
	{
		Version: 10,
		Name:    "notification_intent_outbox",
		SQL: `CREATE TABLE notification_intent_outbox (
		intent_id TEXT PRIMARY KEY,
		transition_id TEXT NOT NULL UNIQUE,
		device_id TEXT NOT NULL,
		rule_id TEXT NOT NULL,
		rule_revision INTEGER NOT NULL CHECK (rule_revision > 0),
		kind TEXT NOT NULL CHECK (kind IN ('firing', 'recovery')),
		delivery_policy TEXT NOT NULL CHECK (delivery_policy IN ('push_eligible', 'record_only')),
		created_at TEXT NOT NULL,
		intent_json BLOB NOT NULL
	);
	CREATE INDEX notification_intent_outbox_order
		ON notification_intent_outbox (created_at, intent_id);`,
	},
}

// AuthRepository implements the auth user and session persistence boundaries.
type AuthRepository struct {
	database   *sql.DB
	writeToken chan struct{}
}

var _ auth.UserRepository = (*AuthRepository)(nil)
var _ auth.SessionRepository = (*AuthRepository)(nil)

// NewAuthRepository binds account and session operations to core.db.
func NewAuthRepository(database *sql.DB) (*AuthRepository, error) {
	if database == nil {
		return nil, errors.New("core database is required")
	}
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return &AuthRepository{database: database, writeToken: token}, nil
}

// CreateInitialOwner atomically creates the first Owner and matching audit event.
func (r *AuthRepository) CreateInitialOwner(ctx context.Context, user auth.User, event audit.Event) error {
	if err := validateAuthMutation(user, event, user.ID, user.ID); err != nil || user.Role != auth.RoleOwner || user.Disabled {
		if err != nil {
			return err
		}
		return auth.ErrInvalidAccount
	}
	return r.withWrite(ctx, func(transaction *sql.Tx) error {
		var exists bool
		if err := transaction.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE role = 'owner')`).Scan(&exists); err != nil {
			return fmt.Errorf("inspect initial Owner: %w", err)
		}
		if exists {
			return auth.ErrOwnerExists
		}
		if err := insertUser(ctx, transaction, user); err != nil {
			return err
		}
		return insertAuditEvent(ctx, transaction, event)
	})
}

// CreateForOwner atomically rechecks the actor and creates an account with audit.
func (r *AuthRepository) CreateForOwner(ctx context.Context, actorID string, user auth.User, event audit.Event) error {
	if err := validateAuthMutation(user, event, actorID, user.ID); err != nil {
		return err
	}
	return r.withWrite(ctx, func(transaction *sql.Tx) error {
		if err := requireActiveOwner(ctx, transaction, actorID); err != nil {
			return err
		}
		if err := insertUser(ctx, transaction, user); err != nil {
			return err
		}
		return insertAuditEvent(ctx, transaction, event)
	})
}

// ListForOwner returns all accounts after checking the actor's current state.
func (r *AuthRepository) ListForOwner(ctx context.Context, actorID string) ([]auth.User, error) {
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(actorID) != actorID {
		return nil, auth.ErrForbidden
	}
	transaction, err := r.database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin user list: %w", err)
	}
	defer transaction.Rollback()
	if err := requireActiveOwner(ctx, transaction, actorID); err != nil {
		return nil, err
	}
	rows, err := transaction.QueryContext(ctx, `SELECT user_id, username, role, password_hash, disabled FROM users ORDER BY username`)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	var users []auth.User
	for rows.Next() {
		user, err := scanUser(rows)
		if err != nil {
			_ = rows.Close()
			return nil, err
		}
		users = append(users, user)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("iterate users: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close user list: %w", err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("finish user list: %w", err)
	}
	return users, nil
}

// UpdateAccountState atomically enforces current-Owner and last-Owner rules.
func (r *AuthRepository) UpdateAccountState(ctx context.Context, actorID, userID string, role auth.Role, disabled bool, event audit.Event) error {
	if role != auth.RoleOwner && role != auth.RoleViewer {
		return auth.ErrInvalidAccount
	}
	if err := event.Validate(); err != nil || event.ActorID != actorID || event.ResourceType != "user" || event.ResourceID != userID {
		return auth.ErrInvalidAccount
	}
	return r.withWrite(ctx, func(transaction *sql.Tx) error {
		if err := requireActiveOwner(ctx, transaction, actorID); err != nil {
			return err
		}
		current, found, err := userByID(ctx, transaction, userID)
		if err != nil {
			return err
		}
		if !found {
			return auth.ErrAccountNotFound
		}
		if current.Role == auth.RoleOwner && !current.Disabled && (role != auth.RoleOwner || disabled) {
			var activeOwners int
			if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM users WHERE role = 'owner' AND disabled = 0`).Scan(&activeOwners); err != nil {
				return fmt.Errorf("count active Owners: %w", err)
			}
			if activeOwners <= 1 {
				return auth.ErrLastOwner
			}
		}
		if _, err := transaction.ExecContext(ctx, `UPDATE users SET role = ?, disabled = ? WHERE user_id = ?`, role, boolInteger(disabled), userID); err != nil {
			return fmt.Errorf("update user: %w", err)
		}
		return insertAuditEvent(ctx, transaction, event)
	})
}

// ByUsername looks up one account using its exact, case-sensitive identity.
func (r *AuthRepository) ByUsername(ctx context.Context, username string) (auth.User, bool, error) {
	return queryUser(ctx, r.database, `SELECT user_id, username, role, password_hash, disabled FROM users WHERE username = ?`, username)
}

// ByID looks up one account by its stable ID.
func (r *AuthRepository) ByID(ctx context.Context, userID string) (auth.User, bool, error) {
	return userByID(ctx, r.database, userID)
}

// Create stores a server-side session token digest.
func (r *AuthRepository) Create(ctx context.Context, session auth.Session) error {
	if session.UserID == "" || session.IssuedAt.IsZero() || !session.IssuedAt.Before(session.ExpiresAt) {
		return errors.New("valid session identity and time range are required")
	}
	return r.withWrite(ctx, func(transaction *sql.Tx) error {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO sessions (token_hash, user_id, issued_at, expires_at)
			VALUES (?, ?, ?, ?)
			ON CONFLICT(token_hash) DO UPDATE SET user_id=excluded.user_id, issued_at=excluded.issued_at, expires_at=excluded.expires_at`,
			session.TokenHash[:], session.UserID, storageTimestamp(session.IssuedAt), storageTimestamp(session.ExpiresAt)); err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		return nil
	})
}

// ByTokenHash returns one stored server-side session.
func (r *AuthRepository) ByTokenHash(ctx context.Context, tokenHash [sha256.Size]byte) (auth.Session, bool, error) {
	var encodedHash []byte
	var session auth.Session
	var issuedAt, expiresAt string
	err := r.database.QueryRowContext(ctx, `SELECT token_hash, user_id, issued_at, expires_at FROM sessions WHERE token_hash = ?`, tokenHash[:]).Scan(&encodedHash, &session.UserID, &issuedAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Session{}, false, nil
	}
	if err != nil {
		return auth.Session{}, false, fmt.Errorf("find session: %w", err)
	}
	if len(encodedHash) != sha256.Size {
		return auth.Session{}, false, errors.New("stored session token digest has invalid length")
	}
	copy(session.TokenHash[:], encodedHash)
	session.IssuedAt, err = parseStorageTimestamp(issuedAt)
	if err != nil {
		return auth.Session{}, false, err
	}
	session.ExpiresAt, err = parseStorageTimestamp(expiresAt)
	if err != nil {
		return auth.Session{}, false, err
	}
	return session, true, nil
}

// Revoke removes one session if present.
func (r *AuthRepository) Revoke(ctx context.Context, tokenHash [sha256.Size]byte) error {
	return r.withWrite(ctx, func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(ctx, `DELETE FROM sessions WHERE token_hash = ?`, tokenHash[:])
		return err
	})
}

// RevokeUser removes every session owned by one account.
func (r *AuthRepository) RevokeUser(ctx context.Context, userID string) error {
	return r.withWrite(ctx, func(transaction *sql.Tx) error {
		_, err := transaction.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
		return err
	})
}

func (r *AuthRepository) withWrite(ctx context.Context, operation func(*sql.Tx) error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.writeToken:
	}
	defer func() { r.writeToken <- struct{}{} }()
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	if err := operation(transaction); err != nil {
		return err
	}
	return transaction.Commit()
}

type rowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validateAuthMutation(user auth.User, event audit.Event, actorID, resourceID string) error {
	if strings.TrimSpace(user.ID) == "" || strings.TrimSpace(user.ID) != user.ID || strings.TrimSpace(user.Username) == "" || strings.TrimSpace(user.Username) != user.Username || user.PasswordHash == "" || (user.Role != auth.RoleOwner && user.Role != auth.RoleViewer) {
		return auth.ErrInvalidAccount
	}
	if err := event.Validate(); err != nil || event.ActorID != actorID || event.ResourceType != "user" || event.ResourceID != resourceID {
		return auth.ErrInvalidAccount
	}
	return nil
}

func insertUser(ctx context.Context, transaction *sql.Tx, user auth.User) error {
	var usernameExists bool
	if err := transaction.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM users WHERE username = ?)`, user.Username).Scan(&usernameExists); err != nil {
		return fmt.Errorf("inspect username: %w", err)
	}
	if usernameExists {
		return auth.ErrUsernameExists
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO users (user_id, username, role, password_hash, disabled) VALUES (?, ?, ?, ?, ?)`, user.ID, user.Username, user.Role, user.PasswordHash, boolInteger(user.Disabled)); err != nil {
		return fmt.Errorf("insert user: %w", err)
	}
	return nil
}

func requireActiveOwner(ctx context.Context, querier rowQuerier, actorID string) error {
	var role auth.Role
	var disabled int
	err := querier.QueryRowContext(ctx, `SELECT role, disabled FROM users WHERE user_id = ?`, actorID).Scan(&role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("verify current Owner: %w", err)
	}
	if role != auth.RoleOwner || disabled != 0 {
		return auth.ErrForbidden
	}
	return nil
}

func userByID(ctx context.Context, querier rowQuerier, userID string) (auth.User, bool, error) {
	return queryUser(ctx, querier, `SELECT user_id, username, role, password_hash, disabled FROM users WHERE user_id = ?`, userID)
}

func queryUser(ctx context.Context, querier rowQuerier, query string, value any) (auth.User, bool, error) {
	user, err := scanUser(querier.QueryRowContext(ctx, query, value))
	if errors.Is(err, sql.ErrNoRows) {
		return auth.User{}, false, nil
	}
	if err != nil {
		return auth.User{}, false, fmt.Errorf("find user: %w", err)
	}
	return user, true, nil
}

type rowScanner interface{ Scan(...any) error }

func scanUser(row rowScanner) (auth.User, error) {
	var user auth.User
	var disabled int
	if err := row.Scan(&user.ID, &user.Username, &user.Role, &user.PasswordHash, &disabled); err != nil {
		return auth.User{}, err
	}
	if disabled != 0 && disabled != 1 {
		return auth.User{}, errors.New("stored user disabled state is invalid")
	}
	user.Disabled = disabled == 1
	return user, nil
}

func insertAuditEvent(ctx context.Context, transaction *sql.Tx, event audit.Event) error {
	if _, err := transaction.ExecContext(ctx, `INSERT INTO audit_events (occurred_at, actor_id, action, resource_type, resource_id) VALUES (?, ?, ?, ?, ?)`, storageTimestamp(event.OccurredAt), event.ActorID, event.Action, event.ResourceType, event.ResourceID); err != nil {
		return fmt.Errorf("insert audit event: %w", err)
	}
	return nil
}

func parseStorageTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(storageTimeLayout, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse stored timestamp: %w", err)
	}
	return parsed, nil
}

func boolInteger(value bool) int {
	if value {
		return 1
	}
	return 0
}
