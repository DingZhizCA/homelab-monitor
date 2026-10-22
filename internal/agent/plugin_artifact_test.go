package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

func TestPluginControlClientDownloadsContentAddressedArtifact(t *testing.T) {
	artifact := []byte("plugin executable bytes")
	digest := sha256.Sum256(artifact)
	manifest := plugins.Manifest{
		PluginID: "health", Version: "1.2.0", Publisher: "publisher",
		Platform:       plugins.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Protocol:       plugins.ProtocolRange{Min: 1, Max: 1},
		Collectors:     []plugins.CollectorSpec{{ID: "health"}},
		ArtifactSHA256: hex.EncodeToString(digest[:]),
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer device-token" {
			t.Errorf("unexpected authorization header: %q", r.Header.Get("Authorization"))
		}
		if r.URL.Query().Get("sha256") != manifest.ArtifactSHA256 || r.URL.Query().Get("plugin_id") != manifest.PluginID {
			t.Errorf("artifact identity query = %v", r.URL.Query())
		}
		writer := multipart.NewWriter(w)
		w.Header().Set("Content-Type", writer.FormDataContentType())
		for name, contents := range map[string][]byte{
			"manifest.json": manifestJSON,
			"manifest.sig":  signature,
			"artifact":      artifact,
		} {
			part, err := writer.CreateFormField(name)
			if err != nil {
				t.Errorf("create multipart part: %v", err)
				return
			}
			if _, err := part.Write(contents); err != nil {
				t.Errorf("write multipart part: %v", err)
				return
			}
		}
		if err := writer.Close(); err != nil {
			t.Errorf("close multipart writer: %v", err)
		}
	}))
	defer server.Close()
	client, err := NewPluginControlClient(server.URL, "device-token")
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	state := plugins.DesiredState{
		DeviceID: "node-a", PluginID: manifest.PluginID, Revision: 3, Version: manifest.Version,
		Artifact: plugins.ArtifactReference{Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}, Enabled: true,
	}
	bundle, err := client.DownloadArtifact(context.Background(), "node-a", state, 1024)
	if err != nil {
		t.Fatalf("download plugin artifact: %v", err)
	}
	if string(bundle.Artifact) != string(artifact) || string(bundle.ManifestJSON) != string(manifestJSON) || string(bundle.Signature) != string(signature) {
		t.Fatal("downloaded package does not match server package bytes")
	}
}

func TestPluginControlClientRejectsArtifactDigestMismatchAndOversize(t *testing.T) {
	artifact := []byte("content")
	manifest := plugins.Manifest{
		PluginID: "health", Version: "1.2.0", Publisher: "publisher",
		Platform: plugins.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}, Protocol: plugins.ProtocolRange{Min: 1, Max: 1},
		Collectors: []plugins.CollectorSpec{{ID: "health"}}, ArtifactSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writer := multipart.NewWriter(w)
		w.Header().Set("Content-Type", writer.FormDataContentType())
		for name, contents := range map[string][]byte{"manifest.json": manifestJSON, "manifest.sig": make([]byte, 64), "artifact": artifact} {
			part, _ := writer.CreateFormField(name)
			_, _ = part.Write(contents)
		}
		_ = writer.Close()
	}))
	defer server.Close()
	client, err := NewPluginControlClient(server.URL, "device-token")
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	state := plugins.DesiredState{
		DeviceID: "node-a", PluginID: manifest.PluginID, Revision: 1, Version: manifest.Version,
		Artifact: plugins.ArtifactReference{Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}, Enabled: true,
	}
	if _, err := client.DownloadArtifact(context.Background(), "node-a", state, 1024); err == nil {
		t.Fatal("artifact digest mismatch was accepted")
	}
	if _, err := client.DownloadArtifact(context.Background(), "node-a", state, 1); err == nil {
		t.Fatal("artifact larger than the configured limit was accepted")
	}
}
