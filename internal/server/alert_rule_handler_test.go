/*
Package server tests the authenticated alert-rule HTTP boundary.

The tests use the in-memory repository so route, CAS, and audit behavior are
exercised together without choosing production persistence.
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
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

func TestAlertRuleRouteRequiresOwnerOriginAndCAS(t *testing.T) {
	fixture := newAuthTestFixture(t)
	registry := dashboard.NewMetricRegistry()
	rules, err := alerts.NewMemoryRuleRepository(registry, alerts.Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	service, err := alerts.NewRuleService(rules, registry, alerts.Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewAlertRuleHandler(service, fixture.handler, 4096)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: fixture.handler, AlertRules: handler})
	if err != nil {
		t.Fatal(err)
	}
	owner, ok, err := fixture.users.ByUsername(context.Background(), "owner")
	if err != nil || !ok {
		t.Fatalf("find Owner: found=%t err=%v", ok, err)
	}
	if err := fixture.service.CreateUser(context.Background(), auth.Principal{UserID: owner.ID, Role: auth.RoleOwner}, "viewer", "viewer-password", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, _, err := fixture.service.Login(context.Background(), "viewer", "viewer-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	body := `{"expected_current":0,"rule":{"id":"high-cpu","device_id":"node/a","revision":1,"severity":"warning","enabled":true,"expression":{"metric":{"descriptor_id":"cpu.usage_percent","operator":"gt","threshold":80,"unit":"percent"}}}}`
	request := authRequest(http.MethodPut, "https://monitor.example/api/v1/owner/alert-rules", body)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || len(rules.AuditEvents()) != 0 {
		t.Fatalf("wrong-origin save status=%d events=%+v body=%s", response.Code, rules.AuditEvents(), response.Body.String())
	}

	request = authRequest(http.MethodPut, "https://monitor.example/api/v1/owner/alert-rules", body)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("save rule status=%d body=%s", response.Code, response.Body.String())
	}

	request = authRequest(http.MethodGet, "https://monitor.example/api/v1/owner/alert-rules?device_id=node%2Fa", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"high-cpu"`) {
		t.Fatalf("list rules status=%d body=%s", response.Code, response.Body.String())
	}

	conflict := strings.Replace(body, `"revision":1`, `"revision":2`, 1)
	request = authRequest(http.MethodPut, "https://monitor.example/api/v1/owner/alert-rules", conflict)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || len(rules.AuditEvents()) != 1 {
		t.Fatalf("stale save status=%d events=%+v body=%s", response.Code, rules.AuditEvents(), response.Body.String())
	}

	second := `{"expected_current":1,"rule":{"id":"high-cpu","device_id":"node/a","revision":2,"severity":"warning","enabled":true,"expression":{"metric":{"descriptor_id":"cpu.usage_percent","operator":"gt","threshold":90,"unit":"percent"}}}}`
	request = authRequest(http.MethodPut, "https://monitor.example/api/v1/owner/alert-rules", second)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("second rule revision status=%d body=%s", response.Code, response.Body.String())
	}

	request = authRequest(http.MethodGet, "https://monitor.example/api/v1/owner/alert-rules/revisions?device_id=node%2Fa&rule_id=high-cpu", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"revision":2`) {
		t.Fatalf("rule history status=%d body=%s", response.Code, response.Body.String())
	}

	rollback := `{"device_id":"node/a","rule_id":"high-cpu","target_revision":1,"new_revision":3,"expected_current":2}`
	request = authRequest(http.MethodPost, "https://monitor.example/api/v1/owner/alert-rules/revisions", rollback)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	current, found, currentErr := rules.Current(context.Background(), "node/a", "high-cpu")
	if response.Code != http.StatusNoContent || currentErr != nil || !found || current.Revision != 3 || current.Expression.Metric.Threshold != 80 {
		t.Fatalf("rollback status=%d current=%+v found=%t err=%v body=%s", response.Code, current, found, currentErr, response.Body.String())
	}
	events := rules.AuditEvents()
	if len(events) != 3 || events[2].Action != alerts.RuleRollbackAuditAction {
		t.Fatalf("rollback audit events=%+v", events)
	}

	request = authRequest(http.MethodGet, "https://monitor.example/api/v1/owner/alert-rules?device_id=node%2Fa", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("Viewer list status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterRejectsAlertRuleHandlerWithDifferentAuth(t *testing.T) {
	first := newAuthTestFixture(t)
	second := newAuthTestFixture(t)
	registry := dashboard.NewMetricRegistry()
	repository, err := alerts.NewMemoryRuleRepository(registry, alerts.Limits{MaxDepth: 1, MaxConditions: 1})
	if err != nil {
		t.Fatal(err)
	}
	service, err := alerts.NewRuleService(repository, registry, alerts.Limits{MaxDepth: 1, MaxConditions: 1})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewAlertRuleHandler(service, first.handler, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: second.handler, AlertRules: handler}); err == nil {
		t.Fatal("router accepted alert-rule handler with a different auth boundary")
	}
}
