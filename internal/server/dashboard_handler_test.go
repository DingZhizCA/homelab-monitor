/*
Package server tests the authenticated Dashboard API contract.

The repository fake keeps routes testable without selecting application storage.
*/
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

type dashboardHTTPRepo struct{ lastAction string }

func (m *dashboardHTTPRepo) value(action string) dashboard.Dashboard {
	m.lastAction = action
	return dashboard.Dashboard{ID: "dash-1", Name: "Shared", Scope: dashboard.ScopeShared}
}

func (m *dashboardHTTPRepo) ListVisible(context.Context, string, auth.Role) ([]dashboard.Dashboard, error) {
	return []dashboard.Dashboard{m.value("list")}, nil
}

func (m *dashboardHTTPRepo) GetVisible(context.Context, string, auth.Role, string) (dashboard.Dashboard, error) {
	return m.value("get"), nil
}

func (m *dashboardHTTPRepo) DraftForOwner(context.Context, string, string, uint64) (dashboard.Document, error) {
	m.lastAction = "draft_for_owner"
	return dashboard.Document{}, nil
}

func (m *dashboardHTTPRepo) CreatePersonal(context.Context, string, string, dashboard.Document) (dashboard.Dashboard, error) {
	return m.value("create_personal"), nil
}

func (m *dashboardHTTPRepo) CreateSharedDraft(context.Context, string, string, dashboard.Document, dashboard.AuditEventFactory) (dashboard.Dashboard, error) {
	return m.value("create_shared"), nil
}

func (m *dashboardHTTPRepo) SavePersonal(context.Context, string, string, uint64, dashboard.Document) (dashboard.Dashboard, error) {
	return m.value("save_personal"), nil
}

func (m *dashboardHTTPRepo) SaveSharedDraft(context.Context, string, string, uint64, dashboard.Document, audit.Event) (dashboard.Dashboard, error) {
	return m.value("save_draft"), nil
}

func (m *dashboardHTTPRepo) PublishShared(context.Context, string, string, uint64, audit.Event) (dashboard.Dashboard, error) {
	return m.value("publish"), nil
}

func (m *dashboardHTTPRepo) ListPublishedRevisions(context.Context, string, string) ([]dashboard.Revision, error) {
	m.lastAction = "list_revisions"
	return []dashboard.Revision{{Number: 3}}, nil
}

func (m *dashboardHTTPRepo) PublishHistoricalAsNew(context.Context, string, string, uint64, uint64, audit.Event) (dashboard.Dashboard, error) {
	return m.value("rollback"), nil
}

func (m *dashboardHTTPRepo) CopyVisibleToPersonal(context.Context, string, auth.Role, string, string) (dashboard.Dashboard, error) {
	return m.value("copy"), nil
}

type dashboardHTTPPublicationPolicy struct{}

func (dashboardHTTPPublicationPolicy) ValidateViewerAccess(context.Context, dashboard.Document) error {
	return nil
}

func TestDashboardRoutesRequireSessionOriginAndRole(t *testing.T) {
	fixture := newAuthTestFixture(t)
	repository := &dashboardHTTPRepo{}
	service, err := dashboard.NewService(repository, dashboardHTTPPublicationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewDashboardHandler(service, fixture.handler, 2048)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: fixture.handler, Dashboards: handler})
	if err != nil {
		t.Fatal(err)
	}
	token, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	request := authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboards", "")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous dashboard list status=%d body=%s", response.Code, response.Body.String())
	}

	request = authRequest(http.MethodPost, "https://monitor.example/api/v1/dashboards", `{"name":"Shared","scope":"shared","document":{"widgets":[],"layouts":[]}}`)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || repository.lastAction != "create_shared" {
		t.Fatalf("Owner dashboard create status=%d action=%q body=%s", response.Code, repository.lastAction, response.Body.String())
	}

	owner, ok, err := fixture.users.ByUsername(context.Background(), "owner")
	if err != nil || !ok {
		t.Fatalf("find bootstrap Owner: found=%t err=%v", ok, err)
	}
	if err := fixture.service.CreateUser(context.Background(), auth.Principal{UserID: owner.ID, Role: auth.RoleOwner}, "viewer", "viewer-password", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	viewerToken, _, err := fixture.service.Login(context.Background(), "viewer", "viewer-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request = authRequest(http.MethodPost, "https://monitor.example/api/v1/dashboards", `{"name":"Shared","scope":"shared","document":{"widgets":[],"layouts":[]}}`)
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	request.Header.Set("Origin", testAuthOrigin)
	repository.lastAction = "unchanged"
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || repository.lastAction != "unchanged" {
		t.Fatalf("Viewer shared create status=%d action=%q body=%s", response.Code, repository.lastAction, response.Body.String())
	}

	request = authRequest(http.MethodPost, "https://monitor.example/api/v1/dashboards", strings.Repeat("x", 2049))
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("oversized dashboard body status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestDashboardRouterRequiresMatchingAuthHandler(t *testing.T) {
	fixture := newAuthTestFixture(t)
	service, err := dashboard.NewService(&dashboardHTTPRepo{}, dashboardHTTPPublicationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewDashboardHandler(service, fixture.handler, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Dashboards: handler}); err == nil {
		t.Fatal("Dashboard handler mounted without its matching auth handler")
	}
}

func TestDashboardHTTPFlowWithMemoryRepositorySeparatesDraftAndPublishedViews(t *testing.T) {
	fixture := newAuthTestFixture(t)
	repository := dashboard.NewMemoryRepository()
	service, err := dashboard.NewService(repository, dashboardHTTPPublicationPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewDashboardHandler(service, fixture.handler, 4096)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: fixture.handler, Dashboards: handler})
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
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
	viewerToken, _, err := fixture.service.Login(context.Background(), "viewer", "viewer-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	create := authRequest(http.MethodPost, "https://monitor.example/api/v1/dashboards", `{"name":"Shared","scope":"shared","document":{"widgets":[{"id":"cpu","kind":"metric_card","title":"CPU","descriptor_id":"cpu.usage_percent"}],"layouts":[]}}`)
	create.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	create.Header.Set("Origin", testAuthOrigin)
	created := httptest.NewRecorder()
	router.ServeHTTP(created, create)
	if created.Code != http.StatusCreated {
		t.Fatalf("create shared dashboard status=%d body=%s", created.Code, created.Body.String())
	}
	var shared dashboard.Dashboard
	if err := json.Unmarshal(created.Body.Bytes(), &shared); err != nil {
		t.Fatal(err)
	}

	viewerList := authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboards", "")
	viewerList.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	listResponse := httptest.NewRecorder()
	router.ServeHTTP(listResponse, viewerList)
	if listResponse.Code != http.StatusOK || !strings.Contains(listResponse.Body.String(), `"dashboards":[]`) {
		t.Fatalf("unpublished dashboard list status=%d body=%s", listResponse.Code, listResponse.Body.String())
	}

	publish := authRequest(http.MethodPost, "https://monitor.example/api/v1/dashboards/"+shared.ID+"/publish", `{"expected_draft_revision":1}`)
	publish.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	publish.Header.Set("Origin", testAuthOrigin)
	publishResponse := httptest.NewRecorder()
	router.ServeHTTP(publishResponse, publish)
	if publishResponse.Code != http.StatusOK {
		t.Fatalf("publish status=%d body=%s", publishResponse.Code, publishResponse.Body.String())
	}
	ownerHistoryRequest := authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboards/"+shared.ID+"/revisions", "")
	ownerHistoryRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	ownerHistoryResponse := httptest.NewRecorder()
	router.ServeHTTP(ownerHistoryResponse, ownerHistoryRequest)
	var publishedHistory struct {
		Revisions []dashboard.Revision `json:"revisions"`
	}
	if ownerHistoryResponse.Code != http.StatusOK || json.Unmarshal(ownerHistoryResponse.Body.Bytes(), &publishedHistory) != nil || len(publishedHistory.Revisions) != 1 || publishedHistory.Revisions[0].Number != 1 {
		t.Fatalf("Owner revision history status=%d body=%s", ownerHistoryResponse.Code, ownerHistoryResponse.Body.String())
	}
	viewerHistoryRequest := authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboards/"+shared.ID+"/revisions", "")
	viewerHistoryRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	viewerHistoryResponse := httptest.NewRecorder()
	router.ServeHTTP(viewerHistoryResponse, viewerHistoryRequest)
	if viewerHistoryResponse.Code != http.StatusForbidden {
		t.Fatalf("Viewer revision history status=%d body=%s", viewerHistoryResponse.Code, viewerHistoryResponse.Body.String())
	}

	viewerGet := authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboards/"+shared.ID, "")
	viewerGet.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	getResponse := httptest.NewRecorder()
	router.ServeHTTP(getResponse, viewerGet)
	var visible dashboard.Dashboard
	if getResponse.Code != http.StatusOK || json.Unmarshal(getResponse.Body.Bytes(), &visible) != nil {
		t.Fatalf("Viewer dashboard status=%d body=%s", getResponse.Code, getResponse.Body.String())
	}
	if visible.Draft != nil || visible.Published == nil || visible.Published.Number != 1 {
		t.Fatalf("Viewer received draft or missed publication: %+v", visible)
	}

	edit := authRequest(http.MethodPut, "https://monitor.example/api/v1/dashboards/"+shared.ID+"/draft", `{"expected_revision":1,"document":{"widgets":[{"id":"cpu","kind":"metric_card","title":"Changed","descriptor_id":"cpu.usage_percent"}],"layouts":[]}}`)
	edit.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	edit.Header.Set("Origin", testAuthOrigin)
	editResponse := httptest.NewRecorder()
	router.ServeHTTP(editResponse, edit)
	if editResponse.Code != http.StatusOK {
		t.Fatalf("Owner draft edit status=%d body=%s", editResponse.Code, editResponse.Body.String())
	}

	viewerGet = authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboards/"+shared.ID, "")
	viewerGet.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	getResponse = httptest.NewRecorder()
	router.ServeHTTP(getResponse, viewerGet)
	if err := json.Unmarshal(getResponse.Body.Bytes(), &visible); err != nil {
		t.Fatal(err)
	}
	if visible.Published.Document.Widgets[0].Title != "CPU" || visible.Draft != nil {
		t.Fatalf("unpublished edit leaked to Viewer: %+v", visible)
	}
	publishSecond := authRequest(http.MethodPost, "https://monitor.example/api/v1/dashboards/"+shared.ID+"/publish", `{"expected_draft_revision":2}`)
	publishSecond.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	publishSecond.Header.Set("Origin", testAuthOrigin)
	publishSecondResponse := httptest.NewRecorder()
	router.ServeHTTP(publishSecondResponse, publishSecond)
	if publishSecondResponse.Code != http.StatusOK {
		t.Fatalf("second publish status=%d body=%s", publishSecondResponse.Code, publishSecondResponse.Body.String())
	}
	rollback := authRequest(http.MethodPost, "https://monitor.example/api/v1/dashboards/"+shared.ID+"/rollback", `{"target_published_revision":1,"expected_published_revision":2}`)
	rollback.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	rollback.Header.Set("Origin", testAuthOrigin)
	rollbackResponse := httptest.NewRecorder()
	router.ServeHTTP(rollbackResponse, rollback)
	var rolledBack dashboard.Dashboard
	if rollbackResponse.Code != http.StatusOK || json.Unmarshal(rollbackResponse.Body.Bytes(), &rolledBack) != nil || rolledBack.Published == nil || rolledBack.Published.Number != 3 {
		t.Fatalf("rollback status=%d body=%s", rollbackResponse.Code, rollbackResponse.Body.String())
	}
	ownerHistoryRequest = authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboards/"+shared.ID+"/revisions", "")
	ownerHistoryRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	ownerHistoryResponse = httptest.NewRecorder()
	router.ServeHTTP(ownerHistoryResponse, ownerHistoryRequest)
	publishedHistory = struct {
		Revisions []dashboard.Revision `json:"revisions"`
	}{}
	if ownerHistoryResponse.Code != http.StatusOK || json.Unmarshal(ownerHistoryResponse.Body.Bytes(), &publishedHistory) != nil || len(publishedHistory.Revisions) != 3 || publishedHistory.Revisions[0].Number != 3 || publishedHistory.Revisions[1].Number != 2 || publishedHistory.Revisions[2].Number != 1 {
		t.Fatalf("revision history after rollback status=%d body=%s", ownerHistoryResponse.Code, ownerHistoryResponse.Body.String())
	}
	edit = authRequest(http.MethodPut, "https://monitor.example/api/v1/dashboards/"+shared.ID+"/draft", `{"expected_revision":2,"document":{"widgets":[],"layouts":[]}}`)
	edit.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	edit.Header.Set("Origin", testAuthOrigin)
	editResponse = httptest.NewRecorder()
	router.ServeHTTP(editResponse, edit)
	if editResponse.Code != http.StatusForbidden {
		t.Fatalf("Viewer draft mutation status=%d body=%s", editResponse.Code, editResponse.Body.String())
	}
}
