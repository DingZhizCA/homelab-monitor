/*
Package agent verifies the plugin control client against the Server HTTP handlers.
*/
package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
	serverpkg "github.com/DingZhizCA/homelab-monitor/internal/server"
)

func TestPluginControlClientAgainstServerHandlers(t *testing.T) {
	registry := plugins.NewRegistry()
	if err := registry.SetDesired(plugins.DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 7, Version: "2.0.0", Artifact: plugins.ArtifactReference{Platform: plugins.Platform{OS: "linux", Arch: "arm64"}, SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	credentials := serverpkg.NewDeviceCredentials()
	if err := credentials.Set("node-a", "device-token"); err != nil {
		t.Fatal(err)
	}
	control, err := serverpkg.NewPluginControlHandler(registry, credentials)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/agent/plugins/desired", control.Desired)
	mux.HandleFunc("/api/v1/agent/plugins/reported", control.Reported)
	server := httptest.NewTLSServer(mux)
	defer server.Close()

	client, err := NewPluginControlClient(server.URL, "device-token")
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	desired, err := client.Desired(context.Background(), "node-a")
	if err != nil || len(desired) != 1 || desired[0].Revision != 7 {
		t.Fatalf("desired = %+v, %v", desired, err)
	}
	reported := plugins.ReportedState{DeviceID: "node-a", PluginID: "health", ObservedRev: 7, AppliedRev: 7, Lifecycle: plugins.StateRunning}
	if err := client.Reported(context.Background(), "node-a", []plugins.ReportedState{reported}); err != nil {
		t.Fatal(err)
	}
	if got, ok := registry.Reported("node-a", "health"); !ok || got != reported {
		t.Fatalf("reported registry state = %+v, %v", got, ok)
	}
}

func TestAgentDownloadsServerDesiredPackageAndVerifiesPublisherSignature(t *testing.T) {
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("integration plugin bytes")
	digest := sha256.Sum256(artifact)
	manifest := plugins.Manifest{
		PluginID: "health", Version: "2.0.0", Publisher: "integration.test",
		Platform:       plugins.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
		Protocol:       plugins.ProtocolRange{Min: 1, Max: 1},
		Collectors:     []plugins.CollectorSpec{{ID: "health"}},
		ArtifactSHA256: hex.EncodeToString(digest[:]),
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	bundle := plugins.PackageArtifact{
		ManifestJSON: manifestJSON,
		Signature:    ed25519.Sign(privateKey, manifestJSON),
		Artifact:     artifact,
	}
	artifactRoot := t.TempDir()
	if err := os.Chmod(artifactRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	repository, err := plugins.NewFileArtifactRepository(artifactRoot, plugins.ArtifactRepositoryLimits{MaxArtifactBytes: 1 << 20, MaxRepositoryBytes: 16 << 20, MaxPackages: 16, MaxScanEntries: 256})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	key := plugins.ArtifactKey{PluginID: manifest.PluginID, Version: manifest.Version, Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}
	if err := repository.Put(key, bundle); err != nil {
		t.Fatalf("store signed test package: %v", err)
	}
	registry := plugins.NewRegistry()
	desiredState := plugins.DesiredState{
		DeviceID: "node-a", PluginID: manifest.PluginID, Revision: 1, Version: manifest.Version,
		Artifact: plugins.ArtifactReference{Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}, Enabled: true,
	}
	if err := registry.SetDesired(desiredState); err != nil {
		t.Fatal(err)
	}
	credentials := serverpkg.NewDeviceCredentials()
	if err := credentials.Set("node-a", "device-token"); err != nil {
		t.Fatal(err)
	}
	control, err := serverpkg.NewPluginControlHandler(registry, credentials)
	if err != nil {
		t.Fatal(err)
	}
	artifactHandler, err := serverpkg.NewPluginArtifactHandler(registry, repository, credentials)
	if err != nil {
		t.Fatal(err)
	}
	router, err := serverpkg.NewRouter(serverpkg.RouterOptions{
		API:           serverpkg.NewHandler(serverpkg.NewStore(), serverpkg.NewBroker()),
		PluginControl: control, PluginArtifacts: artifactHandler,
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(router)
	defer server.Close()
	client, err := NewPluginControlClient(server.URL, "device-token")
	if err != nil {
		t.Fatal(err)
	}
	client.client = server.Client()
	desired, err := client.Desired(context.Background(), "node-a")
	if err != nil || len(desired) != 1 || desired[0] != desiredState {
		t.Fatalf("desired state = %+v, %v", desired, err)
	}
	downloaded, err := client.DownloadArtifact(context.Background(), "node-a", desired[0], 1<<20)
	if err != nil {
		t.Fatalf("download artifact: %v", err)
	}
	if _, err := plugins.VerifyPackage(publicKey, downloaded.ManifestJSON, downloaded.Signature, downloaded.Artifact); err != nil {
		t.Fatalf("locally verify downloaded package: %v", err)
	}
}
