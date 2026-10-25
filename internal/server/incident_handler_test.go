/*
Package server tests the authenticated, read-only Incident HTTP boundary.
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
)

func TestIncidentRouteAllowsOwnerAndViewerDeviceReads(t *testing.T) {
	fixture := newAuthTestFixture(t)
	repository, err := alerts.NewMemoryIncidentRepository(2)
	if err != nil {
		t.Fatal(err)
	}
	service, err := alerts.NewIncidentService(repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewIncidentHandler(service, fixture.handler)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: fixture.handler, Incidents: handler})
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
	zero := time.Duration(0)
	rule := alerts.Rule{ID: "high-cpu", DeviceID: "node/a", Revision: 1, Enabled: false}
	if _, err := repository.Advance(context.Background(), rule, alerts.EvaluationResult{RuleID: rule.ID, Revision: rule.Revision, State: alerts.StateDisabled}, time.Unix(100, 0), alerts.IncidentPolicy{PendingFor: &zero, RecoveryFor: &zero, UnknownBehavior: alerts.UncertainHold, DisabledBehavior: alerts.UncertainHold}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodGet, "https://monitor.example/api/v1/alerts/incidents?device_id=node%2Fa", nil)
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", response.Code, response.Body.String())
	}

	for _, token := range []string{ownerToken, viewerToken} {
		request = httptest.NewRequest(http.MethodGet, "https://monitor.example/api/v1/alerts/incidents?device_id=node%2Fa", nil)
		request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"rule_id":"high-cpu"`) {
			t.Fatalf("authenticated status=%d body=%s", response.Code, response.Body.String())
		}
	}

	request = httptest.NewRequest(http.MethodGet, "https://monitor.example/api/v1/alerts/incidents?device_id=%20node%2Fa%20", nil)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid query status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestRouterRejectsIncidentHandlerWithDifferentAuth(t *testing.T) {
	first := newAuthTestFixture(t)
	second := newAuthTestFixture(t)
	repository, err := alerts.NewMemoryIncidentRepository(1)
	if err != nil {
		t.Fatal(err)
	}
	service, err := alerts.NewIncidentService(repository)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewIncidentHandler(service, first.handler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: second.handler, Incidents: handler}); err == nil {
		t.Fatal("router accepted Incident handler with a different auth boundary")
	}
}
