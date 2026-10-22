package plugins

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type reconcilerStager struct {
	packages map[string]StagedPackage
	err      error
	calls    int
}

func (s *reconcilerStager) Stage(manifestJSON, signature, artifact []byte) (StagedPackage, error) {
	s.calls++
	if s.err != nil {
		return StagedPackage{}, s.err
	}
	manifest, err := DecodeManifest(manifestJSON)
	if err != nil {
		return StagedPackage{}, err
	}
	digest := sha256.Sum256(artifact)
	if hex.EncodeToString(digest[:]) != manifest.ArtifactSHA256 || len(signature) != 64 {
		return StagedPackage{}, errors.New("invalid test package")
	}
	path := fmt.Sprintf("%s/%s/%s-%s/%s", manifest.PluginID, manifest.Version, manifest.Platform.OS, manifest.Platform.Arch, manifest.ArtifactSHA256)
	return s.packages[path], nil
}

type reconcilerDownloader struct {
	bundles map[string]PackageArtifact
	calls   int
}

func (d *reconcilerDownloader) DownloadArtifact(_ context.Context, _ string, state DesiredState, _ int64) (PackageArtifact, error) {
	d.calls++
	bundle, ok := d.bundles[state.Version]
	if !ok {
		return PackageArtifact{}, errors.New("package unavailable")
	}
	return bundle, nil
}

type reconcilerProcess struct {
	closes       int
	healthErr    error
	healthChecks int
}

func (p *reconcilerProcess) Close(context.Context) error {
	p.closes++
	return nil
}

func (p *reconcilerProcess) CheckHealth(context.Context) error {
	p.healthChecks++
	return p.healthErr
}

type reconcilerManager struct {
	processes []*reconcilerProcess
	failPath  map[string]bool
	calls     []string
}

func (m *reconcilerManager) PreparePlugin(_ context.Context, path string) (ManagedPlugin, error) {
	m.calls = append(m.calls, path)
	if m.failPath[path] {
		return nil, errors.New("test startup failure")
	}
	process := &reconcilerProcess{}
	m.processes = append(m.processes, process)
	return process, nil
}

func TestReconcilerAppliesRetriesRollsBackAndDisables(t *testing.T) {
	packageV1 := reconcilerTestPackage(t, "1.0.0", []byte("plugin-v1"))
	packageV2 := reconcilerTestPackage(t, "2.0.0", []byte("plugin-v2"))
	stager := &reconcilerStager{packages: map[string]StagedPackage{
		packageV1.RelativePath: packageV1,
		packageV2.RelativePath: packageV2,
	}}
	downloader := &reconcilerDownloader{bundles: map[string]PackageArtifact{"1.0.0": packageV1Bundle(t, packageV1), "2.0.0": packageV2Bundle(t, packageV2)}}
	manager := &reconcilerManager{failPath: make(map[string]bool)}
	reconciler, err := NewReconciler("node-a", stager, manager, downloader, 1024, testRetryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	v1 := reconcilerDesired("node-a", packageV1.Manifest, 1, true)
	reports, err := reconciler.Apply(ctx, []DesiredState{v1})
	if err != nil || len(reports) != 1 || reports[0].Lifecycle != StateRunning || reports[0].AppliedRev != 1 {
		t.Fatalf("apply v1 reports=%+v err=%v", reports, err)
	}
	if _, err := reconciler.Apply(ctx, []DesiredState{v1}); err != nil || downloader.calls != 1 || len(manager.calls) != 1 {
		t.Fatalf("identical desired retry should be idempotent; downloads=%d starts=%d err=%v", downloader.calls, len(manager.calls), err)
	}

	v2 := reconcilerDesired("node-a", packageV2.Manifest, 2, true)
	manager.failPath[packageV2.RelativePath] = true
	reports, err = reconciler.Apply(ctx, []DesiredState{v2})
	if err == nil || len(reports) != 1 || reports[0].Lifecycle != StateRunning || reports[0].AppliedRev != 1 || reports[0].RefusalReason == "" {
		t.Fatalf("failed upgrade should restore v1; reports=%+v err=%v", reports, err)
	}
	if manager.calls[len(manager.calls)-1] != packageV1.RelativePath {
		t.Fatalf("rollback target = %q, want %q", manager.calls[len(manager.calls)-1], packageV1.RelativePath)
	}
	manager.failPath[packageV2.RelativePath] = false
	reports, err = reconciler.Apply(ctx, []DesiredState{v2})
	if err != nil || reports[0].AppliedRev != 2 || reports[0].Lifecycle != StateRunning {
		t.Fatalf("retry v2 reports=%+v err=%v", reports, err)
	}
	if manager.processes[0].closes != 1 {
		t.Fatalf("old package close count = %d", manager.processes[0].closes)
	}

	disabled := DesiredState{DeviceID: "node-a", PluginID: "health", Revision: 3, Enabled: false}
	reports, err = reconciler.Apply(ctx, []DesiredState{disabled})
	if err != nil || reports[0].Lifecycle != StateStopped || reports[0].AppliedRev != 3 {
		t.Fatalf("disable reports=%+v err=%v", reports, err)
	}
	if manager.processes[len(manager.processes)-1].closes != 1 {
		t.Fatal("disabled plugin process was not closed")
	}
}

func TestReconcilerRejectsStaleDuplicateAndCrossDeviceBatches(t *testing.T) {
	packageV1 := reconcilerTestPackage(t, "1.0.0", []byte("plugin-v1"))
	stager := &reconcilerStager{packages: map[string]StagedPackage{packageV1.RelativePath: packageV1}}
	downloader := &reconcilerDownloader{bundles: map[string]PackageArtifact{"1.0.0": packageV1Bundle(t, packageV1)}}
	reconciler, err := NewReconciler("node-a", stager, &reconcilerManager{failPath: make(map[string]bool)}, downloader, 1024, testRetryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	state := reconcilerDesired("node-a", packageV1.Manifest, 2, true)
	if _, err := reconciler.Apply(ctx, []DesiredState{state}); err != nil {
		t.Fatal(err)
	}
	if _, err := reconciler.Apply(ctx, []DesiredState{state, state}); err == nil {
		t.Fatal("duplicate plugin state was accepted")
	}
	stale := state
	stale.Revision = 1
	if _, err := reconciler.Apply(ctx, []DesiredState{stale}); err == nil {
		t.Fatal("stale desired revision was accepted")
	}
	crossDevice := state
	crossDevice.DeviceID = "node-b"
	crossDevice.Revision = 3
	if _, err := reconciler.Apply(ctx, []DesiredState{crossDevice}); err == nil {
		t.Fatal("cross-device desired state was accepted")
	}
}

func TestNewReconcilerRequiresTrimmedDeviceIdentity(t *testing.T) {
	packageV1 := reconcilerTestPackage(t, "1.0.0", []byte("plugin-v1"))
	stager := &reconcilerStager{packages: map[string]StagedPackage{packageV1.RelativePath: packageV1}}
	downloader := &reconcilerDownloader{bundles: map[string]PackageArtifact{"1.0.0": packageV1Bundle(t, packageV1)}}
	if _, err := NewReconciler(" node-a ", stager, &reconcilerManager{failPath: make(map[string]bool)}, downloader, 1024, testRetryPolicy()); err == nil {
		t.Fatal("reconciler accepted a device identity with surrounding whitespace")
	}
}

func TestReconcilerRejectsWrongArtifactBeforeStaging(t *testing.T) {
	packageV1 := reconcilerTestPackage(t, "1.0.0", []byte("plugin-v1"))
	packageV2 := reconcilerTestPackage(t, "2.0.0", []byte("plugin-v2"))
	stager := &reconcilerStager{packages: map[string]StagedPackage{packageV1.RelativePath: packageV1}}
	manager := &reconcilerManager{failPath: make(map[string]bool)}
	downloader := &reconcilerDownloader{bundles: map[string]PackageArtifact{"1.0.0": packageV2Bundle(t, packageV2)}}
	reconciler, err := NewReconciler("node-a", stager, manager, downloader, 1024, testRetryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reports, err := reconciler.Apply(ctx, []DesiredState{reconcilerDesired("node-a", packageV1.Manifest, 1, true)})
	if err == nil || len(reports) != 1 || reports[0].Lifecycle != StateFailed {
		t.Fatalf("mismatched artifact reports=%+v err=%v", reports, err)
	}
	if stager.calls != 0 || len(manager.calls) != 0 {
		t.Fatalf("mismatched artifact reached stage/start: stage=%d starts=%d", stager.calls, len(manager.calls))
	}
}

func TestReconcilerRestartsActivePluginAfterHealthFailure(t *testing.T) {
	packageV1 := reconcilerTestPackage(t, "1.0.0", []byte("plugin-v1"))
	stager := &reconcilerStager{packages: map[string]StagedPackage{packageV1.RelativePath: packageV1}}
	downloader := &reconcilerDownloader{bundles: map[string]PackageArtifact{"1.0.0": packageV1Bundle(t, packageV1)}}
	manager := &reconcilerManager{failPath: make(map[string]bool)}
	reconciler, err := NewReconciler("node-a", stager, manager, downloader, 1024, testRetryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	desired := reconcilerDesired("node-a", packageV1.Manifest, 1, true)
	if reports, err := reconciler.Apply(ctx, []DesiredState{desired}); err != nil || reports[0].Lifecycle != StateRunning {
		t.Fatalf("initial apply reports=%+v err=%v", reports, err)
	}
	manager.processes[0].healthErr = errors.New("plugin process exited")
	reports, err := reconciler.Apply(ctx, []DesiredState{desired})
	if err == nil || reports[0].Lifecycle != StateRunning || !strings.Contains(reports[0].RefusalReason, "restarted") {
		t.Fatalf("health recovery reports=%+v err=%v", reports, err)
	}
	if manager.processes[0].closes != 1 || len(manager.processes) != 2 || manager.calls[0] != manager.calls[1] {
		t.Fatalf("restart lifecycle: closes=%d starts=%v", manager.processes[0].closes, manager.calls)
	}
}

func TestReconcilerQuarantinesRepeatedHealthFailuresAfterBoundedRestarts(t *testing.T) {
	packageV1 := reconcilerTestPackage(t, "1.0.0", []byte("plugin-v1"))
	stager := &reconcilerStager{packages: map[string]StagedPackage{packageV1.RelativePath: packageV1}}
	downloader := &reconcilerDownloader{bundles: map[string]PackageArtifact{"1.0.0": packageV1Bundle(t, packageV1)}}
	manager := &reconcilerManager{failPath: make(map[string]bool)}
	now := time.Date(2026, 10, 18, 3, 0, 0, 0, time.UTC)
	policy := RetryPolicy{MaxConsecutiveFailures: 2, InitialBackoff: time.Second, MaxBackoff: 2 * time.Second, Now: func() time.Time { return now }}
	reconciler, err := NewReconciler("node-a", stager, manager, downloader, 1024, policy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	desired := reconcilerDesired("node-a", packageV1.Manifest, 1, true)
	if reports, err := reconciler.Apply(ctx, []DesiredState{desired}); err != nil || reports[0].Lifecycle != StateRunning {
		t.Fatalf("initial apply reports=%+v err=%v", reports, err)
	}
	manager.processes[0].healthErr = errors.New("plugin exited")
	if reports, err := reconciler.Apply(ctx, []DesiredState{desired}); err == nil || reports[0].Lifecycle != StateRunning {
		t.Fatalf("first recovered health failure reports=%+v err=%v", reports, err)
	}
	if reports, err := reconciler.Apply(ctx, []DesiredState{desired}); err != nil || reports[0].Lifecycle != StateRunning || len(manager.processes) != 2 {
		t.Fatalf("restart was not deferred during backoff: reports=%+v processes=%d err=%v", reports, len(manager.processes), err)
	}
	now = now.Add(time.Second)
	manager.processes[1].healthErr = errors.New("plugin exited again")
	reports, err := reconciler.Apply(ctx, []DesiredState{desired})
	if err == nil || reports[0].Lifecycle != StateQuarantined || reports[0].AppliedRev != 0 {
		t.Fatalf("repeated health failure reports=%+v err=%v", reports, err)
	}
	if len(manager.processes) != 3 || manager.processes[2].closes != 1 {
		t.Fatalf("quarantine did not stop latest restarted process: processes=%d closes=%d", len(manager.processes), manager.processes[2].closes)
	}
}

func TestReconcilerRollsBackToPreviousVersionAfterHealthFailure(t *testing.T) {
	packageV1 := reconcilerTestPackage(t, "1.0.0", []byte("plugin-v1"))
	packageV2 := reconcilerTestPackage(t, "2.0.0", []byte("plugin-v2"))
	stager := &reconcilerStager{packages: map[string]StagedPackage{
		packageV1.RelativePath: packageV1,
		packageV2.RelativePath: packageV2,
	}}
	downloader := &reconcilerDownloader{bundles: map[string]PackageArtifact{
		"1.0.0": packageV1Bundle(t, packageV1),
		"2.0.0": packageV2Bundle(t, packageV2),
	}}
	manager := &reconcilerManager{failPath: make(map[string]bool)}
	policy := RetryPolicy{MaxConsecutiveFailures: 3, InitialBackoff: time.Hour, MaxBackoff: time.Hour, Now: time.Now}
	reconciler, err := NewReconciler("node-a", stager, manager, downloader, 1024, policy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	v1 := reconcilerDesired("node-a", packageV1.Manifest, 1, true)
	v2 := reconcilerDesired("node-a", packageV2.Manifest, 2, true)
	if reports, err := reconciler.Apply(ctx, []DesiredState{v1}); err != nil || reports[0].Lifecycle != StateRunning {
		t.Fatalf("apply v1 reports=%+v err=%v", reports, err)
	}
	if reports, err := reconciler.Apply(ctx, []DesiredState{v2}); err != nil || reports[0].AppliedRev != 2 {
		t.Fatalf("apply v2 reports=%+v err=%v", reports, err)
	}
	manager.processes[1].healthErr = errors.New("v2 exited")
	reports, err := reconciler.Apply(ctx, []DesiredState{v2})
	if err == nil || reports[0].Lifecycle != StateRunning || reports[0].AppliedRev != 1 || !strings.Contains(reports[0].RefusalReason, "rolled back") {
		t.Fatalf("health failure did not restore v1: reports=%+v err=%v", reports, err)
	}
	if manager.processes[1].closes != 1 || manager.calls[2] != packageV1.RelativePath {
		t.Fatalf("rollback lifecycle: v2 closes=%d starts=%v", manager.processes[1].closes, manager.calls)
	}
}

func TestReconcilerBacksOffAndQuarantinesRepeatedStartupFailures(t *testing.T) {
	packageV1 := reconcilerTestPackage(t, "1.0.0", []byte("plugin-v1"))
	stager := &reconcilerStager{packages: map[string]StagedPackage{packageV1.RelativePath: packageV1}}
	downloader := &reconcilerDownloader{bundles: map[string]PackageArtifact{"1.0.0": packageV1Bundle(t, packageV1)}}
	manager := &reconcilerManager{failPath: map[string]bool{packageV1.RelativePath: true}}
	now := time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC)
	policy := RetryPolicy{MaxConsecutiveFailures: 2, InitialBackoff: time.Second, MaxBackoff: 4 * time.Second, Now: func() time.Time { return now }}
	reconciler, err := NewReconciler("node-a", stager, manager, downloader, 1024, policy)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	desired := reconcilerDesired("node-a", packageV1.Manifest, 1, true)
	reports, err := reconciler.Apply(ctx, []DesiredState{desired})
	if err == nil || reports[0].Lifecycle != StateFailed {
		t.Fatalf("first failure reports=%+v err=%v", reports, err)
	}
	if reports, err = reconciler.Apply(ctx, []DesiredState{desired}); err != nil || reports[0].Lifecycle != StateFailed || len(manager.calls) != 1 {
		t.Fatalf("retry before backoff reports=%+v starts=%d err=%v", reports, len(manager.calls), err)
	}
	now = now.Add(time.Second)
	reports, err = reconciler.Apply(ctx, []DesiredState{desired})
	if err == nil || reports[0].Lifecycle != StateQuarantined {
		t.Fatalf("threshold failure reports=%+v err=%v", reports, err)
	}
	if reports, err = reconciler.Apply(ctx, []DesiredState{desired}); err != nil || reports[0].Lifecycle != StateQuarantined || len(manager.calls) != 2 {
		t.Fatalf("quarantined state retried: reports=%+v starts=%d err=%v", reports, len(manager.calls), err)
	}
	manager.failPath[packageV1.RelativePath] = false
	desired.Revision++
	reports, err = reconciler.Apply(ctx, []DesiredState{desired})
	if err != nil || reports[0].Lifecycle != StateRunning || reports[0].AppliedRev != desired.Revision {
		t.Fatalf("new desired revision did not clear quarantine: reports=%+v err=%v", reports, err)
	}
}

func TestRetryPolicyRequiresExplicitBoundsAndCapsExponentialDelay(t *testing.T) {
	valid := RetryPolicy{MaxConsecutiveFailures: 4, InitialBackoff: time.Second, MaxBackoff: 5 * time.Second, Now: time.Now}
	invalid := []RetryPolicy{
		{},
		{MaxConsecutiveFailures: 2, InitialBackoff: time.Second, MaxBackoff: time.Second},
		{MaxConsecutiveFailures: 2, InitialBackoff: 2 * time.Second, MaxBackoff: time.Second, Now: time.Now},
		{MaxConsecutiveFailures: 2, InitialBackoff: time.Second, MaxBackoff: 2 * time.Second},
	}
	for index, policy := range invalid {
		if err := policy.validate(); err == nil {
			t.Errorf("invalid retry policy %d was accepted", index)
		}
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid retry policy rejected: %v", err)
	}
	for _, test := range []struct {
		failures uint32
		want     time.Duration
	}{
		{1, time.Second}, {2, 2 * time.Second}, {3, 4 * time.Second}, {4, 5 * time.Second},
	} {
		if got := retryBackoff(valid, test.failures); got != test.want {
			t.Errorf("retryBackoff(%d)=%s, want %s", test.failures, got, test.want)
		}
	}
}

func reconcilerTestPackage(t *testing.T, version string, artifact []byte) StagedPackage {
	t.Helper()
	digest := sha256.Sum256(artifact)
	manifest := Manifest{
		PluginID: "health", Version: version, Publisher: "test.publisher",
		Platform: Platform{OS: "linux", Arch: "arm64"}, Protocol: ProtocolRange{Min: 1, Max: 1},
		Collectors: []CollectorSpec{{ID: "health"}}, ArtifactSHA256: hex.EncodeToString(digest[:]),
	}
	path := fmt.Sprintf("health/%s/linux-arm64/%s", version, manifest.ArtifactSHA256)
	return StagedPackage{Manifest: manifest, RelativePath: path, ArtifactPath: "/private/stage/" + path + "/artifact"}
}

func packageV1Bundle(t *testing.T, staged StagedPackage) PackageArtifact {
	return reconcilerBundle(t, staged)
}

func packageV2Bundle(t *testing.T, staged StagedPackage) PackageArtifact {
	return reconcilerBundle(t, staged)
}

func reconcilerBundle(t *testing.T, staged StagedPackage) PackageArtifact {
	t.Helper()
	artifact := []byte("plugin-v2")
	if staged.Manifest.Version == "1.0.0" {
		artifact = []byte("plugin-v1")
	} else {
		artifact = []byte("plugin-v2")
	}
	manifestJSON, err := json.Marshal(staged.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	return PackageArtifact{ManifestJSON: manifestJSON, Signature: make([]byte, 64), Artifact: artifact}
}

func reconcilerDesired(deviceID string, manifest Manifest, revision uint64, enabled bool) DesiredState {
	return DesiredState{
		DeviceID: deviceID, PluginID: manifest.PluginID, Revision: revision, Version: manifest.Version,
		Artifact: ArtifactReference{Platform: manifest.Platform, SHA256: manifest.ArtifactSHA256}, Enabled: enabled,
	}
}

func testRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxConsecutiveFailures: 5, InitialBackoff: time.Nanosecond, MaxBackoff: time.Millisecond, Now: time.Now}
}
