/*
Package agent tests the HTTPS plugin control client request and response boundary.
*/
package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

func TestPluginControlClientExchangesDeviceScopedState(t *testing.T) {
	desired := plugins.DesiredState{DeviceID: "node/a", PluginID: "health", Revision: 4, Version: "1.2.0", Artifact: plugins.ArtifactReference{Platform: plugins.Platform{OS: "linux", Arch: "arm64"}, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, Enabled: true}
	policy := plugins.CollectionPolicy{DeviceID: "node/a", PluginID: "health", CollectorID: "status", Revision: 2, PluginRevision: 4, Enabled: true, IntervalSeconds: 60, TimeoutMilliseconds: 5000, Config: []byte(`{}`)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
			t.Errorf("Authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/plugins/desired":
			if got := r.URL.Query().Get("device_id"); got != "node/a" {
				t.Errorf("device_id query = %q", got)
			}
			_ = json.NewEncoder(w).Encode(struct {
				DeviceID string                 `json:"device_id"`
				States   []plugins.DesiredState `json:"states"`
			}{"node/a", []plugins.DesiredState{desired}})
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/agent/plugins/reported":
			var request struct {
				DeviceID string                  `json:"device_id"`
				States   []plugins.ReportedState `json:"states"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode report: %v", err)
			}
			if request.DeviceID != "node/a" || len(request.States) != 1 || request.States[0].PluginID != "health" {
				t.Errorf("report body = %+v", request)
			}
			w.WriteHeader(http.StatusAccepted)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/agent/plugins/collection-policies":
			_ = json.NewEncoder(w).Encode(struct {
				DeviceID string                     `json:"device_id"`
				Policies []plugins.CollectionPolicy `json:"policies"`
			}{"node/a", []plugins.CollectionPolicy{policy}})
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client, err := NewPluginControlClient(server.URL, "device-token")
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	states, err := client.Desired(context.Background(), "node/a")
	if err != nil || len(states) != 1 || states[0] != desired {
		t.Fatalf("Desired() = %+v, %v", states, err)
	}
	if err := client.Reported(context.Background(), "node/a", []plugins.ReportedState{{DeviceID: "node/a", PluginID: "health", ObservedRev: 4, AppliedRev: 3, Lifecycle: plugins.StateRunning}}); err != nil {
		t.Fatal(err)
	}
	policies, err := client.CollectionPolicies(context.Background(), "node/a")
	if err != nil || len(policies) != 1 || !plugins.EqualCollectionPolicy(policies[0], policy) {
		t.Fatalf("CollectionPolicies() = %+v, %v", policies, err)
	}
}

func TestPluginControlClientDoesNotFollowCredentialRedirects(t *testing.T) {
	forwarded := make(chan struct{}, 1)
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		forwarded <- struct{}{}
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	redirect := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/capture", http.StatusFound)
	}))
	defer redirect.Close()
	client, err := NewPluginControlClient(redirect.URL, "secret")
	if err != nil {
		t.Fatal(err)
	}
	secureClient := redirect.Client()
	secureClient.CheckRedirect = client.client.CheckRedirect
	client.client = secureClient
	_, err = client.Desired(context.Background(), "node-a")
	if err == nil {
		t.Fatal("redirect response was accepted as desired state")
	}
	select {
	case <-forwarded:
		t.Fatal("credential-bearing request followed a redirect")
	default:
	}
}

func TestPluginControlClientRejectsAmbiguousCollectionPolicies(t *testing.T) {
	policy := plugins.CollectionPolicy{DeviceID: "node-a", PluginID: "health", CollectorID: "status", Revision: 1, PluginRevision: 1, Enabled: true, IntervalSeconds: 60, TimeoutMilliseconds: 1000, Config: []byte(`{}`)}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"device_id": "node-a", "policies": []plugins.CollectionPolicy{policy, policy}})
	}))
	defer server.Close()
	client, err := NewPluginControlClient(server.URL, "token")
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	if _, err := client.CollectionPolicies(context.Background(), "node-a"); err == nil {
		t.Fatal("duplicate collection policy was accepted")
	}
}

func TestCredentialHTTPClientRejectsRedirects(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://"+r.Host+"/target", http.StatusFound)
	}))
	server.Listener = listener
	server.Start()
	defer server.Close()

	request, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := newCredentialHTTPClient().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("redirect status = %d", response.StatusCode)
	}
}

func TestPluginControlClientRequiresHTTPSAndMatchingReportedDevice(t *testing.T) {
	if _, err := NewPluginControlClient("http://127.0.0.1:8080", "token"); err == nil {
		t.Fatal("HTTP control endpoint was accepted")
	}
	if _, err := NewPluginControlClient("https://server.example", " "); err == nil {
		t.Fatal("empty control token was accepted")
	}
	client, err := NewPluginControlClient("https://server.example", "token")
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Reported(context.Background(), "node-a", []plugins.ReportedState{{DeviceID: "node-b", PluginID: "x"}}); err == nil {
		t.Fatal("cross-device report was sent")
	}
	if _, err := client.Desired(context.Background(), " "); err == nil {
		t.Fatal("empty device ID was accepted for desired-state polling")
	}
	if _, err := client.Desired(context.Background(), " node-a "); err == nil {
		t.Fatal("untrimmed device ID was accepted for desired-state polling")
	}
	if err := client.Reported(context.Background(), "", nil); err == nil {
		t.Fatal("empty device ID was accepted for reporting")
	}
	if err := client.Reported(context.Background(), " node-a ", nil); err == nil {
		t.Fatal("untrimmed device ID was accepted for reporting")
	}
}

func TestPluginControlClientRequiresPositiveOperationTimeout(t *testing.T) {
	if _, err := newPluginControlClient("https://server.example", "token", 0); err == nil {
		t.Fatal("zero plugin control operation timeout was accepted")
	}
}

func TestPluginControlClientRejectsEnabledDesiredStateWithoutArtifactIdentity(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(struct {
			DeviceID string                 `json:"device_id"`
			States   []plugins.DesiredState `json:"states"`
		}{"node-a", []plugins.DesiredState{{
			DeviceID: "node-a", PluginID: "health", Revision: 1, Version: "1.0.0", Enabled: true,
		}}})
	}))
	defer server.Close()
	client, err := NewPluginControlClient(server.URL, "device-token")
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	if _, err := client.Desired(context.Background(), "node-a"); err == nil {
		t.Fatal("enabled desired state without artifact identity was accepted")
	}
}
