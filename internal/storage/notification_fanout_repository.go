/*
Package storage atomically materializes bounded per-user notifications from one
outbox intent. Existing deterministic records are revalidated so replay is
idempotent and conflicting data cannot be silently accepted.
*/
package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

type NotificationFanoutRepository struct {
	database   *sql.DB
	maxRecords int
}

var _ notifications.NotificationSink = (*NotificationFanoutRepository)(nil)

func NewNotificationFanoutRepository(database *sql.DB, maxRecords int) (*NotificationFanoutRepository, error) {
	if database == nil || maxRecords < 1 {
		return nil, errors.New("core database and positive recipient notification capacity are required")
	}
	return &NotificationFanoutRepository{database: database, maxRecords: maxRecords}, nil
}

func (r *NotificationFanoutRepository) Materialize(ctx context.Context, intent notifications.Intent, recipients []notifications.Recipient) error {
	if err := notifications.ValidateIntent(intent); err != nil {
		return err
	}
	encoded := make([][]byte, len(recipients))
	items := make([]notifications.Notification, len(recipients))
	existingRecords := make([]bool, len(recipients))
	seen := make(map[string]struct{}, len(recipients))
	for index, recipient := range recipients {
		item, err := notifications.BuildNotification(intent, recipient)
		if err != nil {
			return err
		}
		if _, exists := seen[item.ID]; exists {
			return notifications.ErrInvalidSubscription
		}
		seen[item.ID] = struct{}{}
		items[index] = item
		encoded[index], err = json.Marshal(item)
		if err != nil {
			return fmt.Errorf("encode recipient notification: %w", err)
		}
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin notification fanout: %w", err)
	}
	defer transaction.Rollback()
	newRecords := 0
	for index, item := range items {
		var existing []byte
		err := transaction.QueryRowContext(ctx, `SELECT notification_json FROM recipient_notifications WHERE notification_id = ?`, item.ID).Scan(&existing)
		if errors.Is(err, sql.ErrNoRows) {
			newRecords++
			continue
		}
		if err != nil {
			return fmt.Errorf("read recipient notification: %w", err)
		}
		if !bytes.Equal(existing, encoded[index]) {
			return errors.New("recipient notification replay conflicts with stored record")
		}
		existingRecords[index] = true
	}
	if newRecords > 0 {
		var count int
		if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM recipient_notifications`).Scan(&count); err != nil {
			return fmt.Errorf("count recipient notifications: %w", err)
		}
		if count > r.maxRecords-newRecords {
			return notifications.ErrNotificationCapacity
		}
	}
	for index, item := range items {
		if existingRecords[index] {
			continue
		}
		result, err := transaction.ExecContext(ctx, `INSERT INTO recipient_notifications (notification_id, intent_id, user_id, device_id, rule_id, rule_revision, severity, kind, in_app, push_enabled, created_at, notification_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.IntentID, item.UserID, item.DeviceID, item.RuleID, item.RuleRevision, item.Severity, item.Kind, item.Channels.InApp, item.Channels.Push, storageTimestamp(item.CreatedAt), encoded[index])
		if err != nil {
			return fmt.Errorf("insert recipient notification: %w", err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return errors.New("recipient notification insert result is invalid")
		}
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit notification fanout: %w", err)
	}
	return nil
}
