/*
Package storage persists current Incident transition state and superseded rule
revision archives in core.db. Evaluation uses timestamp CAS; explicit Owner
activation atomically archives and clears old current state with an audit event.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

// IncidentRepository stores bounded current Incident state in core.db.
type IncidentRepository struct {
	database             *sql.DB
	maxEntries           int
	maxArchiveEntries    int
	maxTransitionEntries int
	maxIntentEntries     int
}

var _ alerts.IncidentRepository = (*IncidentRepository)(nil)
var _ alerts.IncidentActivationRepository = (*IncidentRepository)(nil)
var _ alerts.IncidentArchiveSource = (*IncidentRepository)(nil)
var _ alerts.IncidentTransitionSource = (*IncidentRepository)(nil)

// NewIncidentRepository requires explicit current-state and archive capacities.
func NewIncidentRepository(database *sql.DB, maxEntries, maxArchiveEntries int) (*IncidentRepository, error) {
	return newIncidentRepository(database, maxEntries, maxArchiveEntries, 0, 0)
}

// NewIncidentRepositoryWithTransitions also persists bounded status changes.
func NewIncidentRepositoryWithTransitions(database *sql.DB, maxEntries, maxArchiveEntries, maxTransitionEntries int) (*IncidentRepository, error) {
	if maxTransitionEntries < 1 {
		return nil, errors.New("positive Incident transition capacity is required")
	}
	return newIncidentRepository(database, maxEntries, maxArchiveEntries, maxTransitionEntries, 0)
}

// NewIncidentRepositoryWithNotificationOutbox atomically persists transitions
// and notification intents within independent explicit capacities.
func NewIncidentRepositoryWithNotificationOutbox(database *sql.DB, maxEntries, maxArchiveEntries, maxTransitionEntries, maxIntentEntries int) (*IncidentRepository, error) {
	if maxTransitionEntries < 1 || maxIntentEntries < 1 {
		return nil, errors.New("positive Incident transition and notification intent capacities are required")
	}
	return newIncidentRepository(database, maxEntries, maxArchiveEntries, maxTransitionEntries, maxIntentEntries)
}

func newIncidentRepository(database *sql.DB, maxEntries, maxArchiveEntries, maxTransitionEntries, maxIntentEntries int) (*IncidentRepository, error) {
	if database == nil || maxEntries < 1 || maxArchiveEntries < 1 {
		return nil, errors.New("core database and positive Incident current and archive capacities are required")
	}
	return &IncidentRepository{database: database, maxEntries: maxEntries, maxArchiveEntries: maxArchiveEntries, maxTransitionEntries: maxTransitionEntries, maxIntentEntries: maxIntentEntries}, nil
}

// ListArchives returns newest superseded revisions first with an explicit limit.
func (r *IncidentRepository) ListArchives(ctx context.Context, deviceID, ruleID string, limit int) ([]alerts.IncidentArchive, error) {
	if err := validateIncidentIdentity(deviceID, ruleID); err != nil || limit < 1 || limit > r.maxArchiveEntries {
		return nil, alerts.ErrInvalidIncidentRequest
	}
	rows, err := r.database.QueryContext(ctx, `SELECT archive_json FROM incident_revision_archives WHERE device_id = ? AND rule_id = ? ORDER BY rule_revision DESC LIMIT ?`, deviceID, ruleID, limit)
	if err != nil {
		return nil, fmt.Errorf("list Incident archives: %w", err)
	}
	defer rows.Close()
	archives := make([]alerts.IncidentArchive, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan Incident archive: %w", err)
		}
		archive, err := decodeIncidentArchive(encoded)
		if err != nil {
			return nil, err
		}
		archives = append(archives, archive)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Incident archives: %w", err)
	}
	return archives, nil
}

// ListTransitions returns newest status changes first with an explicit limit.
func (r *IncidentRepository) ListTransitions(ctx context.Context, deviceID, ruleID string, limit int) ([]alerts.IncidentTransition, error) {
	if err := validateIncidentIdentity(deviceID, ruleID); err != nil || r.maxTransitionEntries < 1 || limit < 1 || limit > r.maxTransitionEntries {
		return nil, alerts.ErrInvalidIncidentRequest
	}
	rows, err := r.database.QueryContext(ctx, `SELECT transition_json FROM incident_transitions WHERE device_id = ? AND rule_id = ? ORDER BY occurred_at DESC, event_id DESC LIMIT ?`, deviceID, ruleID, limit)
	if err != nil {
		return nil, fmt.Errorf("list Incident transitions: %w", err)
	}
	defer rows.Close()
	transitions := make([]alerts.IncidentTransition, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan Incident transition: %w", err)
		}
		var transition alerts.IncidentTransition
		if err := json.Unmarshal(encoded, &transition); err != nil {
			return nil, fmt.Errorf("decode stored Incident transition: %w", err)
		}
		if err := alerts.ValidateIncidentTransition(transition); err != nil || transition.DeviceID != deviceID || transition.RuleID != ruleID {
			return nil, errors.New("stored Incident transition is invalid")
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Incident transitions: %w", err)
	}
	return transitions, nil
}

// ActivateRuleRevisionWithAudit archives an older current state and clears it atomically.
func (r *IncidentRepository) ActivateRuleRevisionWithAudit(ctx context.Context, actorID, deviceID, ruleID string, targetRevision uint64, event audit.Event) (alerts.IncidentActivationResult, error) {
	if err := validateIncidentIdentity(deviceID, ruleID); err != nil || targetRevision == 0 {
		return alerts.IncidentActivationResult{}, alerts.ErrInvalidIncidentRequest
	}
	if err := validateIncidentActivationAudit(event, actorID, deviceID, ruleID); err != nil {
		return alerts.IncidentActivationResult{}, err
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return alerts.IncidentActivationResult{}, fmt.Errorf("begin Incident activation: %w", err)
	}
	defer transaction.Rollback()
	if err := requireActiveRuleOwner(ctx, transaction, actorID); err != nil {
		return alerts.IncidentActivationResult{}, err
	}
	var currentRuleRevision uint64
	err = transaction.QueryRowContext(ctx, `SELECT current_revision FROM alert_rules WHERE device_id = ? AND rule_id = ?`, deviceID, ruleID).Scan(&currentRuleRevision)
	if errors.Is(err, sql.ErrNoRows) {
		return alerts.IncidentActivationResult{}, alerts.ErrIncidentRuleNotFound
	}
	if err != nil {
		return alerts.IncidentActivationResult{}, fmt.Errorf("read current rule for Incident activation: %w", err)
	}
	if currentRuleRevision != targetRevision {
		return alerts.IncidentActivationResult{}, alerts.ErrIncidentActivationConflict
	}
	var encoded []byte
	err = transaction.QueryRowContext(ctx, `SELECT state_json FROM incident_states WHERE device_id = ? AND rule_id = ?`, deviceID, ruleID).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return alerts.IncidentActivationResult{Ready: true}, transaction.Commit()
	}
	if err != nil {
		return alerts.IncidentActivationResult{}, fmt.Errorf("read current Incident for activation: %w", err)
	}
	state, err := decodeIncidentState(encoded)
	if err != nil {
		return alerts.IncidentActivationResult{}, err
	}
	if state.RuleRevision == targetRevision {
		return alerts.IncidentActivationResult{Ready: true}, transaction.Commit()
	}
	if state.RuleRevision > targetRevision {
		return alerts.IncidentActivationResult{}, alerts.ErrIncidentActivationConflict
	}
	var archiveCount int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM incident_revision_archives`).Scan(&archiveCount); err != nil {
		return alerts.IncidentActivationResult{}, fmt.Errorf("count Incident archives: %w", err)
	}
	if archiveCount >= r.maxArchiveEntries {
		return alerts.IncidentActivationResult{}, alerts.ErrIncidentArchiveCapacity
	}
	archive := alerts.IncidentArchive{State: state, Disposition: alerts.IncidentArchiveSuperseded, SupersededByRevision: targetRevision, SupersededAt: event.OccurredAt.UTC()}
	archiveJSON, err := json.Marshal(archive)
	if err != nil {
		return alerts.IncidentActivationResult{}, fmt.Errorf("encode Incident archive: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO incident_revision_archives (device_id, rule_id, rule_revision, superseded_by_revision, superseded_at, archive_json) VALUES (?, ?, ?, ?, ?, ?)`, deviceID, ruleID, state.RuleRevision, targetRevision, storageTimestamp(archive.SupersededAt), archiveJSON); err != nil {
		return alerts.IncidentActivationResult{}, fmt.Errorf("insert Incident archive: %w", err)
	}
	result, err := transaction.ExecContext(ctx, `DELETE FROM incident_states WHERE device_id = ? AND rule_id = ? AND rule_revision = ? AND last_evaluated_at = ?`, deviceID, ruleID, state.RuleRevision, storageTimestamp(state.LastEvaluatedAt))
	if err != nil {
		return alerts.IncidentActivationResult{}, fmt.Errorf("clear superseded Incident state: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return alerts.IncidentActivationResult{}, alerts.ErrIncidentActivationConflict
	}
	if err := insertAuditEvent(ctx, transaction, event); err != nil {
		return alerts.IncidentActivationResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		return alerts.IncidentActivationResult{}, fmt.Errorf("commit Incident activation: %w", err)
	}
	return alerts.IncidentActivationResult{Ready: true, Archived: &archive}, nil
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
	if err := alerts.ValidateIncidentDeviceID(deviceID); err != nil {
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
	transition, changed, err := alerts.BuildIncidentTransition(current, exists, next)
	if err != nil {
		return alerts.IncidentState{}, err
	}
	if changed && r.maxTransitionEntries > 0 {
		var count int
		if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM incident_transitions`).Scan(&count); err != nil {
			return alerts.IncidentState{}, fmt.Errorf("count Incident transitions: %w", err)
		}
		if count >= r.maxTransitionEntries {
			return alerts.IncidentState{}, alerts.ErrIncidentTransitionCapacity
		}
		encoded, err := json.Marshal(transition)
		if err != nil {
			return alerts.IncidentState{}, fmt.Errorf("encode Incident transition: %w", err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO incident_transitions (event_id, device_id, rule_id, rule_revision, occurred_at, from_status, to_status, transition_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, transition.ID, transition.DeviceID, transition.RuleID, transition.RuleRevision, storageTimestamp(transition.OccurredAt), transition.FromStatus, transition.ToStatus, encoded); err != nil {
			return alerts.IncidentState{}, fmt.Errorf("insert Incident transition: %w", err)
		}
		if r.maxIntentEntries > 0 {
			intent, planned, err := notifications.Plan(transition, rule.Severity)
			if err != nil {
				return alerts.IncidentState{}, err
			}
			if planned {
				if err := insertNotificationIntent(ctx, transaction, intent, r.maxIntentEntries); err != nil {
					return alerts.IncidentState{}, err
				}
			}
		}
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

func insertNotificationIntent(ctx context.Context, transaction *sql.Tx, intent notifications.Intent, maxEntries int) error {
	if err := notifications.ValidateIntent(intent); err != nil {
		return err
	}
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_intent_outbox`).Scan(&count); err != nil {
		return fmt.Errorf("count notification intents: %w", err)
	}
	if count >= maxEntries {
		return notifications.ErrIntentCapacity
	}
	encoded, err := json.Marshal(intent)
	if err != nil {
		return fmt.Errorf("encode notification intent: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO notification_intent_outbox (intent_id, transition_id, device_id, rule_id, rule_revision, kind, delivery_policy, created_at, intent_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, intent.ID, intent.TransitionID, intent.DeviceID, intent.RuleID, intent.RuleRevision, intent.Kind, intent.DeliveryPolicy, storageTimestamp(intent.CreatedAt), encoded); err != nil {
		return fmt.Errorf("insert notification intent: %w", err)
	}
	return nil
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

func decodeIncidentArchive(encoded []byte) (alerts.IncidentArchive, error) {
	var archive alerts.IncidentArchive
	if err := json.Unmarshal(encoded, &archive); err != nil {
		return alerts.IncidentArchive{}, fmt.Errorf("decode stored Incident archive: %w", err)
	}
	if err := alerts.ValidateIncidentState(archive.State); err != nil || archive.Disposition != alerts.IncidentArchiveSuperseded || archive.SupersededByRevision <= archive.State.RuleRevision || archive.SupersededAt.IsZero() {
		return alerts.IncidentArchive{}, errors.New("stored Incident archive is invalid")
	}
	return archive, nil
}

func validateIncidentIdentity(deviceID, ruleID string) error {
	return alerts.ValidateIncidentIdentity(deviceID, ruleID)
}

func validateIncidentActivationAudit(event audit.Event, actorID, deviceID, ruleID string) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate Incident activation audit event: %w", err)
	}
	if event.ActorID != actorID || event.Action != alerts.IncidentSupersedeAuditAction || event.ResourceType != alerts.IncidentAuditResourceType || event.ResourceID != alerts.IncidentAuditResourceID(deviceID, ruleID) {
		return errors.New("Incident activation audit event does not match mutation")
	}
	return nil
}
