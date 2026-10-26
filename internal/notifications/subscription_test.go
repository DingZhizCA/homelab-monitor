/*
Package notifications verifies exact explicit subscription matching and bounded
recipient fanout without applying role defaults or wildcard behavior.
*/
package notifications

import (
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
)

func TestSelectRecipientsMatchesExactEnabledSubscriptions(t *testing.T) {
	intent := subscriptionIntent(t, alerts.SeverityWarning)
	subscriptions := []Subscription{
		{UserID: "user-a", DeviceID: intent.DeviceID, RuleID: intent.RuleID, Severity: alerts.SeverityWarning, Revision: 1, Enabled: true, Channels: Channels{InApp: true, Push: true}},
		{UserID: "user-b", DeviceID: intent.DeviceID, RuleID: intent.RuleID, Severity: alerts.SeverityCritical, Revision: 1, Enabled: true, Channels: Channels{InApp: true}},
		{UserID: "user-c", DeviceID: intent.DeviceID, RuleID: intent.RuleID, Severity: alerts.SeverityWarning, Revision: 1, Enabled: false, Channels: Channels{InApp: true}},
	}
	recipients, err := SelectRecipients(intent, subscriptions, 1)
	if err != nil || len(recipients) != 1 || recipients[0].UserID != "user-a" || !recipients[0].Channels.InApp || !recipients[0].Channels.Push {
		t.Fatalf("recipients=%+v err=%v", recipients, err)
	}
}

func TestSelectRecipientsEnforcesBoundAndRejectsDuplicateUser(t *testing.T) {
	intent := subscriptionIntent(t, alerts.SeverityCritical)
	subscription := Subscription{UserID: "user-a", DeviceID: intent.DeviceID, RuleID: intent.RuleID, Severity: intent.Severity, Revision: 1, Enabled: true, Channels: Channels{InApp: true}}
	other := subscription
	other.UserID = "user-b"
	if _, err := SelectRecipients(intent, []Subscription{subscription, other}, 1); !errors.Is(err, ErrRecipientCapacity) {
		t.Fatalf("recipient capacity error=%v", err)
	}
	if _, err := SelectRecipients(intent, []Subscription{subscription, subscription}, 2); !errors.Is(err, ErrInvalidSubscription) {
		t.Fatalf("duplicate recipient error=%v", err)
	}
}

func TestValidateSubscriptionRestrictsInfoToRecordOnly(t *testing.T) {
	subscription := Subscription{UserID: "user-a", DeviceID: "node-a", RuleID: "high-cpu", Severity: alerts.SeverityInfo, Revision: 1, Enabled: true, Channels: Channels{InApp: true, Push: true}}
	if !errors.Is(ValidateSubscription(subscription), ErrInvalidSubscription) {
		t.Fatal("Info Push subscription was accepted")
	}
	subscription.Channels.Push = false
	if err := ValidateSubscription(subscription); err != nil {
		t.Fatalf("Info in-app subscription: %v", err)
	}
}

func subscriptionIntent(t *testing.T, severity alerts.Severity) Intent {
	t.Helper()
	intent := Intent{
		TransitionID:   intentID("subscription-transition", IntentFiring),
		DeviceID:       "node-a",
		RuleID:         "high-cpu",
		RuleRevision:   1,
		Severity:       severity,
		Kind:           IntentFiring,
		DeliveryPolicy: DeliveryPushEligible,
		CreatedAt:      time.Date(2026, 10, 26, 17, 0, 0, 0, time.UTC),
	}
	if severity == alerts.SeverityInfo {
		intent.DeliveryPolicy = DeliveryRecordOnly
	}
	intent.ID = intentID(intent.TransitionID, intent.Kind)
	if err := ValidateIntent(intent); err != nil {
		t.Fatal(err)
	}
	return intent
}
