/*
Package storage persists current Incident transition state in core.db with an
explicit row cap and timestamp compare-and-swap. It delegates all transition
semantics to alerts.AdvanceIncident and does not create notification policy.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
)

// IncidentRepository stores bounded current Incident state in core.db.
type IncidentRepository struct {
	database   *sql.DB
	maxEntries int
}

var _ alerts.IncidentRepository = (*IncidentRepository)(nil)

// NewIncidentRepository requires an explicit maximum number of rule states.
func NewIncidentRepository(database *sql.DB, maxEntries int) (*IncidentRepository, error) {
	if database == nil || maxEntries < 1 {
		return nil, errors.New("core database and positive Incident capacity are required")
	}
	return &IncidentRepository{database: database, maxEntries: maxEntries}, nil
}

func (r *IncidentRepository) Current(ctx context.Context, deviceID, ruleID string) (alerts.IncidentState, bool, error) {
	if err := validateIncidentIdentity(deviceID, ruleID); err != nil {
		return alerts.IncidentState{}, false, err
	}
	var encoded []byte
	err := r.database.QueryRowContext(ctx, `SELECT state_json FROM incident_states WHERE device_id = ? AND rule_id = ?`, deviceID, ruleID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return alerts.IncidentState{}, false, nil
	}
	if err != nil {
		return alerts.IncidentState{}, false, fmt.Errorf("read Incident state: %w", err)
	}
	state, err := decodeIncidentState(encoded)
	return state, err == nil, err
}

func (r *IncidentRepository) ListCurrent(ctx context.Context, deviceID string) ([]alerts.IncidentState, error) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return nil, alerts.ErrIncidentIdentity
	}
	rows, err := r.database.QueryContext(ctx, `SELECT state_json FROM incident_states WHERE device_id = ? ORDER BY rule_id`, deviceID)
	if err != nil {
		return nil, fmt.Errorf("list Incident states: %w", err)
	}
	defer rows.Close()
	states := make([]alerts.IncidentState, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan Incident state: %w", err)
		}
		state, err := decodeIncidentState(encoded)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Incident states: %w", err)
	}
	return states, nil
}

func (r *IncidentRepository) Advance(ctx context.Context, rule alerts.Rule, result alerts.EvaluationResult, at time.Time, policy alerts.IncidentPolicy, expectedLastEvaluatedAt time.Time) (alerts.IncidentState, error) {
	if err := validateIncidentIdentity(rule.DeviceID, rule.ID); err != nil {
		return alerts.IncidentState{}, err
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return alerts.IncidentState{}, fmt.Errorf("begin Incident transition: %w", err)
	}
	defer transaction.Rollback()
	var encoded []byte
	err = transaction.QueryRowContext(ctx, `SELECT state_json FROM incident_states WHERE device_id = ? AND rule_id = ?`, rule.DeviceID, rule.ID).Scan(&encoded)
	exists := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return alerts.IncidentState{}, fmt.Errorf("read current Incident transition: %w", err)
	}
	var current alerts.IncidentState
	if exists {
		current, err = decodeIncidentState(encoded)
		if err != nil {
			return alerts.IncidentState{}, err
		}
		if current.LastEvaluatedAt.Equal(at.UTC()) {
			if current.LastEvaluation != result.State || current.RuleRevision != rule.Revision {
				return alerts.IncidentState{}, alerts.ErrIncidentVersionConflict
			}
			next, err := alerts.AdvanceIncident(current, rule, result, at, policy)
			if err != nil || next != current {
				if err != nil {
					return alerts.IncidentState{}, err
				}
				return alerts.IncidentState{}, alerts.ErrIncidentVersionConflict
			}
			return current, transaction.Commit()
		}
	}
	if (!exists && !expectedLastEvaluatedAt.IsZero()) || (exists && !current.LastEvaluatedAt.Equal(expectedLastEvaluatedAt.UTC())) {
		return alerts.IncidentState{}, alerts.ErrIncidentVersionConflict
	}
	next, err := alerts.AdvanceIncident(current, rule, result, at, policy)
	if err != nil {
		return alerts.IncidentState{}, err
	}
	nextJSON, err := json.Marshal(next)
	if err != nil {
		return alerts.IncidentState{}, fmt.Errorf("encode Incident state: %w", err)
	}
	if !exists {
		var count int
		if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM incident_states`).Scan(&count); err != nil {
			return alerts.IncidentState{}, fmt.Errorf("count Incident states: %w", err)
		}
		if count >= r.maxEntries {
			return alerts.IncidentState{}, alerts.ErrIncidentCapacity
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO incident_states (device_id, rule_id, rule_revision, last_evaluated_at, state_json) VALUES (?, ?, ?, ?, ?)`, rule.DeviceID, rule.ID, rule.Revision, storageTimestamp(next.LastEvaluatedAt), nextJSON); err != nil {
			return alerts.IncidentState{}, fmt.Errorf("insert Incident state: %w", err)
		}
	} else {
		result, err := transaction.ExecContext(ctx, `UPDATE incident_states SET rule_revision = ?, last_evaluated_at = ?, state_json = ? WHERE device_id = ? AND rule_id = ? AND last_evaluated_at = ?`, rule.Revision, storageTimestamp(next.LastEvaluatedAt), nextJSON, rule.DeviceID, rule.ID, storageTimestamp(expectedLastEvaluatedAt))
		if err != nil {
			return alerts.IncidentState{}, fmt.Errorf("update Incident state: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return alerts.IncidentState{}, alerts.ErrIncidentVersionConflict
		}
	}
	if err := transaction.Commit(); err != nil {
		return alerts.IncidentState{}, fmt.Errorf("commit Incident transition: %w", err)
	}
	return next, nil
}

func decodeIncidentState(encoded []byte) (alerts.IncidentState, error) {
	var state alerts.IncidentState
	if err := json.Unmarshal(encoded, &state); err != nil {
		return alerts.IncidentState{}, fmt.Errorf("decode stored Incident state: %w", err)
	}
	if err := alerts.ValidateIncidentState(state); err != nil {
		return alerts.IncidentState{}, fmt.Errorf("validate stored Incident state: %w", err)
	}
	return state, nil
}

func validateIncidentIdentity(deviceID, ruleID string) error {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || strings.TrimSpace(ruleID) == "" || strings.TrimSpace(ruleID) != ruleID {
		return alerts.ErrIncidentIdentity
	}
	return nil
}
