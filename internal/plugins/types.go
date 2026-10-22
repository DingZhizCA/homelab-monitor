/*
Package plugins defines the plugin control-plane model and local runtime adapters.

This package owns manifest validation, integrity verification, RPC contracts,
desired/reported state, and lifecycle vocabulary. It does not launch plugin
binaries, manage publisher trust roots, or change the Agent-to-Server metrics protocol.

Data path: Server desired state -> Agent registry -> future supervisor/host.
*/
package plugins

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

var (
	identifierPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)
	versionPattern    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9.+_-]{0,127}$`)
	sha256Pattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	permissionPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*:[a-z][a-z0-9_]*$`)
)

// Manifest describes a plugin package without asserting that it is trusted.
// Signature verification and platform-specific execution remain host duties.
type Manifest struct {
	PluginID  string        `json:"plugin_id"`
	Version   string        `json:"version"`
	Publisher string        `json:"publisher"`
	Platform  Platform      `json:"platform"`
	Protocol  ProtocolRange `json:"protocol_range"`
	// MetricSchemaVersions declares the metric payload schemas this package can emit.
	// An empty list preserves legacy package bytes and makes no compatibility claim.
	MetricSchemaVersions []string        `json:"metric_schema_versions,omitempty"`
	Collectors           []CollectorSpec `json:"collectors"`
	Permissions          []string        `json:"permissions,omitempty"`
	ArtifactSHA256       string          `json:"artifact_sha256"`
}

// Platform identifies the target operating-system and CPU architecture.
type Platform struct {
	OS   string `json:"os"`
	Arch string `json:"arch"`
}

// ArtifactKey locates an immutable plugin package independent of its transport.
type ArtifactKey struct {
	PluginID string
	Version  string
	Platform Platform
	SHA256   string
}

// Validate checks that each artifact key component is safe for path construction.
func (key ArtifactKey) Validate() error {
	if !identifierPattern.MatchString(key.PluginID) || !versionPattern.MatchString(key.Version) ||
		!identifierPattern.MatchString(key.Platform.OS) || !identifierPattern.MatchString(key.Platform.Arch) ||
		!sha256Pattern.MatchString(key.SHA256) {
		return errors.New("artifact key requires valid plugin, version, platform, and SHA-256 fields")
	}
	return nil
}

// ProtocolRange is the inclusive range of plugin-host protocol majors supported.
type ProtocolRange struct {
	Min uint16 `json:"min"`
	Max uint16 `json:"max"`
}

// CollectorSpec names one independently addressable collector in a package.
type CollectorSpec struct {
	ID string `json:"id"`
}

// Validate checks manifest shape only; it does not establish publisher trust.
func (m Manifest) Validate() error {
	if !identifierPattern.MatchString(m.PluginID) {
		return errors.New("plugin_id must be a stable identifier")
	}
	if !versionPattern.MatchString(m.Version) || strings.TrimSpace(m.Publisher) == "" || strings.TrimSpace(m.Publisher) != m.Publisher {
		return errors.New("version must be a path-safe identifier and publisher is required")
	}
	if !identifierPattern.MatchString(m.Platform.OS) || !identifierPattern.MatchString(m.Platform.Arch) {
		return errors.New("platform os and arch must be stable identifiers")
	}
	if m.Protocol.Min == 0 || m.Protocol.Max < m.Protocol.Min {
		return errors.New("protocol range must be non-zero and ordered")
	}
	if !sha256Pattern.MatchString(m.ArtifactSHA256) {
		return errors.New("artifact_sha256 must be a lowercase SHA-256 digest")
	}
	if len(m.Collectors) == 0 {
		return errors.New("at least one collector is required")
	}
	seen := make(map[string]struct{}, len(m.Collectors))
	for _, collector := range m.Collectors {
		if !identifierPattern.MatchString(collector.ID) {
			return fmt.Errorf("invalid collector id %q", collector.ID)
		}
		if _, ok := seen[collector.ID]; ok {
			return fmt.Errorf("duplicate collector id %q", collector.ID)
		}
		seen[collector.ID] = struct{}{}
	}
	seenPermissions := make(map[string]struct{}, len(m.Permissions))
	for _, permission := range m.Permissions {
		if !permissionPattern.MatchString(permission) {
			return fmt.Errorf("invalid permission %q", permission)
		}
		if _, ok := seenPermissions[permission]; ok {
			return fmt.Errorf("duplicate permission %q", permission)
		}
		seenPermissions[permission] = struct{}{}
	}
	seenSchemaVersions := make(map[string]struct{}, len(m.MetricSchemaVersions))
	for _, version := range m.MetricSchemaVersions {
		if !versionPattern.MatchString(version) {
			return fmt.Errorf("invalid metric schema version %q", version)
		}
		if _, ok := seenSchemaVersions[version]; ok {
			return fmt.Errorf("duplicate metric schema version %q", version)
		}
		seenSchemaVersions[version] = struct{}{}
	}
	return nil
}

// Supports reports whether the plugin and host share a protocol major version.
func (r ProtocolRange) Supports(hostMajor uint16) bool {
	return hostMajor >= r.Min && hostMajor <= r.Max
}

// LifecycleState describes observed local execution progress, not desired intent.
type LifecycleState string

const (
	StateInstalled   LifecycleState = "installed"
	StateVerified    LifecycleState = "verified"
	StateStaged      LifecycleState = "staged"
	StateStarting    LifecycleState = "starting"
	StateRunning     LifecycleState = "running"
	StateDegraded    LifecycleState = "degraded"
	StateFailed      LifecycleState = "failed"
	StateStopping    LifecycleState = "stopping"
	StateStopped     LifecycleState = "stopped"
	StateQuarantined LifecycleState = "quarantined"
)

// CanTransition reports whether a lifecycle transition is part of the model.
func CanTransition(from, to LifecycleState) bool {
	allowed, ok := lifecycleTransitions[from]
	if !ok {
		return false
	}
	_, ok = allowed[to]
	return ok
}

// ValidLifecycleState reports whether state is part of the shared lifecycle vocabulary.
func ValidLifecycleState(state LifecycleState) bool {
	_, ok := lifecycleTransitions[state]
	return ok
}

var lifecycleTransitions = map[LifecycleState]map[LifecycleState]struct{}{
	StateInstalled:   states(StateVerified, StateFailed, StateQuarantined),
	StateVerified:    states(StateStaged, StateFailed, StateQuarantined),
	StateStaged:      states(StateStarting, StateFailed, StateQuarantined),
	StateStarting:    states(StateRunning, StateFailed, StateQuarantined),
	StateRunning:     states(StateDegraded, StateStopping, StateFailed, StateQuarantined),
	StateDegraded:    states(StateRunning, StateStopping, StateFailed, StateQuarantined),
	StateFailed:      states(StateStarting, StateStopped, StateQuarantined),
	StateStopping:    states(StateStopped, StateFailed, StateQuarantined),
	StateStopped:     states(StateStarting, StateQuarantined),
	StateQuarantined: states(StateStarting, StateRunning, StateFailed, StateStopped),
}

func states(values ...LifecycleState) map[LifecycleState]struct{} {
	result := make(map[LifecycleState]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
