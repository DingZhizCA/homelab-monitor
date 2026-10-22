package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

func TestNewPluginRuntimeRequiresAndAssemblesExplicitTrustInputs(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stagingDirectory := filepath.Join(root, "staging")
	runtimeDirectory := filepath.Join(root, "runtime")
	stateDirectory := filepath.Join(root, "state")
	for _, directory := range []string{stagingDirectory, runtimeDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "publisher.pub")
	if err := os.WriteFile(keyPath, publicKey, 0o600); err != nil {
		t.Fatal(err)
	}
	permissionPath := filepath.Join(root, "grants.json")
	if err := os.WriteFile(permissionPath, []byte(`{"grants":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	config := PluginRuntimeConfig{
		ServerURL: "https://server.example", DeviceID: "node-a", DeviceToken: "device-token",
		PublisherKeyPath: keyPath, PermissionFile: permissionPath,
		StagingDirectory: stagingDirectory, RuntimeDirectory: runtimeDirectory, StateDirectory: stateDirectory,
		HostProtocolMajor: 1, MaxArtifactBytes: 1 << 20, OperationTimeout: 30 * time.Second,
		MaxStateEntries: 16, MaxStateBytes: 1 << 20,
		MaxConsecutiveFailures: 5, InitialFailureBackoff: time.Second, MaxFailureBackoff: 5 * time.Minute,
	}
	runtime, err := NewPluginRuntime(config)
	if err != nil {
		t.Fatalf("assemble plugin runtime: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Close(ctx); err != nil {
		t.Fatalf("close plugin runtime: %v", err)
	}
	if runtime.control.client.Timeout != config.OperationTimeout {
		t.Fatalf("plugin HTTP timeout=%s, want configured operation timeout %s", runtime.control.client.Timeout, config.OperationTimeout)
	}
	if err := runtime.ReconcileOnce(ctx); err == nil {
		t.Fatal("closed plugin runtime accepted a reconciliation request")
	}
}

func TestNewPluginRuntimeRejectsIncompleteConfiguration(t *testing.T) {
	if _, err := NewPluginRuntime(PluginRuntimeConfig{ServerURL: "https://server.example", DeviceToken: "token", HostProtocolMajor: 1, MaxArtifactBytes: 1024}); err == nil {
		t.Fatal("runtime without a device identity was accepted")
	}
	if _, err := NewPluginRuntime(PluginRuntimeConfig{ServerURL: "http://server.example", DeviceID: "node-a", DeviceToken: "token", HostProtocolMajor: 1, MaxArtifactBytes: 1024}); err == nil {
		t.Fatal("runtime with an insecure control URL was accepted")
	}
}

func TestPluginRuntimeCloseCancelsInFlightReconciliation(t *testing.T) {
	artifactStarted := make(chan struct{}, 1)
	artifactCanceled := make(chan struct{}, 1)
	desired := plugins.DesiredState{
		DeviceID: "node-a", PluginID: "health", Revision: 1, Version: "1.0.0", Enabled: true,
		Artifact: plugins.ArtifactReference{
			Platform: plugins.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH},
			SHA256:   strings.Repeat("a", 64),
		},
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer device-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/api/v1/agent/plugins/desired":
			_ = json.NewEncoder(w).Encode(struct {
				DeviceID string                 `json:"device_id"`
				States   []plugins.DesiredState `json:"states"`
			}{"node-a", []plugins.DesiredState{desired}})
		case "/api/v1/agent/plugins/artifact":
			artifactStarted <- struct{}{}
			<-r.Context().Done()
			artifactCanceled <- struct{}{}
		case "/api/v1/agent/plugins/reported":
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	stagingDirectory := filepath.Join(root, "staging")
	runtimeDirectory := filepath.Join(root, "runtime")
	stateDirectory := filepath.Join(root, "state")
	for _, directory := range []string{stagingDirectory, runtimeDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "publisher.pub")
	if err := os.WriteFile(keyPath, publicKey, 0o600); err != nil {
		t.Fatal(err)
	}
	permissionPath := filepath.Join(root, "grants.json")
	if err := os.WriteFile(permissionPath, []byte(`{"grants":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	pluginRuntime, err := NewPluginRuntime(PluginRuntimeConfig{
		ServerURL: server.URL, DeviceID: "node-a", DeviceToken: "device-token",
		PublisherKeyPath: keyPath, PermissionFile: permissionPath,
		StagingDirectory: stagingDirectory, RuntimeDirectory: runtimeDirectory, StateDirectory: stateDirectory,
		HostProtocolMajor: 1, MaxArtifactBytes: 1 << 20, OperationTimeout: 30 * time.Second,
		MaxStateEntries: 16, MaxStateBytes: 1 << 20,
		MaxConsecutiveFailures: 3, InitialFailureBackoff: time.Second, MaxFailureBackoff: time.Minute,
	})
	if err != nil {
		t.Fatalf("assemble plugin runtime: %v", err)
	}
	client := server.Client()
	client.CheckRedirect = pluginRuntime.control.client.CheckRedirect
	pluginRuntime.control.client = client

	reconcileCtx, cancelReconcile := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelReconcile()
	reconcileResult := make(chan error, 1)
	go func() { reconcileResult <- pluginRuntime.ReconcileOnce(reconcileCtx) }()
	select {
	case <-artifactStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("reconciliation did not begin the blocked artifact request")
	}

	closeCtx, cancelClose := context.WithTimeout(context.Background(), time.Second)
	defer cancelClose()
	if err := pluginRuntime.Close(closeCtx); err != nil {
		t.Fatalf("close should cancel in-flight reconciliation: %v", err)
	}
	select {
	case <-artifactCanceled:
	case <-time.After(time.Second):
		t.Fatal("artifact request was not canceled during close")
	}
	select {
	case err := <-reconcileResult:
		if err == nil {
			t.Fatal("canceled reconciliation unexpectedly succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not finish after cancellation")
	}
}
