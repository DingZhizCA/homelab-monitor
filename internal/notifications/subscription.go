/*
Package notifications defines explicit per-user notification subscriptions.
Each subscription has an exact device, rule and severity scope; it introduces
no wildcard matching, implicit role recipient, or sampling behavior.
*/
package notifications

import (
	"errors"
	"fmt"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
)

var (
	ErrInvalidSubscription = errors.New("invalid notification subscription")
	ErrRecipientCapacity   = errors.New("notification recipient capacity reached")
)

type Channels struct {
	InApp bool `json:"in_app"`
	Push  bool `json:"push"`
}

// Subscription is one versioned, exact recipient preference.
type Subscription struct {
	UserID   string          `json:"user_id"`
	DeviceID string          `json:"device_id"`
	RuleID   string          `json:"rule_id"`
	Severity alerts.Severity `json:"severity"`
	Revision uint64          `json:"revision"`
	Enabled  bool            `json:"enabled"`
	Channels Channels        `json:"channels"`
}

type Recipient struct {
	UserID   string   `json:"user_id"`
	Channels Channels `json:"channels"`
}

func ValidateSubscription(subscription Subscription) error {
	if strings.TrimSpace(subscription.UserID) == "" || strings.TrimSpace(subscription.UserID) != subscription.UserID || alerts.ValidateIncidentIdentity(subscription.DeviceID, subscription.RuleID) != nil || subscription.Revision == 0 {
		return ErrInvalidSubscription
	}
	if subscription.Severity != alerts.SeverityCritical && subscription.Severity != alerts.SeverityWarning && subscription.Severity != alerts.SeverityInfo {
		return ErrInvalidSubscription
	}
	if subscription.Enabled && !subscription.Channels.InApp && !subscription.Channels.Push {
		return ErrInvalidSubscription
	}
	if subscription.Severity == alerts.SeverityInfo && subscription.Channels.Push {
		return ErrInvalidSubscription
	}
	return nil
}

// SelectRecipients applies exact subscriptions with a caller-provided bound.
func SelectRecipients(intent Intent, subscriptions []Subscription, maxRecipients int) ([]Recipient, error) {
	if err := ValidateIntent(intent); err != nil {
		return nil, err
	}
	if maxRecipients < 1 {
		return nil, errors.New("positive notification recipient limit is required")
	}
	recipients := make([]Recipient, 0)
	seen := make(map[string]struct{})
	for _, subscription := range subscriptions {
		if err := ValidateSubscription(subscription); err != nil {
			return nil, err
		}
		if !subscription.Enabled || subscription.DeviceID != intent.DeviceID || subscription.RuleID != intent.RuleID || subscription.Severity != intent.Severity {
			continue
		}
		if _, exists := seen[subscription.UserID]; exists {
			return nil, fmt.Errorf("%w: duplicate user", ErrInvalidSubscription)
		}
		if len(recipients) >= maxRecipients {
			return nil, ErrRecipientCapacity
		}
		seen[subscription.UserID] = struct{}{}
		recipients = append(recipients, Recipient{UserID: subscription.UserID, Channels: subscription.Channels})
	}
	return recipients, nil
}
