/*
Package plugins tests verified startup, manifest-to-host compatibility, and cleanup.
*/
package plugins

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
	"google.golang.org/grpc"
)

type managerProcess struct {
	socketPath string
	stopped    bool
	stopErr    error
}

func (p *managerProcess) SocketPath() string { return p.socketPath }

func (p *managerProcess) Stop(context.Context) error {
	p.stopped = true
	return p.stopErr
}

type managerSupervisor struct {
	process *managerProcess
	spec    ProcessSpec
	err     error
}

type managerPermissionPolicy func(Manifest) error

func (p managerPermissionPolicy) AuthorizePlugin(manifest Manifest) error {
	return p(manifest)
}

func allowManagerTestPlugin(Manifest) error { return nil }

func (s *managerSupervisor) Start(_ context.Context, spec ProcessSpec) (Process, error) {
	s.spec = spec
	if s.process == nil {
		return nil, s.err
	}
	return s.process, s.err
}

func stageManagerFixture(t *testing.T, collectorID string, protocol ProtocolRange) (*Stager, StagedPackage) {
	return stageManagerFixtureWithPermissions(t, collectorID, protocol, nil)
}

func stageManagerFixtureWithPermissions(t *testing.T, collectorID string, protocol ProtocolRange, permissions []string) (*Stager, StagedPackage) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	artifact := []byte("signed test plugin")
	digest := sha256.Sum256(artifact)
	manifest := validManifest()
	manifest.PluginID = "network"
	manifest.Version = "1.0.0"
	manifest.Platform = Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
	manifest.Protocol = protocol
	manifest.Collectors = []CollectorSpec{{ID: collectorID}}
	manifest.Permissions = append([]string(nil), permissions...)
	manifest.ArtifactSHA256 = hex.EncodeToString(digest[:])
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	signature := ed25519.Sign(privateKey, manifestJSON)
	rootPath := filepath.Join(testutil.TempDir(t), "staging")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	stager, err := NewStager(rootPath, publicKey, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stager.Close() })
	staged, err := stager.Stage(manifestJSON, signature, artifact)
	if err != nil {
		t.Fatal(err)
	}
	return stager, staged
}

func managerSocket(t *testing.T) string {
	t.Helper()
	socketPath := filepath.Join(testutil.TempDir(t), "plugin.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	if err := RegisterGRPCPlugin(server, testPlugin{minHostMajor: 1, maxHostMajor: 1}); err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	return socketPath
}

func TestPluginManagerPreparesAndClosesVerifiedRuntime(t *testing.T) {
	stager, staged := stageManagerFixture(t, "network.flow", ProtocolRange{Min: 1, Max: 1})
	process := &managerProcess{socketPath: managerSocket(t)}
	supervisor := &managerSupervisor{process: process}
	manager, err := NewPluginManager(stager, supervisor, 1, managerPermissionPolicy(allowManagerTestPlugin))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := manager.Prepare(context.Background(), staged.RelativePath); err == nil {
		t.Fatal("plugin startup without a deadline was accepted")
	}
	prepared, err := manager.Prepare(ctx, staged.RelativePath)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Package.Manifest.PluginID != "network" || len(prepared.Collectors) != 1 {
		t.Fatalf("prepared package = %+v", prepared)
	}
	if supervisor.spec.ArtifactPath != staged.ArtifactPath || supervisor.spec.Manifest.Version != "1.0.0" {
		t.Fatalf("supervisor spec = %+v", supervisor.spec)
	}
	if err := prepared.ValidateConfig(ctx, "network.flow", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	result, err := prepared.Collect(ctx, CollectRequest{CollectorID: "network.flow", Config: json.RawMessage(`{}`)})
	if err != nil || len(result.Samples) != 1 {
		t.Fatalf("Collect() = %+v, %v", result, err)
	}
	health, err := prepared.Health(ctx)
	if err != nil || !health.Healthy {
		t.Fatalf("Health() = %+v, %v", health, err)
	}
	if err := prepared.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !process.stopped {
		t.Fatal("prepared process was not stopped")
	}
	if err := prepared.Close(ctx); err != nil {
		t.Fatalf("repeated close should be safe: %v", err)
	}
	if _, err := prepared.Collect(ctx, CollectRequest{CollectorID: "network.flow", Config: json.RawMessage(`{}`)}); err == nil {
		t.Fatal("collection succeeded after close")
	}
}

func TestPluginManagerStopsProcessWhenManifestContractDoesNotMatch(t *testing.T) {
	stager, staged := stageManagerFixture(t, "disk.health", ProtocolRange{Min: 1, Max: 1})
	process := &managerProcess{socketPath: managerSocket(t)}
	manager, err := NewPluginManager(stager, &managerSupervisor{process: process}, 1, managerPermissionPolicy(allowManagerTestPlugin))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := manager.Prepare(ctx, staged.RelativePath); err == nil {
		t.Fatal("runtime with undeclared collector was accepted")
	}
	if !process.stopped {
		t.Fatal("rejected plugin process was not stopped")
	}
}

func TestPluginManagerRejectsIncompatibleManifestBeforeStart(t *testing.T) {
	stager, staged := stageManagerFixture(t, "network.flow", ProtocolRange{Min: 2, Max: 2})
	supervisor := &managerSupervisor{process: &managerProcess{socketPath: managerSocket(t)}}
	manager, err := NewPluginManager(stager, supervisor, 1, managerPermissionPolicy(allowManagerTestPlugin))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := manager.Prepare(ctx, staged.RelativePath); err == nil {
		t.Fatal("incompatible signed manifest was accepted")
	}
	if supervisor.spec.Manifest.PluginID != "" {
		t.Fatal("supervisor was called before manifest compatibility validation")
	}
}

func TestPluginManagerRequiresLocalAuthorizationForRequestedPermissions(t *testing.T) {
	stager, staged := stageManagerFixtureWithPermissions(t, "network.flow", ProtocolRange{Min: 1, Max: 1}, []string{"read:proc"})
	supervisor := &managerSupervisor{process: &managerProcess{socketPath: managerSocket(t)}}
	if _, err := NewPluginManager(stager, supervisor, 1, nil); err == nil {
		t.Fatal("plugin manager accepted a missing local authorization policy")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if supervisor.spec.Manifest.PluginID != "" {
		t.Fatal("supervisor was called before plugin permission authorization")
	}

	policyCalled := false
	permissionDenied := errors.New("permission not granted")
	manager, err := NewPluginManager(stager, supervisor, 1, managerPermissionPolicy(func(manifest Manifest) error {
		policyCalled = true
		if len(manifest.Permissions) != 1 || manifest.Permissions[0] != "read:proc" {
			t.Fatalf("permission policy received manifest permissions: %+v", manifest.Permissions)
		}
		return permissionDenied
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Prepare(ctx, staged.RelativePath); !errors.Is(err, permissionDenied) {
		t.Fatalf("denied plugin permission error = %v", err)
	}
	if !policyCalled || supervisor.spec.Manifest.PluginID != "" {
		t.Fatal("permission denial was not enforced before process start")
	}

	noPermissionStager, noPermissionPackage := stageManagerFixture(t, "network.flow", ProtocolRange{Min: 1, Max: 1})
	packageDenied, err := NewPluginManager(noPermissionStager, supervisor, 1, managerPermissionPolicy(func(Manifest) error {
		return permissionDenied
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := packageDenied.Prepare(ctx, noPermissionPackage.RelativePath); !errors.Is(err, permissionDenied) {
		t.Fatalf("package without declared capabilities bypassed local authorization: %v", err)
	}
	if supervisor.spec.Manifest.PluginID != "" {
		t.Fatal("package authorization denial was not enforced before process start")
	}

	manager, err = NewPluginManager(stager, supervisor, 1, managerPermissionPolicy(func(manifest Manifest) error {
		manifest.Permissions[0] = "local:mutated"
		manifest.Collectors[0].ID = "mutated"
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := manager.Prepare(ctx, staged.RelativePath)
	if err != nil {
		t.Fatalf("allowed permission policy failed: %v", err)
	}
	defer prepared.Close(ctx)
	if got := supervisor.spec.Manifest.Permissions[0]; got != "read:proc" {
		t.Fatalf("permission policy mutated the signed manifest: %q", got)
	}
	if got := supervisor.spec.Manifest.Collectors[0].ID; got != "network.flow" {
		t.Fatalf("permission policy mutated the signed collector list: %q", got)
	}
}

func TestPluginManagerReportsStartupFailureAndStopsProcess(t *testing.T) {
	stager, staged := stageManagerFixture(t, "network.flow", ProtocolRange{Min: 1, Max: 1})
	process := &managerProcess{socketPath: "relative.sock"}
	manager, err := NewPluginManager(stager, &managerSupervisor{process: process}, 1, managerPermissionPolicy(allowManagerTestPlugin))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = manager.Prepare(ctx, staged.RelativePath)
	if err == nil || !process.stopped {
		t.Fatalf("Prepare() error=%v, process stopped=%v", err, process.stopped)
	}

	startFailure := errors.New("process creation failed")
	manager, err = NewPluginManager(stager, &managerSupervisor{err: startFailure}, 1, managerPermissionPolicy(allowManagerTestPlugin))
	if err != nil {
		t.Fatal(err)
	}
	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Second)
	defer cancelStart()
	if _, err := manager.Prepare(startCtx, staged.RelativePath); !errors.Is(err, startFailure) {
		t.Fatalf("start failure = %v", err)
	}
}

func TestPreparedPluginCloseDeadlineCancelsInFlightRPC(t *testing.T) {
	collectStart := make(chan struct{}, 1)
	host, err := NewGRPCHost(startTestPlugin(t, testPlugin{
		minHostMajor: 1, maxHostMajor: 1, blockCollect: true, collectStart: collectStart,
	}))
	if err != nil {
		t.Fatal(err)
	}
	prepareCtx, cancelPrepare := context.WithTimeout(context.Background(), time.Second)
	defer cancelPrepare()
	identity := HostIdentity{PluginID: "network", Version: "1.0.0", ProtocolMajor: 1}
	if _, err := host.Handshake(prepareCtx, identity); err != nil {
		t.Fatal(err)
	}
	collectors, err := host.Describe(prepareCtx)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &PreparedPlugin{
		Package:    StagedPackage{Manifest: Manifest{PluginID: "network"}},
		Collectors: collectors,
		process:    &managerProcess{},
		host:       host,
	}
	collectCtx, cancelCollect := context.WithTimeout(context.Background(), time.Minute)
	defer cancelCollect()
	collectResult := make(chan error, 1)
	go func() {
		_, err := prepared.Collect(collectCtx, CollectRequest{CollectorID: "network.flow", Config: json.RawMessage(`{}`)})
		collectResult <- err
	}()
	select {
	case <-collectStart:
	case <-time.After(time.Second):
		t.Fatal("blocking collector did not start")
	}

	closeCtx, cancelClose := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancelClose()
	closed := make(chan error, 1)
	go func() { closed <- prepared.Close(closeCtx) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("Close() with active collection = %v", err)
		}
	case <-time.After(time.Second):
		cancelCollect()
		<-closed
		t.Fatal("Close() waited for the collection's longer deadline")
	}
	select {
	case err := <-collectResult:
		if err == nil {
			t.Fatal("in-flight collection succeeded after Host shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight collection was not canceled by Host shutdown")
	}
}

func TestPreparedPluginCloseCanRetryAfterProcessStopFailure(t *testing.T) {
	stopFailure := errors.New("process is still terminating")
	process := &managerProcess{stopErr: stopFailure}
	prepared := &PreparedPlugin{process: process}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := prepared.Close(ctx); !errors.Is(err, stopFailure) {
		t.Fatalf("first Close() error = %v", err)
	}
	if !process.stopped {
		t.Fatal("first Close() did not request process termination")
	}
	process.stopErr = nil
	if err := prepared.Close(ctx); err != nil {
		t.Fatalf("second Close() did not retry process termination: %v", err)
	}
	if err := prepared.Close(ctx); err != nil {
		t.Fatalf("Close() after successful stop = %v", err)
	}
}
