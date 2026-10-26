/*
Package storage persists explicit per-user notification subscriptions in
core.db. Self-service mutation, active-account checks, revision CAS, capacity,
and audit commit in one transaction.
*/
package storage

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

// NotificationSubscriptionRepository stores current explicit preferences.
type NotificationSubscriptionRepository struct {
	database      *sql.DB
	maxEntries    int
	maxPerUser    int
	maxRecipients int
}

var _ notifications.SubscriptionRepository = (*NotificationSubscriptionRepository)(nil)
var _ notifications.RecipientSource = (*NotificationSubscriptionRepository)(nil)

func NewNotificationSubscriptionRepository(database *sql.DB, maxEntries, maxPerUser, maxRecipients int) (*NotificationSubscriptionRepository, error) {
	if database == nil || maxEntries < 1 || maxPerUser < 1 || maxRecipients < 1 || maxPerUser > maxEntries {
		return nil, errors.New("core database and valid positive notification subscription capacities are required")
	}
	return &NotificationSubscriptionRepository{database: database, maxEntries: maxEntries, maxPerUser: maxPerUser, maxRecipients: maxRecipients}, nil
}

func (r *NotificationSubscriptionRepository) ListForUser(ctx context.Context, userID string, limit int) ([]notifications.Subscription, error) {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(userID) != userID || limit < 1 || limit > r.maxPerUser {
		return nil, notifications.ErrInvalidSubscription
	}
	rows, err := r.database.QueryContext(ctx, `SELECT subscription_json FROM notification_subscriptions WHERE user_id = ? ORDER BY device_id, rule_id, severity LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("list notification subscriptions: %w", err)
	}
	defer rows.Close()
	result := make([]notifications.Subscription, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan notification subscription: %w", err)
		}
		subscription, err := decodeNotificationSubscription(encoded)
		if err != nil || subscription.UserID != userID {
			return nil, errors.New("stored notification subscription is invalid")
		}
		result = append(result, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate notification subscriptions: %w", err)
	}
	return result, nil
}

func (r *NotificationSubscriptionRepository) SaveWithAudit(ctx context.Context, actorID string, subscription notifications.Subscription, expectedRevision uint64, event audit.Event) error {
	if err := notifications.ValidateSubscription(subscription); err != nil || actorID != subscription.UserID || !validNotificationSubscriptionAudit(event, actorID, subscription) {
		return notifications.ErrInvalidSubscription
	}
	encoded, err := json.Marshal(subscription)
	if err != nil {
		return fmt.Errorf("encode notification subscription: %w", err)
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin notification subscription save: %w", err)
	}
	defer transaction.Rollback()
	var active bool
	if err := transaction.QueryRowContext(ctx, `SELECT disabled = 0 FROM users WHERE user_id = ?`, actorID).Scan(&active); errors.Is(err, sql.ErrNoRows) || !active {
		return notifications.ErrInvalidSubscription
	} else if err != nil {
		return fmt.Errorf("check notification subscription user: %w", err)
	}
	var currentRevision uint64
	var currentJSON []byte
	err = transaction.QueryRowContext(ctx, `SELECT revision, subscription_json FROM notification_subscriptions WHERE user_id = ? AND device_id = ? AND rule_id = ? AND severity = ?`, subscription.UserID, subscription.DeviceID, subscription.RuleID, subscription.Severity).Scan(&currentRevision, &currentJSON)
	if errors.Is(err, sql.ErrNoRows) {
		if expectedRevision != 0 || subscription.Revision != 1 {
			return notifications.ErrSubscriptionConflict
		}
		if err := r.checkSubscriptionCapacity(ctx, transaction, actorID); err != nil {
			return err
		}
	} else if err != nil {
		return fmt.Errorf("read notification subscription revision: %w", err)
	} else {
		if currentRevision == subscription.Revision && bytes.Equal(currentJSON, encoded) {
			return transaction.Commit()
		}
		if currentRevision != expectedRevision || subscription.Revision != expectedRevision+1 {
			return notifications.ErrSubscriptionConflict
		}
	}
	result, err := transaction.ExecContext(ctx, `INSERT INTO notification_subscriptions (user_id, device_id, rule_id, severity, revision, enabled, in_app, push_enabled, subscription_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(user_id, device_id, rule_id, severity) DO UPDATE SET revision = excluded.revision, enabled = excluded.enabled, in_app = excluded.in_app, push_enabled = excluded.push_enabled, subscription_json = excluded.subscription_json WHERE notification_subscriptions.revision = ?`, subscription.UserID, subscription.DeviceID, subscription.RuleID, subscription.Severity, subscription.Revision, subscription.Enabled, subscription.Channels.InApp, subscription.Channels.Push, encoded, expectedRevision)
	if err != nil {
		return fmt.Errorf("save notification subscription: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil || changed != 1 {
		return notifications.ErrSubscriptionConflict
	}
	if err := insertAuditEvent(ctx, transaction, event); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("commit notification subscription: %w", err)
	}
	return nil
}

func (r *NotificationSubscriptionRepository) Recipients(ctx context.Context, intent notifications.Intent, limit int) ([]notifications.Recipient, error) {
	if err := notifications.ValidateIntent(intent); err != nil || limit < 1 || limit > r.maxRecipients {
		return nil, notifications.ErrInvalidSubscription
	}
	rows, err := r.database.QueryContext(ctx, `SELECT subscription.subscription_json FROM notification_subscriptions subscription JOIN users user ON user.user_id = subscription.user_id WHERE subscription.device_id = ? AND subscription.rule_id = ? AND subscription.severity = ? AND subscription.enabled = 1 AND user.disabled = 0 ORDER BY subscription.user_id LIMIT ?`, intent.DeviceID, intent.RuleID, intent.Severity, limit+1)
	if err != nil {
		return nil, fmt.Errorf("match notification subscriptions: %w", err)
	}
	defer rows.Close()
	subscriptions := make([]notifications.Subscription, 0)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return nil, fmt.Errorf("scan matching notification subscription: %w", err)
		}
		subscription, err := decodeNotificationSubscription(encoded)
		if err != nil {
			return nil, err
		}
		subscriptions = append(subscriptions, subscription)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate matching notification subscriptions: %w", err)
	}
	return notifications.SelectRecipients(intent, subscriptions, limit)
}

func (r *NotificationSubscriptionRepository) checkSubscriptionCapacity(ctx context.Context, transaction *sql.Tx, userID string) error {
	var total, perUser int
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_subscriptions`).Scan(&total); err != nil {
		return fmt.Errorf("count notification subscriptions: %w", err)
	}
	if err := transaction.QueryRowContext(ctx, `SELECT COUNT(*) FROM notification_subscriptions WHERE user_id = ?`, userID).Scan(&perUser); err != nil {
		return fmt.Errorf("count user notification subscriptions: %w", err)
	}
	if total >= r.maxEntries || perUser >= r.maxPerUser {
		return notifications.ErrSubscriptionCapacity
	}
	return nil
}

func decodeNotificationSubscription(encoded []byte) (notifications.Subscription, error) {
	var subscription notifications.Subscription
	if err := json.Unmarshal(encoded, &subscription); err != nil {
		return notifications.Subscription{}, fmt.Errorf("decode notification subscription: %w", err)
	}
	if err := notifications.ValidateSubscription(subscription); err != nil {
		return notifications.Subscription{}, errors.New("stored notification subscription is invalid")
	}
	return subscription, nil
}

func validNotificationSubscriptionAudit(event audit.Event, actorID string, subscription notifications.Subscription) bool {
	return event.Validate() == nil && event.ActorID == actorID && event.Action == notifications.SubscriptionAuditAction && event.ResourceType == notifications.SubscriptionResourceType && event.ResourceID == notifications.SubscriptionResourceID(subscription)
}
