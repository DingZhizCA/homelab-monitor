/*
Package server tests route assembly against existing API and SPA behavior.

These checks protect the externally visible paths while moving wiring out of
the executable entry point.
*/
package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestNewRouterPreservesAPIAndOptionalDashboardRoutes(t *testing.T) {
	directory := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(directory, "index.html"), []byte("dashboard"), 0o600); err != nil {
		t.Fatal(err)
	}
	api := NewHandler(NewStore(), NewBroker())
	router, err := NewRouter(RouterOptions{API: api, WebDirectory: directory})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path       string
		wantStatus int
		wantBody   string
	}{
		{path: "/api/v1/health", wantStatus: http.StatusOK, wantBody: `"status":"ok"`},
		{path: "/api/v1/devices", wantStatus: http.StatusOK, wantBody: "[]"},
		{path: "/dashboard", wantStatus: http.StatusOK, wantBody: "dashboard"},
	} {
		t.Run(test.path, func(t *testing.T) {
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", response.Code, test.wantStatus, response.Body.String())
			}
			if !strings.Contains(response.Body.String(), test.wantBody) {
				t.Fatalf("body %q does not contain %q", response.Body.String(), test.wantBody)
			}
		})
	}
}

func TestNewRouterMountsOptionalPluginControlRoutes(t *testing.T) {
	pluginControl, registry := pluginControlFixture(t)
	if err := registry.SetDesired(plugins.DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 1, Version: "1.0.0", Artifact: testArtifactRef(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), PluginControl: pluginControl})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/plugins/desired?device_id=node-a", nil)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer token-a")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"plugin_id":"health"`) {
		t.Fatalf("plugin desired route status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNewRouterRequiresAPIHandler(t *testing.T) {
	if _, err := NewRouter(RouterOptions{}); err == nil {
		t.Fatal("router without API handler was accepted")
	}
}

func TestNewRouterRequiresAuthenticationForPluginAdministration(t *testing.T) {
	admin, err := NewPluginAdminHandler(plugins.NewRegistry(), fixedArtifactRepository{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), PluginAdmin: admin}); err == nil {
		t.Fatal("router mounted plugin administration without human authentication")
	}
}
