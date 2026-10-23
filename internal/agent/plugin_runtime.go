/*
Package agent assembles the local plugin reconciliation runtime.

Configuration supplies every trust, filesystem, protocol, and artifact-size
input explicitly. This object performs one control poll only when called; it
does not create background polling or collector sampling schedules.
*/
package agent

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

// PluginRuntimeConfig contains explicit local trust and package runtime boundaries.
type PluginRuntimeConfig struct {
	ServerURL              string
	DeviceID               string
	DeviceToken            string
	PublisherKeyPath       string
	PermissionFile         string
	StagingDirectory       string
	RuntimeDirectory       string
	HostProtocolMajor      uint16
	MaxArtifactBytes       int64
	MaxStagingBytes        int64
	MaxStagedPackages      int
	MaxStagingScanEntries  int
	StagingCleanupInterval time.Duration
	StagingOrphanGrace     time.Duration
	StagingCleanupLimit    int
	StagingCleanupTimeout  time.Duration
	StagingObserver        StagingCleanupObserver
	StateDirectory         string
	MaxStateEntries        int
	MaxStateBytes          int64
	OperationTimeout       time.Duration
	MaxConsecutiveFailures uint32
	InitialFailureBackoff  time.Duration
	MaxFailureBackoff      time.Duration
}

// StagingCleanupObservation reports one Agent staging cleanup pass.
type StagingCleanupObservation struct {
	Cutoff          time.Time
	ScannedEntries  int
	Packages        int
	UsedBytes       int64
	DeletedPackages int
	DeletedBytes    int64
	Duration        time.Duration
	Err             error
}

type StagingCleanupObserver func(StagingCleanupObservation)

// PluginRuntime owns the Agent's package verifier, process manager, and reconciler.
type PluginRuntime struct {
	mu              sync.Mutex
	closeMu         sync.Mutex
	deviceID        string
	control         *PluginControlClient
	reconciler      *plugins.Reconciler
	stager          *plugins.Stager
	stateStore      *pluginReconcilerFileStore
	closing         bool
	closed          bool
	reconcileCancel context.CancelFunc
	reconcileDone   chan struct{}
	cleanupInterval time.Duration
	orphanGrace     time.Duration
	cleanupLimit    int
	cleanupTimeout  time.Duration
	cleanupObserver StagingCleanupObserver
	lastCleanup     time.Time
	now             func() time.Time
}

// NewPluginRuntime assembles plugin control only from explicit trust and resource settings.
func NewPluginRuntime(config PluginRuntimeConfig) (*PluginRuntime, error) {
	if strings.TrimSpace(config.DeviceID) == "" || strings.TrimSpace(config.DeviceID) != config.DeviceID || config.HostProtocolMajor == 0 || config.MaxArtifactBytes < 1 || config.MaxStagingBytes < config.MaxArtifactBytes || config.MaxStagedPackages < 1 || config.MaxStagingScanEntries < 1 || config.MaxStagedPackages > (config.MaxStagingScanEntries-1)/8 || config.StagingCleanupInterval <= 0 || config.StagingOrphanGrace <= 0 || config.StagingCleanupLimit < 1 || config.StagingCleanupTimeout <= 0 || config.StagingObserver == nil || config.OperationTimeout <= 0 || !filepath.IsAbs(config.StateDirectory) || config.MaxStateEntries < 1 || config.MaxStateBytes < 1 {
		return nil, errors.New("plugin identity, protocol, staging, cleanup, operation, and local state limits are required")
	}
	if config.MaxConsecutiveFailures == 0 || config.InitialFailureBackoff <= 0 || config.MaxFailureBackoff < config.InitialFailureBackoff {
		return nil, errors.New("plugin retry failure limit and ordered backoffs are required")
	}
	control, err := newPluginControlClient(config.ServerURL, config.DeviceToken, config.OperationTimeout)
	if err != nil {
		return nil, fmt.Errorf("configure plugin control client: %w", err)
	}
	publicKey, err := plugins.LoadPublisherPublicKey(config.PublisherKeyPath)
	if err != nil {
		return nil, fmt.Errorf("load plugin publisher trust root: %w", err)
	}
	authorizer, err := plugins.LoadGrantAuthorizer(config.PermissionFile)
	if err != nil {
		return nil, fmt.Errorf("load local plugin grants: %w", err)
	}
	stager, err := plugins.NewBoundedStager(config.StagingDirectory, publicKey, plugins.StagingLimits{
		MaxArtifactBytes: config.MaxArtifactBytes, MaxStagingBytes: config.MaxStagingBytes,
		MaxPackages: config.MaxStagedPackages, MaxScanEntries: config.MaxStagingScanEntries,
	})
	if err != nil {
		return nil, fmt.Errorf("open plugin package stager: %w", err)
	}
	supervisor, err := plugins.NewExecSupervisor(config.RuntimeDirectory)
	if err != nil {
		_ = stager.Close()
		return nil, fmt.Errorf("configure plugin process supervisor: %w", err)
	}
	manager, err := plugins.NewPluginManager(stager, supervisor, config.HostProtocolMajor, authorizer)
	if err != nil {
		_ = stager.Close()
		return nil, fmt.Errorf("configure plugin manager: %w", err)
	}
	stateStore, err := openPluginReconcilerStore(config.StateDirectory, config.DeviceID, config.MaxStateBytes)
	if err != nil {
		_ = stager.Close()
		return nil, fmt.Errorf("open plugin reconciler state: %w", err)
	}
	reconciler, err := plugins.NewReconciler(config.DeviceID, stager, manager, control, config.MaxArtifactBytes, config.MaxStateEntries, plugins.RetryPolicy{
		MaxConsecutiveFailures: config.MaxConsecutiveFailures,
		InitialBackoff:         config.InitialFailureBackoff,
		MaxBackoff:             config.MaxFailureBackoff,
		Now:                    time.Now,
	}, stateStore)
	if err != nil {
		_ = stateStore.Close()
		_ = stager.Close()
		return nil, fmt.Errorf("configure plugin reconciler: %w", err)
	}
	return &PluginRuntime{
		deviceID: config.DeviceID, control: control, reconciler: reconciler, stager: stager, stateStore: stateStore,
		cleanupInterval: config.StagingCleanupInterval, orphanGrace: config.StagingOrphanGrace,
		cleanupLimit: config.StagingCleanupLimit, cleanupTimeout: config.StagingCleanupTimeout,
		cleanupObserver: config.StagingObserver, now: time.Now,
	}, nil
}

// ReconcileOnce polls and applies one control batch, without scheduling metrics.
func (r *PluginRuntime) ReconcileOnce(ctx context.Context) error {
	if ctx == nil {
		return errors.New("plugin reconciliation context is required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("plugin reconciliation context must have a deadline")
	}
	r.mu.Lock()
	if r.closed || r.closing {
		r.mu.Unlock()
		return errors.New("plugin runtime is closing or closed")
	}
	if r.reconcileDone != nil {
		r.mu.Unlock()
		return errors.New("plugin reconciliation is already in progress")
	}
	operationCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.reconcileCancel = cancel
	r.reconcileDone = done
	r.mu.Unlock()

	cleanupErr := r.cleanupStaging(operationCtx)
	reconcileErr := ReconcilePluginsOnce(operationCtx, r.deviceID, r.control, r.reconciler)
	err := errors.Join(cleanupErr, reconcileErr)
	cancel()
	r.mu.Lock()
	r.reconcileCancel = nil
	r.reconcileDone = nil
	close(done)
	r.mu.Unlock()
	return err
}

func (r *PluginRuntime) cleanupStaging(ctx context.Context) error {
	now := r.now().UTC()
	if !r.lastCleanup.IsZero() && now.Sub(r.lastCleanup) < r.cleanupInterval {
		return nil
	}
	started := time.Now()
	observation := StagingCleanupObservation{Cutoff: now.Add(-r.orphanGrace)}
	cleanupCtx, cancel := context.WithTimeout(ctx, r.cleanupTimeout)
	defer cancel()
	result, err := r.stager.Prune(cleanupCtx, r.reconciler.ProtectedPackagePaths(), observation.Cutoff, r.cleanupLimit)
	observation.ScannedEntries = result.ScannedEntries
	observation.Packages = result.Packages
	observation.UsedBytes = result.UsedBytes
	observation.DeletedPackages = result.DeletedPackages
	observation.DeletedBytes = result.DeletedBytes
	observation.Duration = time.Since(started)
	if err != nil {
		observation.Err = fmt.Errorf("clean plugin staging: %w", err)
	} else {
		r.lastCleanup = now
	}
	r.cleanupObserver(observation)
	return observation.Err
}

// Close cancels an in-flight reconciliation before stopping processes and closing the stager.
func (r *PluginRuntime) Close(ctx context.Context) error {
	if err := requireRuntimeDeadline(ctx); err != nil {
		return err
	}
	r.closeMu.Lock()
	defer r.closeMu.Unlock()

	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closing = true
	if r.reconcileCancel != nil {
		r.reconcileCancel()
	}
	done := r.reconcileDone
	r.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := r.reconciler.Close(ctx); err != nil {
		return err
	}
	var stateErr error
	if r.stateStore != nil {
		stateErr = r.stateStore.Close()
		r.stateStore = nil
	}
	stagerErr := r.stager.Close()
	r.mu.Lock()
	r.closed = true
	r.closing = false
	r.mu.Unlock()
	return errors.Join(stateErr, stagerErr)
}

func requireRuntimeDeadline(ctx context.Context) error {
	if ctx == nil {
		return errors.New("plugin runtime close context is required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("plugin runtime close context must have a deadline")
	}
	return nil
}
