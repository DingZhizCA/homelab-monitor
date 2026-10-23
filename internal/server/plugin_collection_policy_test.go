/*
Package server tests authenticated collection policy publication and polling.
*/
package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

type collectionPolicyStore struct {
	desired  []plugins.DesiredState
	policies []plugins.CollectionPolicy
	events   []audit.Event
}

func (s *collectionPolicyStore) DesiredForDeviceContext(_ context.Context, deviceID string) ([]plugins.DesiredState, error) {
	var result []plugins.DesiredState
	for _, state := range s.desired {
		if state.DeviceID == deviceID {
			result = append(result, state)
		}
	}
	return result, nil
}

func (s *collectionPolicyStore) CollectionPoliciesForDeviceContext(_ context.Context, deviceID string) ([]plugins.CollectionPolicy, error) {
	var result []plugins.CollectionPolicy
	for _, policy := range s.policies {
		if policy.DeviceID == deviceID {
			result = append(result, policy)
		}
	}
	return result, nil
}

func (s *collectionPolicyStore) SetCollectionPolicyWithAuditContext(_ context.Context, policy plugins.CollectionPolicy, event audit.Event) error {
	s.policies = []plugins.CollectionPolicy{policy}
	s.events = append(s.events, event)
	return nil
}

func TestPluginCollectionPolicyRequiresDeclaredCollectorAndServesAgent(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	bundle, key, err := plugins.SignPackage(privateKey, plugins.Manifest{
		PluginID: "health", Version: "1.0.0", Publisher: "test.publisher",
		Platform: plugins.Platform{OS: "linux", Arch: "amd64"}, Protocol: plugins.ProtocolRange{Min: 1, Max: 1},
		Collectors: []plugins.CollectorSpec{{ID: "status"}},
	}, []byte("plugin"))
	if err != nil {
		t.Fatal(err)
	}
	store := &collectionPolicyStore{desired: []plugins.DesiredState{{
		DeviceID: "node-a", PluginID: "health", Revision: 1, Version: "1.0.0", Enabled: true,
		Artifact: plugins.ArtifactReference{Platform: key.Platform, SHA256: key.SHA256},
	}}}
	devices := NewDeviceCredentials()
	if err := devices.Set("node-a", "device-token"); err != nil {
		t.Fatal(err)
	}
	handler, err := NewPluginCollectionPolicyHandler(store, fixedArtifactRepository{key: key, bundle: bundle}, devices)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), CollectionPolicies: handler}); err == nil {
		t.Fatal("collection policy routes without human authentication were accepted")
	}
	body := `{"device_id":"node-a","plugin_id":"health","collector_id":"status","revision":1,"plugin_revision":1,"enabled":true,"interval_seconds":60,"timeout_milliseconds":5000,"config":{}}`
	request := httptest.NewRequest(http.MethodPut, "/api/v1/owner/plugins/collection-policies", bytes.NewBufferString(body))
	request = withAuthenticatedPrincipal(request, auth.Principal{UserID: "owner-id", Username: "owner", Role: auth.RoleOwner})
	response := httptest.NewRecorder()
	handler.SetDesired(response, request)
	if response.Code != http.StatusAccepted || len(store.policies) != 1 || len(store.events) != 1 {
		t.Fatalf("publish status=%d policies=%+v events=%+v body=%s", response.Code, store.policies, store.events, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/v1/agent/plugins/collection-policies?device_id=node-a", nil)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer device-token")
	response = httptest.NewRecorder()
	handler.Desired(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("poll status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		Policies []plugins.CollectionPolicy `json:"policies"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || len(result.Policies) != 1 {
		t.Fatalf("poll result=%+v err=%v", result, err)
	}

	bad := bytes.ReplaceAll([]byte(body), []byte(`"status"`), []byte(`"missing"`))
	request = httptest.NewRequest(http.MethodPut, "/api/v1/owner/plugins/collection-policies", bytes.NewReader(bad))
	request = withAuthenticatedPrincipal(request, auth.Principal{UserID: "owner-id", Username: "owner", Role: auth.RoleOwner})
	response = httptest.NewRecorder()
	handler.SetDesired(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("undeclared collector status=%d body=%s", response.Code, response.Body.String())
	}
}
