/*
Package storage reads, defers, dead-letters, and acknowledges notification
intents stored in core.db. It validates indexed identity columns and the full
JSON fact before a consumer can act on an item.
*/
package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

var ErrInvalidNotificationOutboxRequest = errors.New("invalid notification outbox request")

// NotificationOutboxRepository provides bounded access to pending intents.
type NotificationOutboxRepository struct {
	database       *sql.DB
	maxBatch       int
	maxDeadLetters int
	maxPending     int
}

var _ notifications.Outbox = (*NotificationOutboxRepository)(nil)
var _ notifications.DeadLetterStore = (*NotificationOutboxRepository)(nil)

func NewNotificationOutboxRepository(database *sql.DB, maxBatch, maxDeadLetters, maxPending int) (*NotificationOutboxRepository, error) {
	if database == nil || maxBatch < 1 || maxDeadLetters < 1 || maxPending < 1 {
		return nil, errors.New("core database and positive notification outbox limits are required")
	}
	return &NotificationOutboxRepository{database: database, maxBatch: maxBatch, maxDeadLetters: maxDeadLetters, maxPending: maxPending}, nil
}

// ListDeadLetters returns a validated, newest-first and explicitly bounded view.
func (r *NotificationOutboxRepository) ListDeadLetters(ctx context.Context, limit int) ([]notifications.DeadLetter, error) {
	if limit < 1 || limit > r.maxDeadLetters {
		return nil, ErrInvalidNotificationOutboxRequest
	}
	rows, err := r.database.QueryContext(ctx, `SELECT intent_id, transition_id, device_id, rule_id, rule_revision, kind, delivery_policy, created_at, attempt_count, failed_at, last_error, intent_json FROM notification_intent_dead_letters ORDER BY failed_at DESC, intent_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("list notification dead letters: %w", err)
	}
	defer rows.Close()
	items := make([]notifications.DeadLetter, 0)
	for rows.Next() {
		var intentID, transitionID, deviceID, ruleID, kind, policy, createdAt, failedAt, lastError string
		var revision uint64
		var attempts int
		var encoded []byte
		if err := rows.Scan(&intentID, &transitionID, &deviceID, &ruleID, &revision, &kind, &policy, &createdAt, &attempts, &failedAt, &lastError, &encoded); err != nil {
			return nil, fmt.Errorf("scan notification dead letter: %w", err)
		}
		var intent notifications.Intent
		failed, parseErr := time.Parse(storageTimeLayout, failedAt)
		if err := json.Unmarshal(encoded, &intent); err != nil || parseErr != nil {
			return nil, errors.New("stored notification dead letter is invalid")
		}
		item := notifications.DeadLetter{Intent: intent, AttemptCount: attempts, FailedAt: failed, LastError: lastError}
		if item.Validate() != nil || intent.ID != intentID || intent.TransitionID != transitionID || intent.DeviceID != deviceID || intent.RuleID != ruleID || intent.RuleRevision != revision || string(intent.Kind) != kind || string(intent.DeliveryPolicy) != policy || storageTimestamp(intent.CreatedAt) != createdAt {
			return nil, errors.New("stored notification dead letter is invalid")
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notification dead letters: %w", err)
	}
	return items, nil
}

// RequeueDeadLetter atomically resets one terminal failure if pending capacity permits.
func (r *NotificationOutboxRepository) RequeueDeadLetter(ctx context.Context, intentID string) error {
	if notifications.ValidateIntentID(intentID) != nil {
		return ErrInvalidNotificationOutboxRequest
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin notification dead-letter requeue: %w", err)
	}
	defer transaction.Rollback()
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_intent_outbox`).Scan(&count); err != nil {
		return fmt.Errorf("count notification outbox for requeue: %w", err)
	}
	if count >= r.maxPending {
		return notifications.ErrIntentCapacity
	}
	result, err := transaction.ExecContext(ctx, `INSERT INTO notification_intent_outbox (intent_id, transition_id, device_id, rule_id, rule_revision, kind, delivery_policy, created_at, intent_json, attempt_count, next_attempt_at, last_error) SELECT intent_id, transition_id, device_id, rule_id, rule_revision, kind, delivery_policy, created_at, intent_json, 0, NULL, '' FROM notification_intent_dead_letters WHERE intent_id = ?`, intentID)
	if err != nil {
		return fmt.Errorf("requeue notification dead letter: %w", err)
	}
	if err := requireOneChanged(result); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM notification_intent_dead_letters WHERE intent_id = ?`, intentID); err != nil {
		return fmt.Errorf("remove requeued notification dead letter: %w", err)
	}
	return transaction.Commit()
}

// DeleteDeadLettersBefore removes at most limit oldest terminal records.
func (r *NotificationOutboxRepository) DeleteDeadLettersBefore(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if cutoff.IsZero() || limit < 1 || limit > r.maxDeadLetters {
		return 0, ErrInvalidNotificationOutboxRequest
	}
	result, err := r.database.ExecContext(ctx, `DELETE FROM notification_intent_dead_letters WHERE intent_id IN (SELECT intent_id FROM notification_intent_dead_letters WHERE failed_at < ? ORDER BY failed_at, intent_id LIMIT ?)`, storageTimestamp(cutoff.UTC()), limit)
	if err != nil {
		return 0, fmt.Errorf("delete notification dead letters: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("inspect deleted notification dead letters: %w", err)
	}
	return int(changed), nil
}

// Pending returns the oldest eligible intents without letting deferred work block ready work.
func (r *NotificationOutboxRepository) Pending(ctx context.Context, readyAt time.Time, limit int) ([]notifications.PendingIntent, error) {
	if readyAt.IsZero() || limit < 1 || limit > r.maxBatch {
		return nil, ErrInvalidNotificationOutboxRequest
	}
	rows, err := r.database.QueryContext(ctx, `SELECT intent_id, transition_id, device_id, rule_id, rule_revision, kind, delivery_policy, created_at, intent_json, attempt_count FROM notification_intent_outbox WHERE next_attempt_at IS NULL OR next_attempt_at <= ? ORDER BY created_at, intent_id LIMIT ?`, storageTimestamp(readyAt.UTC()), limit)
	if err != nil {
		return nil, fmt.Errorf("list notification intents: %w", err)
	}
	defer rows.Close()
	intents := make([]notifications.PendingIntent, 0)
	for rows.Next() {
		var intentID, transitionID, deviceID, ruleID, kind, policy, createdAt string
		var revision uint64
		var attemptCount int
		var encoded []byte
		if err := rows.Scan(&intentID, &transitionID, &deviceID, &ruleID, &revision, &kind, &policy, &createdAt, &encoded, &attemptCount); err != nil {
			return nil, fmt.Errorf("scan notification intent: %w", err)
		}
		var intent notifications.Intent
		if err := json.Unmarshal(encoded, &intent); err != nil {
			return nil, fmt.Errorf("decode notification intent: %w", err)
		}
		if err := notifications.ValidateIntent(intent); err != nil || attemptCount < 0 || intent.ID != intentID || intent.TransitionID != transitionID || intent.DeviceID != deviceID || intent.RuleID != ruleID || intent.RuleRevision != revision || string(intent.Kind) != kind || string(intent.DeliveryPolicy) != policy || storageTimestamp(intent.CreatedAt) != createdAt {
			return nil, errors.New("stored notification intent is invalid")
		}
		intents = append(intents, notifications.PendingIntent{Intent: intent, AttemptCount: attemptCount})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notification intents: %w", err)
	}
	return intents, nil
}

// RecordFailure atomically defers an item or moves it into the bounded dead-letter table.
func (r *NotificationOutboxRepository) RecordFailure(ctx context.Context, failure notifications.Failure) error {
	if failure.Validate() != nil {
		return ErrInvalidNotificationOutboxRequest
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin notification failure: %w", err)
	}
	defer transaction.Rollback()
	if !failure.DeadLetter {
		result, err := transaction.ExecContext(ctx, `UPDATE notification_intent_outbox SET attempt_count = ?, next_attempt_at = ?, last_error = ? WHERE intent_id = ? AND attempt_count = ?`, failure.AttemptCount, storageTimestamp(failure.NextAttempt), failure.LastError, failure.IntentID, failure.AttemptCount-1)
		if err != nil {
			return fmt.Errorf("defer notification intent: %w", err)
		}
		if err := requireOneChanged(result); err != nil {
			return err
		}
		return transaction.Commit()
	}
	var count int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_intent_dead_letters`).Scan(&count); err != nil {
		return fmt.Errorf("count notification dead letters: %w", err)
	}
	if count >= r.maxDeadLetters {
		return notifications.ErrDeadLetterCapacity
	}
	result, err := transaction.ExecContext(ctx, `INSERT INTO notification_intent_dead_letters (intent_id, transition_id, device_id, rule_id, rule_revision, kind, delivery_policy, created_at, attempt_count, failed_at, last_error, intent_json) SELECT intent_id, transition_id, device_id, rule_id, rule_revision, kind, delivery_policy, created_at, ?, ?, ?, intent_json FROM notification_intent_outbox WHERE intent_id = ? AND attempt_count = ?`, failure.AttemptCount, storageTimestamp(failure.FailedAt), failure.LastError, failure.IntentID, failure.AttemptCount-1)
	if err != nil {
		return fmt.Errorf("dead-letter notification intent: %w", err)
	}
	if err := requireOneChanged(result); err != nil {
		return err
	}
	result, err = transaction.ExecContext(ctx, `DELETE FROM notification_intent_outbox WHERE intent_id = ? AND attempt_count = ?`, failure.IntentID, failure.AttemptCount-1)
	if err != nil {
		return fmt.Errorf("remove dead-lettered notification intent: %w", err)
	}
	if err := requireOneChanged(result); err != nil {
		return err
	}
	return transaction.Commit()
}

func requireOneChanged(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect notification outbox change: %w", err)
	}
	if changed != 1 {
		return errors.New("notification outbox state changed concurrently")
	}
	return nil
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
