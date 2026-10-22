package plugins

import "testing"

func validManifest() Manifest {
	return Manifest{
		PluginID: "storage.health", Version: "1.2.3", Publisher: "homelab",
		Platform: Platform{OS: "linux", Arch: "arm64"},
		Protocol: ProtocolRange{Min: 1, Max: 2}, Collectors: []CollectorSpec{{ID: "disk.health"}},
		ArtifactSHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
}

func TestManifestValidationAndProtocolCompatibility(t *testing.T) {
	manifest := validManifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if !manifest.Protocol.Supports(1) || !manifest.Protocol.Supports(2) || manifest.Protocol.Supports(3) {
		t.Fatal("protocol range compatibility is incorrect")
	}
	manifest.Collectors = append(manifest.Collectors, manifest.Collectors[0])
	if err := manifest.Validate(); err == nil {
		t.Fatal("duplicate collector ID was accepted")
	}
	manifest = validManifest()
	manifest.Version = "../1.2.3"
	if err := manifest.Validate(); err == nil {
		t.Fatal("path traversal in plugin version was accepted")
	}
	manifest = validManifest()
	manifest.Platform.Arch = "../arm64"
	if err := manifest.Validate(); err == nil {
		t.Fatal("path traversal in plugin platform was accepted")
	}
}

func TestManifestValidatesPermissionIdentifiers(t *testing.T) {
	manifest := validManifest()
	manifest.Permissions = []string{"read:proc", "remote:openwrt_metrics"}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid permissions rejected: %v", err)
	}
	manifest.Permissions = []string{"read:proc", "read:proc"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("duplicate permission was accepted")
	}
	manifest.Permissions = []string{"read:anything;exec"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("malformed permission was accepted")
	}
}

func TestManifestValidatesMetricSchemaVersions(t *testing.T) {
	manifest := validManifest()
	manifest.MetricSchemaVersions = []string{"v1", "v2.1"}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("valid metric schema versions rejected: %v", err)
	}
	manifest.MetricSchemaVersions = []string{"v1", "v1"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("duplicate metric schema version was accepted")
	}
	manifest.MetricSchemaVersions = []string{"../v1"}
	if err := manifest.Validate(); err == nil {
		t.Fatal("path traversal in metric schema version was accepted")
	}
}

func TestLifecycleTransitions(t *testing.T) {
	if !CanTransition(StateVerified, StateStaged) {
		t.Fatal("verified plugin should be stageable")
	}
	if CanTransition(StateRunning, StateInstalled) {
		t.Fatal("running plugin must not jump directly to installed")
	}
	if !CanTransition(StateQuarantined, StateRunning) || !CanTransition(StateQuarantined, StateFailed) {
		t.Fatal("a new desired revision must be allowed to leave quarantine through a fresh attempt")
	}
	if CanTransition("unknown", StateRunning) {
		t.Fatal("unknown lifecycle state must not transition")
	}
}
