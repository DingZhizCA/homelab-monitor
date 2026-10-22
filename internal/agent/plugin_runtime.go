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
	OperationTimeout       time.Duration
	MaxConsecutiveFailures uint32
	InitialFailureBackoff  time.Duration
	MaxFailureBackoff      time.Duration
}

// PluginRuntime owns the Agent's package verifier, process manager, and reconciler.
type PluginRuntime struct {
	mu              sync.Mutex
	closeMu         sync.Mutex
	deviceID        string
	control         *PluginControlClient
	reconciler      *plugins.Reconciler
	stager          *plugins.Stager
	closing         bool
	closed          bool
	reconcileCancel context.CancelFunc
	reconcileDone   chan struct{}
}

// NewPluginRuntime assembles plugin control only from explicit trust and resource settings.
func NewPluginRuntime(config PluginRuntimeConfig) (*PluginRuntime, error) {
	if strings.TrimSpace(config.DeviceID) == "" || strings.TrimSpace(config.DeviceID) != config.DeviceID || config.HostProtocolMajor == 0 || config.MaxArtifactBytes < 1 || config.OperationTimeout <= 0 {
		return nil, errors.New("plugin device ID, host protocol major, positive artifact size limit, and operation timeout are required")
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
	stager, err := plugins.NewStager(config.StagingDirectory, publicKey, config.MaxArtifactBytes)
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
	reconciler, err := plugins.NewReconciler(config.DeviceID, stager, manager, control, config.MaxArtifactBytes, plugins.RetryPolicy{
		MaxConsecutiveFailures: config.MaxConsecutiveFailures,
		InitialBackoff:         config.InitialFailureBackoff,
		MaxBackoff:             config.MaxFailureBackoff,
		Now:                    time.Now,
	})
	if err != nil {
		_ = stager.Close()
		return nil, fmt.Errorf("configure plugin reconciler: %w", err)
	}
	return &PluginRuntime{deviceID: config.DeviceID, control: control, reconciler: reconciler, stager: stager}, nil
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

	err := ReconcilePluginsOnce(operationCtx, r.deviceID, r.control, r.reconciler)
	cancel()
	r.mu.Lock()
	r.reconcileCancel = nil
	r.reconcileDone = nil
	close(done)
	r.mu.Unlock()
	return err
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
	if err := r.stager.Close(); err != nil {
		return err
	}
	r.mu.Lock()
	r.closed = true
	r.closing = false
	r.mu.Unlock()
	return nil
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
