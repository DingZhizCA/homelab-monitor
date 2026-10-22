/*
Package plugins reconciles Agent plugin processes with Server desired state.

The reconciler acts only when its caller supplies a Desired State batch. It
does not poll on a timer or alter collector sampling cadence; package trust,
process startup and package storage remain delegated to injected components.
*/
package plugins

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// PluginPackageStager verifies and stores package bytes before execution.
type PluginPackageStager interface {
	Stage(manifestJSON, signature, artifact []byte) (StagedPackage, error)
}

// ManagedPlugin represents a prepared process owned by the injected Plugin Manager.
type ManagedPlugin interface {
	Close(context.Context) error
}

// ManagedPluginHealthChecker reports whether an active managed process is still healthy.
type ManagedPluginHealthChecker interface {
	CheckHealth(context.Context) error
}

// PluginPreparer starts and checks one staged package before it becomes active.
type PluginPreparer interface {
	PreparePlugin(context.Context, string) (ManagedPlugin, error)
}

// PluginArtifactDownloader pulls the exact package named by one desired state.
type PluginArtifactDownloader interface {
	DownloadArtifact(context.Context, string, DesiredState, int64) (PackageArtifact, error)
}

// RetryPolicy bounds consecutive plugin reconciliation failures without implicit timings.
type RetryPolicy struct {
	MaxConsecutiveFailures uint32
	InitialBackoff         time.Duration
	MaxBackoff             time.Duration
	Now                    func() time.Time
}

func (p RetryPolicy) validate() error {
	if p.MaxConsecutiveFailures == 0 || p.InitialBackoff <= 0 || p.MaxBackoff < p.InitialBackoff || p.Now == nil {
		return errors.New("plugin retry policy requires a positive failure limit, ordered backoffs, and a clock")
	}
	return nil
}

type activePlugin struct {
	packageData     StagedPackage
	process         ManagedPlugin
	appliedRevision uint64
	previous        *activePluginVersion
}

type activePluginVersion struct {
	packageData     StagedPackage
	appliedRevision uint64
}

type pluginFailure struct {
	revision    uint64
	consecutive uint32
	nextAttempt time.Time
	quarantined bool
	lastError   string
}

// Reconciler applies one Agent's desired plugin states under explicit caller control.
type Reconciler struct {
	mu              sync.Mutex
	deviceID        string
	stager          PluginPackageStager
	manager         PluginPreparer
	downloader      PluginArtifactDownloader
	maxArtifactSize int64
	retryPolicy     RetryPolicy
	active          map[string]activePlugin
	observed        map[string]DesiredState
	failures        map[string]pluginFailure
}

// NewReconciler requires all package boundaries and an explicit artifact size limit.
func NewReconciler(deviceID string, stager PluginPackageStager, manager PluginPreparer, downloader PluginArtifactDownloader, maxArtifactSize int64, retryPolicy RetryPolicy) (*Reconciler, error) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || stager == nil || manager == nil || downloader == nil || maxArtifactSize < 1 {
		return nil, errors.New("device ID, package stager, plugin manager, artifact downloader, and positive size limit are required")
	}
	if err := retryPolicy.validate(); err != nil {
		return nil, err
	}
	return &Reconciler{
		deviceID: deviceID, stager: stager, manager: manager, downloader: downloader,
		maxArtifactSize: maxArtifactSize, retryPolicy: retryPolicy,
		active: make(map[string]activePlugin), observed: make(map[string]DesiredState), failures: make(map[string]pluginFailure),
	}, nil
}

// Apply reconciles one explicit batch and returns a report for each desired plugin.
func (r *Reconciler) Apply(ctx context.Context, desired []DesiredState) ([]ReportedState, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := make(map[string]struct{}, len(desired))
	for _, state := range desired {
		if state.DeviceID != r.deviceID {
			return nil, fmt.Errorf("desired plugin state belongs to another device")
		}
		if err := state.Validate(); err != nil {
			return nil, err
		}
		if _, exists := seen[state.PluginID]; exists {
			return nil, fmt.Errorf("desired plugin batch contains duplicate plugin %q", state.PluginID)
		}
		seen[state.PluginID] = struct{}{}
		if current, exists := r.observed[state.PluginID]; exists {
			if state.Revision < current.Revision {
				return nil, fmt.Errorf("desired revision %d is older than observed revision %d for plugin %q", state.Revision, current.Revision, state.PluginID)
			}
			if state.Revision == current.Revision && state != current {
				return nil, fmt.Errorf("desired revision %d conflicts with observed state for plugin %q", state.Revision, state.PluginID)
			}
		}
	}

	reports := make([]ReportedState, 0, len(desired))
	var applyErrors []error
	for _, state := range desired {
		r.observed[state.PluginID] = state
		report, err := r.applyWithRetry(ctx, state)
		reports = append(reports, report)
		if err != nil {
			applyErrors = append(applyErrors, fmt.Errorf("reconcile plugin %q revision %d: %w", state.PluginID, state.Revision, err))
		}
	}
	slices.SortFunc(reports, func(left, right ReportedState) int {
		return cmpString(left.PluginID, right.PluginID)
	})
	return reports, errors.Join(applyErrors...)
}

// Close stops every active plugin during orderly Agent shutdown.
func (r *Reconciler) Close(ctx context.Context) error {
	if err := requirePluginDeadline(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var closeErrors []error
	for pluginID, active := range r.active {
		if err := active.process.Close(ctx); err != nil {
			closeErrors = append(closeErrors, fmt.Errorf("close plugin %q: %w", pluginID, err))
			continue
		}
		delete(r.active, pluginID)
	}
	return errors.Join(closeErrors...)
}

func (r *Reconciler) applyOne(ctx context.Context, desired DesiredState) (ReportedState, error) {
	current, hasCurrent := r.active[desired.PluginID]
	if !desired.Enabled {
		if hasCurrent {
			if err := current.process.Close(ctx); err != nil {
				return reportedFor(desired, current, StateDegraded, err.Error()), err
			}
			delete(r.active, desired.PluginID)
		}
		return ReportedState{
			DeviceID: r.deviceID, PluginID: desired.PluginID, ObservedRev: desired.Revision,
			AppliedRev: desired.Revision, Lifecycle: StateStopped,
		}, nil
	}
	if hasCurrent && current.packageData.Manifest.Version == desired.Version &&
		current.packageData.Manifest.Platform == desired.Artifact.Platform &&
		current.packageData.Manifest.ArtifactSHA256 == desired.Artifact.SHA256 {
		if checker, ok := current.process.(ManagedPluginHealthChecker); ok {
			if healthErr := checker.CheckHealth(ctx); healthErr != nil {
				if closeErr := current.process.Close(ctx); closeErr != nil {
					return reportedFor(desired, current, StateDegraded, errors.Join(healthErr, closeErr).Error()), errors.Join(healthErr, closeErr)
				}
				delete(r.active, desired.PluginID)
				restartPackage := current.packageData
				appliedRevision := desired.Revision
				rollback := current.previous != nil
				if rollback {
					restartPackage = current.previous.packageData
					appliedRevision = current.previous.appliedRevision
				}
				restarted, startErr := r.manager.PreparePlugin(ctx, restartPackage.RelativePath)
				if startErr == nil && restarted == nil {
					startErr = errors.New("plugin manager returned an empty recovery process")
				}
				if startErr != nil {
					if restarted != nil {
						startErr = errors.Join(startErr, restarted.Close(ctx))
					}
					if rollback {
						startErr = fmt.Errorf("restore previous plugin version after health failure: %w", startErr)
					} else {
						startErr = fmt.Errorf("restart unhealthy plugin: %w", startErr)
					}
					return ReportedState{DeviceID: r.deviceID, PluginID: desired.PluginID, ObservedRev: desired.Revision, Lifecycle: StateFailed, RefusalReason: startErr.Error()}, startErr
				}
				recovered := activePlugin{packageData: restartPackage, process: restarted, appliedRevision: appliedRevision}
				r.active[desired.PluginID] = recovered
				if rollback {
					return reportedFor(desired, recovered, StateRunning, "unhealthy desired version rolled back to previous applied version"), errors.New("plugin health check failed; previous version restored")
				}
				return reportedFor(desired, recovered, StateRunning, "restarted after plugin health check failed"), errors.New("plugin health check failed; current version restarted")
			}
		}
		current.appliedRevision = desired.Revision
		r.active[desired.PluginID] = current
		return reportedFor(desired, current, StateRunning, ""), nil
	}

	bundle, err := r.downloader.DownloadArtifact(ctx, r.deviceID, desired, r.maxArtifactSize)
	if err != nil {
		return reportCurrentOrFailed(r.deviceID, desired, current, hasCurrent, err)
	}
	if int64(len(bundle.Artifact)) > r.maxArtifactSize {
		return reportCurrentOrFailed(r.deviceID, desired, current, hasCurrent, errors.New("downloaded plugin artifact exceeds configured size limit"))
	}
	manifest, err := DecodeManifest(bundle.ManifestJSON)
	if err != nil {
		return reportCurrentOrFailed(r.deviceID, desired, current, hasCurrent, fmt.Errorf("decode downloaded plugin manifest: %w", err))
	}
	if manifest.PluginID != desired.PluginID || manifest.Version != desired.Version ||
		manifest.Platform != desired.Artifact.Platform || manifest.ArtifactSHA256 != desired.Artifact.SHA256 {
		return reportCurrentOrFailed(r.deviceID, desired, current, hasCurrent, errors.New("downloaded package does not match desired artifact identity"))
	}
	staged, err := r.stager.Stage(bundle.ManifestJSON, bundle.Signature, bundle.Artifact)
	if err != nil {
		return reportCurrentOrFailed(r.deviceID, desired, current, hasCurrent, err)
	}
	if staged.Manifest.PluginID != desired.PluginID || staged.Manifest.Version != desired.Version ||
		staged.Manifest.Platform != desired.Artifact.Platform || staged.Manifest.ArtifactSHA256 != desired.Artifact.SHA256 {
		return reportCurrentOrFailed(r.deviceID, desired, current, hasCurrent, errors.New("downloaded package does not match desired artifact identity"))
	}

	if hasCurrent {
		if err := current.process.Close(ctx); err != nil {
			return reportedFor(desired, current, StateDegraded, err.Error()), err
		}
		delete(r.active, desired.PluginID)
	}
	process, err := r.manager.PreparePlugin(ctx, staged.RelativePath)
	if err == nil && process == nil {
		err = errors.New("plugin manager returned an empty prepared process")
	}
	if err != nil {
		if process != nil {
			err = errors.Join(err, process.Close(ctx))
		}
		if hasCurrent {
			rollback, rollbackErr := r.manager.PreparePlugin(ctx, current.packageData.RelativePath)
			if rollbackErr == nil && rollback != nil {
				current.process = rollback
				r.active[desired.PluginID] = current
				return reportedFor(desired, current, StateRunning, "new package failed; previous package restored"), fmt.Errorf("prepare new plugin: %w", err)
			}
			if rollback != nil {
				err = errors.Join(err, rollback.Close(ctx))
			}
			delete(r.active, desired.PluginID)
			return ReportedState{DeviceID: r.deviceID, PluginID: desired.PluginID, ObservedRev: desired.Revision, Lifecycle: StateFailed, RefusalReason: errors.Join(err, rollbackErr).Error()}, errors.Join(err, rollbackErr)
		}
		return ReportedState{DeviceID: r.deviceID, PluginID: desired.PluginID, ObservedRev: desired.Revision, Lifecycle: StateFailed, RefusalReason: err.Error()}, err
	}
	active := activePlugin{packageData: staged, process: process, appliedRevision: desired.Revision}
	if hasCurrent {
		active.previous = &activePluginVersion{packageData: current.packageData, appliedRevision: current.appliedRevision}
	}
	r.active[desired.PluginID] = active
	return reportedFor(desired, active, StateRunning, ""), nil
}

func (r *Reconciler) applyWithRetry(ctx context.Context, desired DesiredState) (ReportedState, error) {
	if !desired.Enabled {
		delete(r.failures, desired.PluginID)
	} else if failure, ok := r.failures[desired.PluginID]; ok {
		if failure.revision != desired.Revision {
			delete(r.failures, desired.PluginID)
		} else if failure.quarantined || r.retryPolicy.Now().Before(failure.nextAttempt) {
			return r.retryDeferredReport(desired, failure), nil
		}
	}

	report, err := r.applyOne(ctx, desired)
	if err == nil {
		delete(r.failures, desired.PluginID)
		return report, nil
	}
	if !desired.Enabled {
		return report, err
	}
	failure := r.failures[desired.PluginID]
	if failure.revision != desired.Revision {
		failure = pluginFailure{revision: desired.Revision}
	}
	failure.consecutive++
	failure.lastError = err.Error()
	failure.quarantined = failure.consecutive >= r.retryPolicy.MaxConsecutiveFailures
	if !failure.quarantined {
		failure.nextAttempt = r.retryPolicy.Now().Add(retryBackoff(r.retryPolicy, failure.consecutive))
	}
	r.failures[desired.PluginID] = failure
	if failure.quarantined {
		if current, active := r.active[desired.PluginID]; active {
			if current.appliedRevision == desired.Revision && current.packageData.Manifest.Version == desired.Version &&
				current.packageData.Manifest.Platform == desired.Artifact.Platform && current.packageData.Manifest.ArtifactSHA256 == desired.Artifact.SHA256 {
				if closeErr := current.process.Close(ctx); closeErr == nil {
					delete(r.active, desired.PluginID)
					report.Lifecycle = StateQuarantined
					report.AppliedRev = 0
				} else {
					report.Lifecycle = StateDegraded
					failure.lastError = errors.Join(errors.New(failure.lastError), fmt.Errorf("stop quarantined plugin: %w", closeErr)).Error()
				}
			} else {
				report.Lifecycle = StateDegraded
			}
		} else {
			report.Lifecycle = StateQuarantined
		}
		r.failures[desired.PluginID] = failure
		report.RefusalReason = failure.lastError + "; desired revision quarantined after consecutive failures"
	}
	return report, err
}

func (r *Reconciler) retryDeferredReport(desired DesiredState, failure pluginFailure) ReportedState {
	if failure.quarantined {
		if current, ok := r.active[desired.PluginID]; ok {
			return reportedFor(desired, current, StateDegraded, failure.lastError+"; desired revision is quarantined")
		}
		return ReportedState{
			DeviceID: r.deviceID, PluginID: desired.PluginID, ObservedRev: desired.Revision,
			Lifecycle: StateQuarantined, RefusalReason: failure.lastError + "; desired revision is quarantined",
		}
	}
	message := fmt.Sprintf("retry deferred until %s after failure: %s", failure.nextAttempt.UTC().Format(time.RFC3339Nano), failure.lastError)
	if current, ok := r.active[desired.PluginID]; ok {
		return reportedFor(desired, current, StateRunning, message)
	}
	return ReportedState{DeviceID: r.deviceID, PluginID: desired.PluginID, ObservedRev: desired.Revision, Lifecycle: StateFailed, RefusalReason: message}
}

func retryBackoff(policy RetryPolicy, consecutiveFailures uint32) time.Duration {
	delay := policy.InitialBackoff
	for count := uint32(1); count < consecutiveFailures && delay < policy.MaxBackoff; count++ {
		if delay > policy.MaxBackoff/2 {
			return policy.MaxBackoff
		}
		delay *= 2
	}
	return min(delay, policy.MaxBackoff)
}

func reportCurrentOrFailed(deviceID string, desired DesiredState, current activePlugin, hasCurrent bool, cause error) (ReportedState, error) {
	if hasCurrent {
		return reportedFor(desired, current, StateRunning, cause.Error()), cause
	}
	return ReportedState{DeviceID: deviceID, PluginID: desired.PluginID, ObservedRev: desired.Revision, Lifecycle: StateFailed, RefusalReason: cause.Error()}, cause
}

func reportedFor(desired DesiredState, active activePlugin, lifecycle LifecycleState, reason string) ReportedState {
	return ReportedState{
		DeviceID: desired.DeviceID, PluginID: desired.PluginID, ObservedRev: desired.Revision,
		AppliedRev: active.appliedRevision, Lifecycle: lifecycle, RefusalReason: reason,
	}
}

func cmpString(left, right string) int {
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}
