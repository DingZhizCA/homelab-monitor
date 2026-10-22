package server

import (
	"crypto/tls"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

type fixedArtifactRepository struct {
	key    plugins.ArtifactKey
	bundle plugins.PackageArtifact
}

func (r fixedArtifactRepository) Get(key plugins.ArtifactKey) (plugins.PackageArtifact, error) {
	if key != r.key {
		return plugins.PackageArtifact{}, io.EOF
	}
	return r.bundle, nil
}

func TestPluginArtifactHandlerServesOnlyCurrentDeviceDesiredArtifact(t *testing.T) {
	registry := plugins.NewRegistry()
	state := plugins.DesiredState{
		DeviceID: "node-a", PluginID: "health", Revision: 2, Version: "1.2.0",
		Artifact: testArtifactRef(), Enabled: true,
	}
	if err := registry.SetDesired(state); err != nil {
		t.Fatal(err)
	}
	credentials := NewDeviceCredentials()
	if err := credentials.Set("node-a", "device-token"); err != nil {
		t.Fatal(err)
	}
	key := state.Artifact.Key(state.PluginID, state.Version)
	bundle := plugins.PackageArtifact{ManifestJSON: []byte(`{"manifest":true}`), Signature: []byte("signature"), Artifact: []byte("binary")}
	handler, err := NewPluginArtifactHandler(registry, fixedArtifactRepository{key: key, bundle: bundle}, credentials)
	if err != nil {
		t.Fatal(err)
	}
	request := artifactTestRequest(http.MethodGet, "node-a", key, "device-token")
	response := httptest.NewRecorder()
	handler.Download(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("download status = %d, body=%s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", response.Header().Get("Cache-Control"))
	}
	_, params, err := mime.ParseMediaType(response.Header().Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	reader := multipart.NewReader(response.Body, params["boundary"])
	parts := make(map[string][]byte)
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		contents, err := io.ReadAll(part)
		if err != nil {
			t.Fatal(err)
		}
		parts[part.FormName()] = contents
	}
	if string(parts["manifest.json"]) != string(bundle.ManifestJSON) || string(parts["manifest.sig"]) != string(bundle.Signature) || string(parts["artifact"]) != string(bundle.Artifact) {
		t.Fatalf("multipart package contents = %q", parts)
	}

	wrongKey := key
	wrongKey.SHA256 = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	request = artifactTestRequest(http.MethodGet, "node-a", wrongKey, "device-token")
	response = httptest.NewRecorder()
	handler.Download(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("non-desired artifact status = %d", response.Code)
	}
	request = artifactTestRequest(http.MethodGet, "node-a", key, "wrong-token")
	response = httptest.NewRecorder()
	handler.Download(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized artifact status = %d", response.Code)
	}
}

func artifactTestRequest(method, deviceID string, key plugins.ArtifactKey, token string) *http.Request {
	query := url.Values{
		"device_id": {deviceID}, "plugin_id": {key.PluginID}, "version": {key.Version},
		"os": {key.Platform.OS}, "arch": {key.Platform.Arch}, "sha256": {key.SHA256},
	}
	request := httptest.NewRequest(method, "/api/v1/agent/plugins/artifact?"+query.Encode(), nil)
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}
