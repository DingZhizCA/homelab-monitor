/*
Package storage persists immutable alert-rule revisions and their current
pointers in core.db. Owner state, compare-and-swap updates, and management
audit events are checked and committed in one transaction.
*/
package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

// AlertRuleRepository implements the rule repository against core.db.
type AlertRuleRepository struct {
	database *sql.DB
	registry alerts.Registry
	limits   alerts.Limits
}

var _ alerts.RuleRepository = (*AlertRuleRepository)(nil)

// NewAlertRuleRepository binds validated alert rules to core.db.
func NewAlertRuleRepository(database *sql.DB, registry alerts.Registry, limits alerts.Limits) (*AlertRuleRepository, error) {
	if database == nil || registry == nil || limits.MaxDepth < 1 || limits.MaxConditions < 1 {
		return nil, errors.New("core database, metric registry, and positive alert-rule limits are required")
	}
	return &AlertRuleRepository{database: database, registry: registry, limits: limits}, nil
}

func (r *AlertRuleRepository) Current(ctx context.Context, deviceID, ruleID string) (alerts.Rule, bool, error) {
	return r.read(ctx, `SELECT revision.rule_json FROM alert_rules current
		JOIN alert_rule_revisions revision ON revision.device_id = current.device_id AND revision.rule_id = current.rule_id AND revision.revision = current.current_revision
		WHERE current.device_id = ? AND current.rule_id = ?`, deviceID, ruleID)
}

func (r *AlertRuleRepository) Revision(ctx context.Context, deviceID, ruleID string, revision uint64) (alerts.Rule, bool, error) {
	return r.read(ctx, `SELECT rule_json FROM alert_rule_revisions WHERE device_id = ? AND rule_id = ? AND revision = ?`, deviceID, ruleID, revision)
}

func (r *AlertRuleRepository) read(ctx context.Context, query string, args ...any) (alerts.Rule, bool, error) {
	var encoded []byte
	err := r.database.QueryRowContext(ctx, query, args...).Scan(&encoded)
	if errors.Is(err, sql.ErrNoRows) {
		return alerts.Rule{}, false, nil
	}
	if err != nil {
		return alerts.Rule{}, false, fmt.Errorf("read alert rule: %w", err)
	}
	rule, err := r.decode(encoded)
	return rule, err == nil, err
}

func (r *AlertRuleRepository) ListRevisions(ctx context.Context, deviceID, ruleID string) ([]alerts.Rule, error) {
	return r.list(ctx, `SELECT rule_json FROM alert_rule_revisions WHERE device_id = ? AND rule_id = ? ORDER BY revision DESC`, deviceID, ruleID)
}

func (r *AlertRuleRepository) ListCurrent(ctx context.Context, deviceID string) ([]alerts.Rule, error) {
	return r.list(ctx, `SELECT revision.rule_json FROM alert_rules current
		JOIN alert_rule_revisions revision ON revision.device_id = current.device_id AND revision.rule_id = current.rule_id AND revision.revision = current.current_revision
		WHERE current.device_id = ? ORDER BY current.rule_id`, deviceID)
}

// ListEvaluationPage returns a stable global page after a device/rule cursor.
func (r *AlertRuleRepository) ListEvaluationPage(ctx context.Context, afterDeviceID, afterRuleID string, limit int) ([]alerts.Rule, error) {
	if limit < 1 || (afterDeviceID == "" && afterRuleID != "") {
		return nil, errors.New("valid alert evaluation cursor and positive limit are required")
	}
	return r.list(ctx, `SELECT revision.rule_json FROM alert_rules current
		JOIN alert_rule_revisions revision ON revision.device_id = current.device_id AND revision.rule_id = current.rule_id AND revision.revision = current.current_revision
		WHERE current.device_id > ? OR (current.device_id = ? AND current.rule_id > ?)
		ORDER BY current.device_id, current.rule_id LIMIT ?`, afterDeviceID, afterDeviceID, afterRuleID, limit)
}

func (r *AlertRuleRepository) list(ctx context.Context, query string, args ...any) ([]alerts.Rule, error) {
	rows, err := r.database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list alert rules: %w", err)
	}
	defer rows.Close()
	result := make([]alerts.Rule, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan alert rule: %w", err)
		}
		rule, err := r.decode(encoded)
		if err != nil {
			return nil, err
		}
		result = append(result, rule)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate alert rules: %w", err)
	}
	return result, nil
}

func (r *AlertRuleRepository) SaveRevisionWithAudit(ctx context.Context, actorID string, rule alerts.Rule, expectedCurrent uint64, event audit.Event) error {
	if err := rule.Validate(r.registry, r.limits); err != nil {
		return fmt.Errorf("invalid alert rule: %w", err)
	}
	if err := validateStoredRuleAudit(event, actorID, rule); err != nil {
		return err
	}
	encoded, err := json.Marshal(rule)
	if err != nil {
		return fmt.Errorf("encode alert rule: %w", err)
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin alert-rule save: %w", err)
	}
	defer transaction.Rollback()
	if err := requireActiveRuleOwner(ctx, transaction, actorID); err != nil {
		return err
	}
	var current uint64
	err = transaction.QueryRowContext(ctx, `SELECT current_revision FROM alert_rules WHERE device_id = ? AND rule_id = ?`, rule.DeviceID, rule.ID).Scan(&current)
	if errors.Is(err, sql.ErrNoRows) {
		if expectedCurrent != 0 {
			return alerts.ErrRuleVersionConflict
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO alert_rules (device_id, rule_id, current_revision) VALUES (?, ?, ?)`, rule.DeviceID, rule.ID, rule.Revision); err != nil {
			return fmt.Errorf("insert alert rule: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("read current alert-rule revision: %w", err)
	} else {
		var previous []byte
		err := transaction.QueryRowContext(ctx, `SELECT rule_json FROM alert_rule_revisions WHERE device_id = ? AND rule_id = ? AND revision = ?`, rule.DeviceID, rule.ID, rule.Revision).Scan(&previous)
		if err == nil {
			if bytes.Equal(previous, encoded) {
				return transaction.Commit()
			}
			return alerts.ErrRuleVersionConflict
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check alert-rule revision: %w", err)
		}
		if current != expectedCurrent || rule.Revision <= expectedCurrent {
			return alerts.ErrRuleVersionConflict
		}
		result, err := transaction.ExecContext(ctx, `UPDATE alert_rules SET current_revision = ? WHERE device_id = ? AND rule_id = ? AND current_revision = ?`, rule.Revision, rule.DeviceID, rule.ID, expectedCurrent)
		if err != nil {
			return fmt.Errorf("advance alert-rule revision: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return alerts.ErrRuleVersionConflict
		}
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO alert_rule_revisions (device_id, rule_id, revision, rule_json) VALUES (?, ?, ?, ?)`, rule.DeviceID, rule.ID, rule.Revision, encoded); err != nil {
		return fmt.Errorf("insert alert-rule revision: %w", err)
	}
	if err := insertAuditEvent(ctx, transaction, event); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit alert-rule save: %w", err)
	}
	return nil
}

func (r *AlertRuleRepository) decode(encoded []byte) (alerts.Rule, error) {
	var rule alerts.Rule
	if err := json.Unmarshal(encoded, &rule); err != nil {
		return alerts.Rule{}, fmt.Errorf("decode stored alert rule: %w", err)
	}
	if err := rule.Validate(r.registry, r.limits); err != nil {
		return alerts.Rule{}, fmt.Errorf("validate stored alert rule: %w", err)
	}
	return rule, nil
}

func requireActiveRuleOwner(ctx context.Context, transaction *sql.Tx, actorID string) error {
	var role auth.Role
	var disabled int
	err := transaction.QueryRowContext(ctx, `SELECT role, disabled FROM users WHERE user_id = ?`, actorID).Scan(&role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("verify alert-rule Owner: %w", err)
	}
	if role != auth.RoleOwner || disabled != 0 {
		return auth.ErrForbidden
	}
	return nil
}

func validateStoredRuleAudit(event audit.Event, actorID string, rule alerts.Rule) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate alert-rule audit event: %w", err)
	}
	if event.ActorID != actorID || (event.Action != alerts.RuleAuditAction && event.Action != alerts.RuleRollbackAuditAction) || event.ResourceType != alerts.RuleAuditResourceType || event.ResourceID != alerts.RuleAuditResourceID(rule) {
		return errors.New("alert-rule audit event does not match mutation")
	}
	return nil
}
