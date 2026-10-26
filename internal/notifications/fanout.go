/*
Package notifications materializes one immutable per-user notification from an
intent and the subscriptions still active when that intent is processed. The
record is the idempotent downstream effect required before outbox acknowledgement.
*/
package notifications

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
)

var ErrNotificationCapacity = errors.New("recipient notification capacity reached")

type Notification struct {
	ID           string          `json:"id"`
	IntentID     string          `json:"intent_id"`
	UserID       string          `json:"user_id"`
	DeviceID     string          `json:"device_id"`
	RuleID       string          `json:"rule_id"`
	RuleRevision uint64          `json:"rule_revision"`
	Severity     alerts.Severity `json:"severity"`
	Kind         IntentKind      `json:"kind"`
	Channels     Channels        `json:"channels"`
	CreatedAt    time.Time       `json:"created_at"`
}

type NotificationSink interface {
	Materialize(ctx context.Context, intent Intent, recipients []Recipient) error
}

type FanoutHandler struct {
	recipients    RecipientSource
	sink          NotificationSink
	maxRecipients int
}

func NewFanoutHandler(recipients RecipientSource, sink NotificationSink, maxRecipients int) (*FanoutHandler, error) {
	if recipients == nil || sink == nil || maxRecipients < 1 {
		return nil, errors.New("notification recipient source, sink, and positive recipient limit are required")
	}
	return &FanoutHandler{recipients: recipients, sink: sink, maxRecipients: maxRecipients}, nil
}

func (h *FanoutHandler) Handle(ctx context.Context, intent Intent) error {
	recipients, err := h.recipients.Recipients(ctx, intent, h.maxRecipients)
	if err != nil {
		return err
	}
	return h.sink.Materialize(ctx, intent, recipients)
}

func BuildNotification(intent Intent, recipient Recipient) (Notification, error) {
	if err := ValidateIntent(intent); err != nil {
		return Notification{}, err
	}
	if strings.TrimSpace(recipient.UserID) == "" || strings.TrimSpace(recipient.UserID) != recipient.UserID || (!recipient.Channels.InApp && !recipient.Channels.Push) || (intent.DeliveryPolicy == DeliveryRecordOnly && recipient.Channels.Push) {
		return Notification{}, ErrInvalidSubscription
	}
	notification := Notification{
		IntentID: intent.ID, UserID: recipient.UserID, DeviceID: intent.DeviceID,
		RuleID: intent.RuleID, RuleRevision: intent.RuleRevision, Severity: intent.Severity,
		Kind: intent.Kind, Channels: recipient.Channels, CreatedAt: intent.CreatedAt,
	}
	notification.ID = notificationID(intent.ID, recipient.UserID)
	return notification, nil
}

func ValidateNotification(notification Notification) error {
	if err := ValidateIntentID(notification.ID); err != nil || ValidateIntentID(notification.IntentID) != nil || strings.TrimSpace(notification.UserID) == "" || strings.TrimSpace(notification.UserID) != notification.UserID || alerts.ValidateIncidentIdentity(notification.DeviceID, notification.RuleID) != nil || notification.RuleRevision == 0 || notification.CreatedAt.IsZero() || (!notification.Channels.InApp && !notification.Channels.Push) {
		return errors.New("recipient notification is invalid")
	}
	if notification.Severity != alerts.SeverityCritical && notification.Severity != alerts.SeverityWarning && notification.Severity != alerts.SeverityInfo {
		return errors.New("recipient notification severity is invalid")
	}
	if notification.Kind != IntentFiring && notification.Kind != IntentRecovery {
		return errors.New("recipient notification kind is invalid")
	}
	if notification.Severity == alerts.SeverityInfo && notification.Channels.Push {
		return errors.New("Info notification cannot use Push")
	}
	if notification.ID != notificationID(notification.IntentID, notification.UserID) {
		return errors.New("recipient notification ID is invalid")
	}
	return nil
}

func notificationID(intentID, userID string) string {
	sum := sha256.Sum256([]byte(intentID + "\x00" + userID))
	return hex.EncodeToString(sum[:])
}
