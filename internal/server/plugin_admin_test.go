package server

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

func TestPluginAdminRequiresArtifactAndAcceptsExplicitDisable(t *testing.T) {
	registry := plugins.NewRegistry()
	key := plugins.ArtifactKey{PluginID: "health", Version: "1.0.0", Platform: testArtifactRef().Platform, SHA256: testArtifactRef().SHA256}
	artifacts := fixedArtifactRepository{key: key, bundle: plugins.PackageArtifact{Artifact: []byte("package")}}
	handler, err := NewPluginAdminHandler(registry, artifacts)
	if err != nil {
		t.Fatal(err)
	}

	request := ownerPluginRequest(`{"device_id":"node-a","plugin_id":"health","revision":1,"version":"1.0.0","artifact":{"platform":{"os":"linux","arch":"arm64"},"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},"enabled":true}`)
	response := httptest.NewRecorder()
	handler.SetDesired(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("available artifact status=%d body=%s", response.Code, response.Body.String())
	}
	if states := registry.DesiredForDevice("node-a"); len(states) != 1 || !states[0].Enabled {
		t.Fatalf("desired states after publication = %+v", states)
	}
	assertPluginAudit(t, registry.AuditEvents(), 1, "owner-id", "node-a/health")

	request = ownerPluginRequest(`{"device_id":"node-a","plugin_id":"health","revision":2,"enabled":false}`)
	response = httptest.NewRecorder()
	handler.SetDesired(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("disable status=%d body=%s", response.Code, response.Body.String())
	}
	if states := registry.DesiredForDevice("node-a"); len(states) != 1 || states[0].Enabled || states[0].Revision != 2 {
		t.Fatalf("desired states after disable = %+v", states)
	}
	assertPluginAudit(t, registry.AuditEvents(), 2, "owner-id", "node-a/health")
}

func TestRouterRestrictsPluginDesiredMutationToOwnerAndConfiguredOrigin(t *testing.T) {
	fixture := newAuthTestFixture(t)
	owner, _, err := fixture.users.ByUsername(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CreateUser(httptest.NewRequest(http.MethodGet, "/", nil).Context(), auth.Principal{UserID: owner.ID, Username: owner.Username, Role: auth.RoleOwner}, "viewer", "viewer-password", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	registry := plugins.NewRegistry()
	key := plugins.ArtifactKey{PluginID: "health", Version: "1.0.0", Platform: testArtifactRef().Platform, SHA256: testArtifactRef().SHA256}
	admin, err := NewPluginAdminHandler(registry, fixedArtifactRepository{key: key})
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: fixture.handler, PluginAdmin: admin})
	if err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := fixture.service.Login(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, _, err := fixture.service.Login(httptest.NewRequest(http.MethodGet, "/", nil).Context(), "viewer", "viewer-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"device_id":"node-a","plugin_id":"health","revision":1,"enabled":false}`
	for _, test := range []struct {
		name   string
		token  string
		origin string
		status int
	}{
		{name: "anonymous", status: http.StatusUnauthorized, origin: testAuthOrigin},
		{name: "viewer", token: viewerToken, status: http.StatusForbidden, origin: testAuthOrigin},
		{name: "wrong origin", token: ownerToken, status: http.StatusForbidden, origin: "https://attacker.example"},
		{name: "owner", token: ownerToken, status: http.StatusAccepted, origin: testAuthOrigin},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := authRequest(http.MethodPut, "/api/v1/owner/plugins/desired", body)
			request.Header.Set("Origin", test.origin)
			if test.token != "" {
				request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: test.token})
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("status=%d want=%d body=%s", response.Code, test.status, response.Body.String())
			}
		})
	}
	assertPluginAudit(t, registry.AuditEvents(), 1, owner.ID, "node-a/health")
}

func TestPluginAdminRejectsUnavailableArtifactWithoutChangingRegistry(t *testing.T) {
	registry := plugins.NewRegistry()
	handler, err := NewPluginAdminHandler(registry, fixedArtifactRepository{})
	if err != nil {
		t.Fatal(err)
	}
	request := ownerPluginRequest(`{"device_id":"node-a","plugin_id":"health","revision":1,"version":"1.0.0","artifact":{"platform":{"os":"linux","arch":"arm64"},"sha256":"0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},"enabled":true}`)
	response := httptest.NewRecorder()
	handler.SetDesired(response, request)
	if response.Code != http.StatusConflict || len(registry.DesiredForDevice("node-a")) != 0 {
		t.Fatalf("unavailable artifact status=%d states=%+v", response.Code, registry.DesiredForDevice("node-a"))
	}
	if events := registry.AuditEvents(); len(events) != 0 {
		t.Fatalf("unavailable artifact produced audit events: %+v", events)
	}
}

func ownerPluginRequest(body string) *http.Request {
	request := httptest.NewRequest(http.MethodPut, "/api/v1/owner/plugins/desired", bytes.NewBufferString(body))
	return withAuthenticatedPrincipal(request, auth.Principal{UserID: "owner-id", Username: "owner", Role: auth.RoleOwner})
}

func assertPluginAudit(t *testing.T, events []audit.Event, wantCount int, actorID, resourceID string) {
	t.Helper()
	if len(events) != wantCount {
		t.Fatalf("audit count=%d want=%d events=%+v", len(events), wantCount, events)
	}
	event := events[len(events)-1]
	if event.ActorID != actorID || event.Action != plugins.DesiredStateAuditAction || event.ResourceType != plugins.DesiredStateAuditResourceType || event.ResourceID != resourceID {
		t.Fatalf("audit event = %+v", event)
	}
}
