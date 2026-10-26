/*
Package notifications verifies Session identity binding, rule existence checks,
and audit construction for explicit self-service subscriptions.
*/
package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

func TestSubscriptionServiceBindsActorAndRequiresExistingRule(t *testing.T) {
	at := time.Date(2026, 10, 26, 19, 0, 0, 0, time.UTC)
	repository := &subscriptionServiceRepository{}
	rules := &subscriptionServiceRules{found: true}
	service, err := NewSubscriptionService(repository, rules, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	actor := auth.Principal{UserID: "viewer-a", Role: auth.RoleViewer}
	subscription := Subscription{UserID: "other", DeviceID: "node-a", RuleID: "high-cpu", Severity: alerts.SeverityWarning, Revision: 1, Enabled: true, Channels: Channels{InApp: true}}
	if err := service.Save(context.Background(), actor, subscription, 0); err != nil {
		t.Fatal(err)
	}
	if repository.saved.UserID != actor.UserID || repository.actorID != actor.UserID || repository.event.ActorID != actor.UserID || repository.event.OccurredAt != at || repository.event.ResourceID != SubscriptionResourceID(repository.saved) {
		t.Fatalf("saved=%+v actor=%q event=%+v", repository.saved, repository.actorID, repository.event)
	}
	rules.found = false
	if err := service.Save(context.Background(), actor, subscription, 0); !errors.Is(err, ErrSubscriptionRuleNotFound) {
		t.Fatalf("missing rule error=%v", err)
	}
}

func TestSubscriptionServiceListsOnlySessionUser(t *testing.T) {
	repository := &subscriptionServiceRepository{listed: []Subscription{{UserID: "owner-a", DeviceID: "node-a", RuleID: "high-cpu", Severity: alerts.SeverityCritical, Revision: 1}}}
	service, err := NewSubscriptionService(repository, &subscriptionServiceRules{found: true}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(context.Background(), auth.Principal{UserID: "owner-a", Role: auth.RoleOwner}, 4)
	if err != nil || repository.listedUser != "owner-a" || repository.listedLimit != 4 || len(listed) != 1 {
		t.Fatalf("listed=%+v user=%q limit=%d err=%v", listed, repository.listedUser, repository.listedLimit, err)
	}
	if _, err := service.List(context.Background(), auth.Principal{}, 4); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("unauthorized list error=%v", err)
	}
}

type subscriptionServiceRepository struct {
	listed      []Subscription
	listedUser  string
	listedLimit int
	actorID     string
	saved       Subscription
	event       audit.Event
}

func (r *subscriptionServiceRepository) ListForUser(_ context.Context, userID string, limit int) ([]Subscription, error) {
	r.listedUser, r.listedLimit = userID, limit
	return append([]Subscription(nil), r.listed...), nil
}

func (r *subscriptionServiceRepository) SaveWithAudit(_ context.Context, actorID string, subscription Subscription, _ uint64, event audit.Event) error {
	r.actorID, r.saved, r.event = actorID, subscription, event
	return nil
}

type subscriptionServiceRules struct {
	found bool
}

func (r *subscriptionServiceRules) Current(_ context.Context, deviceID, ruleID string) (alerts.Rule, bool, error) {
	return alerts.Rule{DeviceID: deviceID, ID: ruleID, Revision: 1}, r.found, nil
}
