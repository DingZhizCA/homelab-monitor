/*
Package server verifies authenticated self-service notification subscription
HTTP behavior, strict request scope, and Session-derived user identity.
*/
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

func TestNotificationSubscriptionRouteUsesSessionIdentity(t *testing.T) {
	fixture := newAuthTestFixture(t)
	owner, found, err := fixture.users.ByUsername(context.Background(), "owner")
	if err != nil || !found {
		t.Fatalf("Owner found=%t err=%v", found, err)
	}
	token, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	repository := &notificationSubscriptionHTTPRepository{}
	service, err := notifications.NewSubscriptionService(repository, notificationSubscriptionHTTPRules{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewNotificationSubscriptionHandler(service, fixture.handler, 4096)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: fixture.handler, NotificationSubscriptions: handler})
	if err != nil {
		t.Fatal(err)
	}

	request := authRequest(http.MethodGet, "https://monitor.example/api/v1/notifications/subscriptions?limit=4", "")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", response.Code, response.Body.String())
	}

	request = authRequest(http.MethodPut, "https://monitor.example/api/v1/notifications/subscriptions", `{"expected_revision":0,"subscription":{"device_id":"node-a","rule_id":"high-cpu","severity":"warning","revision":1,"enabled":true,"channels":{"in_app":true,"push":false}}}`)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.actorID != owner.ID || repository.saved.UserID != owner.ID || repository.saved.DeviceID != "node-a" || repository.saved.Severity != alerts.SeverityWarning {
		t.Fatalf("actor=%q saved=%+v", repository.actorID, repository.saved)
	}

	repository.listed = []notifications.Subscription{repository.saved}
	request = authRequest(http.MethodGet, "https://monitor.example/api/v1/notifications/subscriptions?limit=4", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"user_id":"`+owner.ID+`"`) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
}

type notificationSubscriptionHTTPRepository struct {
	actorID string
	saved   notifications.Subscription
	listed  []notifications.Subscription
}

func (r *notificationSubscriptionHTTPRepository) ListForUser(context.Context, string, int) ([]notifications.Subscription, error) {
	return append([]notifications.Subscription(nil), r.listed...), nil
}

func (r *notificationSubscriptionHTTPRepository) SaveWithAudit(_ context.Context, actorID string, subscription notifications.Subscription, _ uint64, _ audit.Event) error {
	r.actorID, r.saved = actorID, subscription
	return nil
}

type notificationSubscriptionHTTPRules struct{}

func (notificationSubscriptionHTTPRules) Current(_ context.Context, deviceID, ruleID string) (alerts.Rule, bool, error) {
	return alerts.Rule{DeviceID: deviceID, ID: ruleID, Revision: 1}, true, nil
}
