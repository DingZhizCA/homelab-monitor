/*
Package server tests authenticated plugin desired-state polling and status reports.
*/
package server

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

func pluginControlFixture(t *testing.T) (*PluginControlHandler, *plugins.Registry) {
	t.Helper()
	registry := plugins.NewRegistry()
	credentials := NewDeviceCredentials()
	if err := credentials.Set("node-a", "token-a"); err != nil {
		t.Fatal(err)
	}
	handler, err := NewPluginControlHandler(registry, credentials)
	if err != nil {
		t.Fatal(err)
	}
	return handler, registry
}

func TestPluginControlDesiredRequiresScopedHTTPSIdentity(t *testing.T) {
	handler, registry := pluginControlFixture(t)
	if err := registry.SetDesired(plugins.DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 3, Version: "1.2.0", Artifact: testArtifactRef(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := registry.SetDesired(plugins.DesiredState{DeviceID: "node-b", PluginID: "other", Revision: 1, Version: "1.0.0", Artifact: testArtifactRef(), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/api/v1/agent/plugins/desired?device_id=node-a", nil)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer token-a")
	w := httptest.NewRecorder()
	handler.Desired(w, request)
	if w.Code != http.StatusOK {
		t.Fatalf("desired status = %d, body=%s", w.Code, w.Body.String())
	}
	var response struct {
		DeviceID string                 `json:"device_id"`
		States   []plugins.DesiredState `json:"states"`
	}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.DeviceID != "node-a" || len(response.States) != 1 || response.States[0].PluginID != "health" {
		t.Fatalf("desired response = %+v", response)
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/agent/plugins/desired?device_id=node-b", nil)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer token-a")
	w = httptest.NewRecorder()
	handler.Desired(w, request)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-device desired status = %d", w.Code)
	}
}

func TestPluginControlDesiredRejectsNonCanonicalOrRepeatedDeviceID(t *testing.T) {
	handler, _ := pluginControlFixture(t)
	for _, target := range []string{
		"/api/v1/agent/plugins/desired",
		"/api/v1/agent/plugins/desired?device_id=",
		"/api/v1/agent/plugins/desired?device_id=%20node-a%20",
		"/api/v1/agent/plugins/desired?device_id=node-a&device_id=node-b",
	} {
		request := httptest.NewRequest(http.MethodGet, target, nil)
		request.TLS = &tls.ConnectionState{}
		response := httptest.NewRecorder()
		handler.Desired(response, request)
		if response.Code != http.StatusBadRequest {
			t.Errorf("Desired(%q) status=%d, want %d", target, response.Code, http.StatusBadRequest)
		}
	}
}

func testArtifactRef() plugins.ArtifactReference {
	return plugins.ArtifactReference{Platform: plugins.Platform{OS: "linux", Arch: "arm64"}, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}
}

func TestPluginControlReportedBatchIsScopedAndAtomic(t *testing.T) {
	handler, registry := pluginControlFixture(t)
	if err := registry.SetReported(plugins.ReportedState{DeviceID: "node-a", PluginID: "existing", ObservedRev: 2, AppliedRev: 2, Lifecycle: plugins.StateRunning}); err != nil {
		t.Fatal(err)
	}
	batch := struct {
		DeviceID string                  `json:"device_id"`
		States   []plugins.ReportedState `json:"states"`
	}{DeviceID: "node-a", States: []plugins.ReportedState{
		{DeviceID: "node-a", PluginID: "new", ObservedRev: 1, AppliedRev: 1, Lifecycle: plugins.StateRunning},
		{DeviceID: "node-a", PluginID: "existing", ObservedRev: 1, AppliedRev: 1, Lifecycle: plugins.StateRunning},
	}}
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/plugins/reported", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer token-a")
	w := httptest.NewRecorder()
	handler.Reported(w, request)
	if w.Code != http.StatusConflict {
		t.Fatalf("stale batch status = %d, body=%s", w.Code, w.Body.String())
	}
	if _, ok := registry.Reported("node-a", "new"); ok {
		t.Fatal("valid prefix of rejected batch was partially committed")
	}

	batch.States[1].ObservedRev = 2
	batch.States[1].AppliedRev = 2
	body, err = json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/agent/plugins/reported", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer token-a")
	w = httptest.NewRecorder()
	handler.Reported(w, request)
	if w.Code != http.StatusAccepted {
		t.Fatalf("valid batch status = %d, body=%s", w.Code, w.Body.String())
	}
}

func TestPluginControlReportedRejectsCrossDeviceState(t *testing.T) {
	handler, registry := pluginControlFixture(t)
	body := []byte(`{"device_id":"node-a","states":[{"device_id":"node-b","plugin_id":"bad","observed_revision":1,"applied_revision":1,"lifecycle_state":"running"}]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/plugins/reported", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer token-a")
	w := httptest.NewRecorder()
	handler.Reported(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-device report status = %d", w.Code)
	}
	if _, ok := registry.Reported("node-b", "bad"); ok {
		t.Fatal("cross-device report reached registry")
	}
}

func TestPluginControlReportedRejectsUntrimmedBatchDeviceID(t *testing.T) {
	handler, registry := pluginControlFixture(t)
	body := []byte(`{"device_id":" node-a ","states":[]}`)
	request := httptest.NewRequest(http.MethodPost, "/api/v1/agent/plugins/reported", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer token-a")
	response := httptest.NewRecorder()
	handler.Reported(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("untrimmed report status=%d body=%s", response.Code, response.Body.String())
	}
	if reports := registry.ReportedForDevice("node-a"); len(reports) != 0 {
		t.Fatalf("untrimmed report mutated state: %+v", reports)
	}
}
