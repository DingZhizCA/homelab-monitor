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
	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

type testIncidentActivationRepository struct {
	actorID        string
	deviceID       string
	ruleID         string
	targetRevision uint64
	archives       []alerts.IncidentArchive
}

func (r *testIncidentActivationRepository) ActivateRuleRevisionWithAudit(_ context.Context, actorID, deviceID, ruleID string, targetRevision uint64, _ audit.Event) (alerts.IncidentActivationResult, error) {
	r.actorID, r.deviceID, r.ruleID, r.targetRevision = actorID, deviceID, ruleID, targetRevision
	return alerts.IncidentActivationResult{Ready: true}, nil
}

func (r *testIncidentActivationRepository) ListArchives(_ context.Context, _, _ string, _ int) ([]alerts.IncidentArchive, error) {
	return r.archives, nil
}

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
	activationRepository := &testIncidentActivationRepository{}
	activation, err := alerts.NewIncidentActivationService(activationRepository)
	if err != nil {
		t.Fatal(err)
	}
	archiveService, err := alerts.NewIncidentArchiveService(activationRepository, 8)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewIncidentHandler(service, activation, archiveService, fixture.handler, 4096)
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

	request = authRequest(http.MethodGet, "https://monitor.example/api/v1/alerts/incidents/history?device_id=node%2Fa&rule_id=high-cpu&limit=4", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Body.String() != `{"archives":[]}`+"\n" {
		t.Fatalf("Viewer archive query status=%d body=%s", response.Code, response.Body.String())
	}

	body := `{"device_id":"node/a","rule_id":"high-cpu","target_revision":2}`
	request = authRequest(http.MethodPost, "https://monitor.example/api/v1/owner/alerts/incidents/activate", body)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("Viewer activation status=%d body=%s", response.Code, response.Body.String())
	}

	request = authRequest(http.MethodPost, "https://monitor.example/api/v1/owner/alerts/incidents/activate", body)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || activationRepository.deviceID != "node/a" || activationRepository.ruleID != "high-cpu" || activationRepository.targetRevision != 2 || activationRepository.actorID != owner.ID {
		t.Fatalf("Owner activation status=%d repository=%+v body=%s", response.Code, activationRepository, response.Body.String())
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
	activation, err := alerts.NewIncidentActivationService(&testIncidentActivationRepository{})
	if err != nil {
		t.Fatal(err)
	}
	archiveService, err := alerts.NewIncidentArchiveService(&testIncidentActivationRepository{}, 4)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewIncidentHandler(service, activation, archiveService, first.handler, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: second.handler, Incidents: handler}); err == nil {
		t.Fatal("router accepted Incident handler with a different auth boundary")
	}
}
