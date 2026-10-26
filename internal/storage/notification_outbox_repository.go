/*
Package storage reads and acknowledges notification intents stored in core.db.
It validates both indexed identity columns and the complete JSON fact before a
consumer can act on an item.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

var ErrInvalidNotificationOutboxRequest = errors.New("invalid notification outbox request")

// NotificationOutboxRepository provides bounded access to pending intents.
type NotificationOutboxRepository struct {
	database *sql.DB
	maxBatch int
}

var _ notifications.Outbox = (*NotificationOutboxRepository)(nil)

func NewNotificationOutboxRepository(database *sql.DB, maxBatch int) (*NotificationOutboxRepository, error) {
	if database == nil || maxBatch < 1 {
		return nil, errors.New("core database and positive notification outbox batch limit are required")
	}
	return &NotificationOutboxRepository{database: database, maxBatch: maxBatch}, nil
}

// Pending returns the oldest intents so a single consumer cannot starve them.
func (r *NotificationOutboxRepository) Pending(ctx context.Context, limit int) ([]notifications.Intent, error) {
	if limit < 1 || limit > r.maxBatch {
		return nil, ErrInvalidNotificationOutboxRequest
	}
	rows, err := r.database.QueryContext(ctx, `SELECT intent_id, transition_id, device_id, rule_id, rule_revision, kind, delivery_policy, created_at, intent_json FROM notification_intent_outbox ORDER BY created_at, intent_id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list notification intents: %w", err)
	}
	defer rows.Close()
	intents := make([]notifications.Intent, 0)
	for rows.Next() {
		var intentID, transitionID, deviceID, ruleID, kind, policy, createdAt string
		var revision uint64
		var encoded []byte
		if err := rows.Scan(&intentID, &transitionID, &deviceID, &ruleID, &revision, &kind, &policy, &createdAt, &encoded); err != nil {
			return nil, fmt.Errorf("scan notification intent: %w", err)
		}
		var intent notifications.Intent
		if err := json.Unmarshal(encoded, &intent); err != nil {
			return nil, fmt.Errorf("decode notification intent: %w", err)
		}
		if err := notifications.ValidateIntent(intent); err != nil || intent.ID != intentID || intent.TransitionID != transitionID || intent.DeviceID != deviceID || intent.RuleID != ruleID || intent.RuleRevision != revision || string(intent.Kind) != kind || string(intent.DeliveryPolicy) != policy || storageTimestamp(intent.CreatedAt) != createdAt {
			return nil, errors.New("stored notification intent is invalid")
		}
		intents = append(intents, intent)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notification intents: %w", err)
	}
	return intents, nil
}

// Acknowledge idempotently removes an intent after downstream commit.
func (r *NotificationOutboxRepository) Acknowledge(ctx context.Context, intentID string) error {
	if err := notifications.ValidateIntentID(intentID); err != nil {
		return ErrInvalidNotificationOutboxRequest
	}
	if _, err := r.database.ExecContext(ctx, `DELETE FROM notification_intent_outbox WHERE intent_id = ?`, intentID); err != nil {
		return fmt.Errorf("acknowledge notification intent: %w", err)
	}
	return nil
}
