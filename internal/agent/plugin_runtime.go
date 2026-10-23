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
	"slices"
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
	LauncherPath           string
	ProcessLimits          plugins.ProcessLimits
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
	Collection             *PluginCollectionRuntimeConfig
}

// PluginCollectionRuntimeConfig enables independent policy scheduling and delivery.
type PluginCollectionRuntimeConfig struct {
	StateDirectory string
	MaxPolicies    int
	MaxTasksPerRun int
	MaxConcurrent  int
	QueueLimits    PluginBatchQueueLimits
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
	operationCancel context.CancelFunc
	operationDone   chan struct{}
	cleanupInterval time.Duration
	orphanGrace     time.Duration
	cleanupLimit    int
	cleanupTimeout  time.Duration
	cleanupObserver StagingCleanupObserver
	lastCleanup     time.Time
	now             func() time.Time
	collection      *pluginCollectionRuntime
}

type pluginCollectionRuntime struct {
	scheduler   *plugins.PluginScheduler
	queue       *PluginBatchQueue
	sender      *Sender
	maxPolicies int
	maxTasks    int
	policies    []plugins.CollectionPolicy
	lastAttempt map[string]time.Time
}

// NewPluginRuntime assembles plugin control only from explicit trust and resource settings.
func NewPluginRuntime(config PluginRuntimeConfig) (*PluginRuntime, error) {
	if strings.TrimSpace(config.DeviceID) == "" || strings.TrimSpace(config.DeviceID) != config.DeviceID || config.HostProtocolMajor == 0 || !filepath.IsAbs(config.LauncherPath) || config.ProcessLimits.Validate() != nil || config.MaxArtifactBytes < 1 || config.MaxStagingBytes < config.MaxArtifactBytes || config.MaxStagedPackages < 1 || config.MaxStagingScanEntries < 1 || config.MaxStagedPackages > (config.MaxStagingScanEntries-1)/8 || config.StagingCleanupInterval <= 0 || config.StagingOrphanGrace <= 0 || config.StagingCleanupLimit < 1 || config.StagingCleanupTimeout <= 0 || config.StagingObserver == nil || config.OperationTimeout <= 0 || !filepath.IsAbs(config.StateDirectory) || config.MaxStateEntries < 1 || config.MaxStateBytes < 1 {
		return nil, errors.New("plugin identity, protocol, process, staging, cleanup, operation, and local state limits are required")
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
	supervisor, err := plugins.NewLimitedExecSupervisor(config.RuntimeDirectory, config.LauncherPath, config.ProcessLimits)
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
	var collection *pluginCollectionRuntime
	if config.Collection != nil {
		collectionConfig := config.Collection
		if !filepath.IsAbs(collectionConfig.StateDirectory) || collectionConfig.MaxPolicies < 1 || collectionConfig.MaxTasksPerRun < 1 || collectionConfig.MaxTasksPerRun > collectionConfig.MaxPolicies || collectionConfig.MaxConcurrent < 1 || collectionConfig.MaxConcurrent > collectionConfig.MaxTasksPerRun {
			_ = stateStore.Close()
			_ = stager.Close()
			return nil, errors.New("plugin collection requires an absolute queue directory and positive policy, task, and concurrency limits")
		}
		scheduler, err := plugins.NewPluginScheduler(collectionConfig.MaxConcurrent)
		if err != nil {
			_ = stateStore.Close()
			_ = stager.Close()
			return nil, err
		}
		queue, err := OpenPluginBatchQueue(collectionConfig.StateDirectory, config.DeviceID, collectionConfig.QueueLimits)
		if err != nil {
			_ = stateStore.Close()
			_ = stager.Close()
			return nil, fmt.Errorf("open plugin observation queue: %w", err)
		}
		collection = &pluginCollectionRuntime{scheduler: scheduler, queue: queue, sender: NewSender(config.ServerURL, config.DeviceToken), maxPolicies: collectionConfig.MaxPolicies, maxTasks: collectionConfig.MaxTasksPerRun, lastAttempt: make(map[string]time.Time)}
	}
	return &PluginRuntime{
		deviceID: config.DeviceID, control: control, reconciler: reconciler, stager: stager, stateStore: stateStore,
		cleanupInterval: config.StagingCleanupInterval, orphanGrace: config.StagingOrphanGrace,
		cleanupLimit: config.StagingCleanupLimit, cleanupTimeout: config.StagingCleanupTimeout,
		cleanupObserver: config.StagingObserver, now: time.Now, collection: collection,
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
	operationCtx, done, err := r.beginOperation(ctx)
	if err != nil {
		return err
	}

	cleanupErr := r.cleanupStaging(operationCtx)
	reconcileErr := ReconcilePluginsOnce(operationCtx, r.deviceID, r.control, r.reconciler)
	policyErr := r.refreshCollectionPolicies(operationCtx)
	r.endOperation(done)
	return errors.Join(cleanupErr, reconcileErr, policyErr)
}

func (r *PluginRuntime) beginOperation(ctx context.Context) (context.Context, chan struct{}, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.closing {
		return nil, nil, errors.New("plugin runtime is closing or closed")
	}
	if r.operationDone != nil {
		return nil, nil, errors.New("another plugin operation is already in progress")
	}
	operationCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	r.operationCancel = cancel
	r.operationDone = done
	return operationCtx, done, nil
}

func (r *PluginRuntime) endOperation(done chan struct{}) {
	r.mu.Lock()
	if r.operationCancel != nil {
		r.operationCancel()
	}
	r.operationCancel = nil
	r.operationDone = nil
	close(done)
	r.mu.Unlock()
}

func (r *PluginRuntime) refreshCollectionPolicies(ctx context.Context) error {
	if r.collection == nil {
		return nil
	}
	policies, err := r.control.CollectionPolicies(ctx, r.deviceID)
	if err != nil {
		return fmt.Errorf("poll plugin collection policies: %w", err)
	}
	if len(policies) > r.collection.maxPolicies {
		return fmt.Errorf("plugin collection policy count %d exceeds local limit %d", len(policies), r.collection.maxPolicies)
	}
	activeKeys := make(map[string]struct{}, len(policies))
	for _, policy := range policies {
		if err := policy.Validate(); err != nil || policy.DeviceID != r.deviceID {
			return errors.New("plugin collection policy response is invalid")
		}
		activeKeys[collectionPolicyKey(policy)] = struct{}{}
	}
	slices.SortFunc(policies, func(a, b plugins.CollectionPolicy) int {
		return strings.Compare(collectionPolicyKey(a), collectionPolicyKey(b))
	})
	r.collection.policies = policies
	for key := range r.collection.lastAttempt {
		if _, exists := activeKeys[key]; !exists {
			delete(r.collection.lastAttempt, key)
		}
	}
	return nil
}

// CollectOnce sends retained data, runs a bounded due-policy batch, and sends once more.
func (r *PluginRuntime) CollectOnce(ctx context.Context) error {
	if ctx == nil {
		return errors.New("plugin collection context is required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("plugin collection context must have a deadline")
	}
	if r.collection == nil {
		return errors.New("plugin collection runtime is disabled")
	}
	operationCtx, done, err := r.beginOperation(ctx)
	if err != nil {
		return err
	}
	defer r.endOperation(done)
	var runErrors []error
	if _, _, err := r.collection.queue.SendPending(operationCtx, r.collection.sender); err != nil {
		runErrors = append(runErrors, fmt.Errorf("send pending plugin observations: %w", err))
	}
	now := r.now().UTC()
	tasks := make([]plugins.CollectionTask, 0, r.collection.maxTasks)
	keys := make([]string, 0, r.collection.maxTasks)
	for _, policy := range r.collection.policies {
		if !policy.Enabled || len(tasks) >= r.collection.maxTasks {
			continue
		}
		key := collectionPolicyKey(policy)
		if previous := r.collection.lastAttempt[key]; !previous.IsZero() && now.Sub(previous) < time.Duration(policy.IntervalSeconds)*time.Second {
			continue
		}
		// A failed or unavailable target still consumes this policy interval so a
		// short scheduler tick cannot become an implicit high-rate retry loop.
		r.collection.lastAttempt[key] = now
		target, ok := r.reconciler.CollectionTarget(policy.PluginID, policy.PluginRevision)
		if !ok {
			runErrors = append(runErrors, fmt.Errorf("collection target %s is not active at desired revision %d", key, policy.PluginRevision))
			continue
		}
		tasks = append(tasks, plugins.CollectionTask{Plugin: target, CollectorID: policy.CollectorID, Config: append([]byte(nil), policy.Config...), Timeout: time.Duration(policy.TimeoutMilliseconds) * time.Millisecond})
		keys = append(keys, key)
	}
	if len(tasks) > 0 {
		outcomes, collectErr := r.collection.scheduler.CollectBatch(operationCtx, tasks)
		if collectErr != nil {
			runErrors = append(runErrors, collectErr)
		} else {
			for index, outcome := range outcomes {
				if outcome.Err != nil {
					runErrors = append(runErrors, fmt.Errorf("collect %s: %w", keys[index], outcome.Err))
					continue
				}
				observation, err := NormalizePluginOutcome(r.deviceID, outcome)
				if err == nil {
					err = r.collection.queue.Append(observation)
				}
				if err != nil {
					runErrors = append(runErrors, fmt.Errorf("queue %s: %w", keys[index], err))
				}
			}
		}
	}
	if _, _, err := r.collection.queue.SendPending(operationCtx, r.collection.sender); err != nil {
		runErrors = append(runErrors, fmt.Errorf("send collected plugin observations: %w", err))
	}
	return errors.Join(runErrors...)
}

func collectionPolicyKey(policy plugins.CollectionPolicy) string {
	return policy.PluginID + "/" + policy.CollectorID
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
	if r.operationCancel != nil {
		r.operationCancel()
	}
	done := r.operationDone
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
	var queueErr error
	if r.collection != nil {
		queueErr = r.collection.queue.Close()
	}
	r.mu.Lock()
	r.closed = true
	r.closing = false
	r.mu.Unlock()
	return errors.Join(stateErr, stagerErr, queueErr)
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
