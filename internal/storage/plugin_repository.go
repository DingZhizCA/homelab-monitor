/*
Package storage persists plugin desired and reported state in core.db.

Owner-authored desired revisions and their audit events commit atomically.
Agent reports commit as whole batches after all revision checks pass.
*/
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

// PluginRepository implements durable plugin control-plane state.
type PluginRepository struct {
	database   *sql.DB
	writeToken chan struct{}
}

// NewPluginRepository binds plugin state to core.db.
func NewPluginRepository(database *sql.DB) (*PluginRepository, error) {
	if database == nil {
		return nil, errors.New("core database is required")
	}
	token := make(chan struct{}, 1)
	token <- struct{}{}
	return &PluginRepository{database: database, writeToken: token}, nil
}

// SetDesiredWithAuditContext atomically rechecks the Owner and publishes a revision.
func (r *PluginRepository) SetDesiredWithAuditContext(ctx context.Context, state plugins.DesiredState, event audit.Event) error {
	if err := validatePluginDesiredMutation(state, event); err != nil {
		return err
	}
	return r.withWrite(ctx, func(transaction *sql.Tx) error {
		if err := requireActiveOwner(ctx, transaction, event.ActorID); err != nil {
			return err
		}
		current, exists, err := readDesiredState(ctx, transaction, state.DeviceID, state.PluginID)
		if err != nil {
			return err
		}
		if exists && state.Revision < current.Revision {
			return fmt.Errorf("%w: desired revision %d is older than current revision %d", plugins.ErrStateConflict, state.Revision, current.Revision)
		}
		if exists && state.Revision == current.Revision {
			if state != current {
				return fmt.Errorf("%w: desired revision %d conflicts with current state", plugins.ErrStateConflict, state.Revision)
			}
			return nil
		}
		enabled := 0
		if state.Enabled {
			enabled = 1
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO plugin_desired_states
			(device_id, plugin_id, revision, version, artifact_os, artifact_arch, artifact_sha256, enabled)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(device_id, plugin_id) DO UPDATE SET
				revision=excluded.revision, version=excluded.version, artifact_os=excluded.artifact_os,
				artifact_arch=excluded.artifact_arch, artifact_sha256=excluded.artifact_sha256, enabled=excluded.enabled`,
			state.DeviceID, state.PluginID, state.Revision, state.Version, state.Artifact.Platform.OS,
			state.Artifact.Platform.Arch, state.Artifact.SHA256, enabled); err != nil {
			return fmt.Errorf("store plugin desired state: %w", err)
		}
		return insertAuditEvent(ctx, transaction, event)
	})
}

// DesiredForDeviceContext returns a stable plugin-ID-ordered desired snapshot.
func (r *PluginRepository) DesiredForDeviceContext(ctx context.Context, deviceID string) ([]plugins.DesiredState, error) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return nil, errors.New("trimmed device ID is required")
	}
	rows, err := r.database.QueryContext(ctx, `SELECT device_id, plugin_id, revision, version, artifact_os, artifact_arch, artifact_sha256, enabled
		FROM plugin_desired_states WHERE device_id = ? ORDER BY plugin_id`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("query plugin desired state: %w", err)
	}
	defer rows.Close()
	states := make([]plugins.DesiredState, 0)
	for rows.Next() {
		state, err := scanDesiredState(rows)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin desired state: %w", err)
	}
	return states, nil
}

// SetReportedBatchContext atomically stores a validated Agent report batch.
func (r *PluginRepository) SetReportedBatchContext(ctx context.Context, states []plugins.ReportedState) error {
	if err := plugins.ValidateReportedBatch(states); err != nil {
		return err
	}
	if len(states) == 0 {
		return nil
	}
	return r.withWrite(ctx, func(transaction *sql.Tx) error {
		for _, state := range states {
			var current uint64
			err := transaction.QueryRowContext(ctx, `SELECT observed_revision FROM plugin_reported_states WHERE device_id = ? AND plugin_id = ?`, state.DeviceID, state.PluginID).Scan(&current)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("read plugin reported revision: %w", err)
			}
			if err == nil && state.ObservedRev < current {
				return fmt.Errorf("%w: observed revision %d is older than current revision %d", plugins.ErrStateConflict, state.ObservedRev, current)
			}
		}
		for _, state := range states {
			if _, err := transaction.ExecContext(ctx, `INSERT INTO plugin_reported_states
				(device_id, plugin_id, observed_revision, applied_revision, lifecycle_state, refusal_reason)
				VALUES (?, ?, ?, ?, ?, ?)
				ON CONFLICT(device_id, plugin_id) DO UPDATE SET observed_revision=excluded.observed_revision,
					applied_revision=excluded.applied_revision, lifecycle_state=excluded.lifecycle_state,
					refusal_reason=excluded.refusal_reason`, state.DeviceID, state.PluginID, state.ObservedRev,
				state.AppliedRev, state.Lifecycle, state.RefusalReason); err != nil {
				return fmt.Errorf("store plugin reported state: %w", err)
			}
		}
		return nil
	})
}

// ReportedForDeviceContext returns a stable plugin-ID-ordered reported snapshot.
func (r *PluginRepository) ReportedForDeviceContext(ctx context.Context, deviceID string) ([]plugins.ReportedState, error) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return nil, errors.New("trimmed device ID is required")
	}
	rows, err := r.database.QueryContext(ctx, `SELECT device_id, plugin_id, observed_revision, applied_revision, lifecycle_state, refusal_reason
		FROM plugin_reported_states WHERE device_id = ? ORDER BY plugin_id`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("query plugin reported state: %w", err)
	}
	defer rows.Close()
	states := make([]plugins.ReportedState, 0)
	for rows.Next() {
		var state plugins.ReportedState
		if err := rows.Scan(&state.DeviceID, &state.PluginID, &state.ObservedRev, &state.AppliedRev, &state.Lifecycle, &state.RefusalReason); err != nil {
			return nil, fmt.Errorf("scan plugin reported state: %w", err)
		}
		if err := plugins.ValidateReportedBatch([]plugins.ReportedState{state}); err != nil {
			return nil, fmt.Errorf("validate stored plugin reported state: %w", err)
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin reported state: %w", err)
	}
	return states, nil
}

func validatePluginDesiredMutation(state plugins.DesiredState, event audit.Event) error {
	if err := state.Validate(); err != nil {
		return err
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate desired-state audit event: %w", err)
	}
	if event.Action != plugins.DesiredStateAuditAction || event.ResourceType != plugins.DesiredStateAuditResourceType || event.ResourceID != plugins.DesiredStateAuditResourceID(state) {
		return errors.New("desired-state audit target does not match plugin state")
	}
	return nil
}

func readDesiredState(ctx context.Context, transaction *sql.Tx, deviceID, pluginID string) (plugins.DesiredState, bool, error) {
	row := transaction.QueryRowContext(ctx, `SELECT device_id, plugin_id, revision, version, artifact_os, artifact_arch, artifact_sha256, enabled
		FROM plugin_desired_states WHERE device_id = ? AND plugin_id = ?`, deviceID, pluginID)
	state, err := scanDesiredState(row)
	if errors.Is(err, sql.ErrNoRows) {
		return plugins.DesiredState{}, false, nil
	}
	if err != nil {
		return plugins.DesiredState{}, false, err
	}
	return state, true, nil
}

func scanDesiredState(row rowScanner) (plugins.DesiredState, error) {
	var state plugins.DesiredState
	var enabled int
	if err := row.Scan(&state.DeviceID, &state.PluginID, &state.Revision, &state.Version, &state.Artifact.Platform.OS, &state.Artifact.Platform.Arch, &state.Artifact.SHA256, &enabled); err != nil {
		return plugins.DesiredState{}, err
	}
	if enabled != 0 && enabled != 1 {
		return plugins.DesiredState{}, errors.New("stored plugin desired state has invalid enabled value")
	}
	state.Enabled = enabled == 1
	if err := state.Validate(); err != nil {
		return plugins.DesiredState{}, fmt.Errorf("validate stored plugin desired state: %w", err)
	}
	return state, nil
}

func (r *PluginRepository) withWrite(ctx context.Context, operation func(*sql.Tx) error) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-r.writeToken:
	}
	defer func() { r.writeToken <- struct{}{} }()
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin plugin state transaction: %w", err)
	}
	defer transaction.Rollback()
	if err := operation(transaction); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit plugin state transaction: %w", err)
	}
	return nil
}
