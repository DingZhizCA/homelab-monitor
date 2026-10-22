/*
Package plugins coordinates verified package startup through injected process
and Plugin Host boundaries. It prepares a healthy instance but does not schedule
collection or claim OS isolation beyond the configured Supervisor implementation.
*/
package plugins

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"sync"
)

// PluginManager prepares staged packages for use after protocol and health checks.
type PluginManager struct {
	stager            *Stager
	supervisor        Supervisor
	authorizer        PluginAuthorizer
	hostProtocolMajor uint16
}

// NewPluginManager requires package authorization as well as runtime boundaries.
func NewPluginManager(stager *Stager, supervisor Supervisor, hostProtocolMajor uint16, authorizer PluginAuthorizer) (*PluginManager, error) {
	if stager == nil || supervisor == nil || authorizer == nil || hostProtocolMajor == 0 {
		return nil, errors.New("stager, supervisor, plugin authorizer, and non-zero host protocol major are required")
	}
	return &PluginManager{stager: stager, supervisor: supervisor, authorizer: authorizer, hostProtocolMajor: hostProtocolMajor}, nil
}

// PreparedPlugin is a verified, handshaken, healthy process not yet scheduled for collection.
type PreparedPlugin struct {
	Package    StagedPackage
	Collectors []CollectorDescriptor
	mu         sync.RWMutex
	closing    bool
	process    Process
	host       *GRPCHost
}

// ValidateConfig delegates side-effect-free collector configuration validation.
func (p *PreparedPlugin) ValidateConfig(ctx context.Context, collectorID string, config []byte) error {
	if err := requirePluginDeadline(ctx); err != nil {
		return err
	}
	p.mu.RLock()
	host := p.host
	closing := p.closing
	p.mu.RUnlock()
	if host == nil || closing {
		return errors.New("prepared plugin is closed")
	}
	return host.ValidateConfig(ctx, collectorID, config)
}

// Collect invokes one described collector under the caller's deadline.
func (p *PreparedPlugin) Collect(ctx context.Context, request CollectRequest) (CollectResult, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return CollectResult{}, err
	}
	p.mu.RLock()
	host := p.host
	closing := p.closing
	p.mu.RUnlock()
	if host == nil || closing {
		return CollectResult{}, errors.New("prepared plugin is closed")
	}
	return host.Collect(ctx, request)
}

// Health checks process health through the Plugin Host contract.
func (p *PreparedPlugin) Health(ctx context.Context) (HealthStatus, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return HealthStatus{}, err
	}
	p.mu.RLock()
	host := p.host
	closing := p.closing
	p.mu.RUnlock()
	if host == nil || closing {
		return HealthStatus{}, errors.New("prepared plugin is closed")
	}
	return host.Health(ctx)
}

// CheckHealth adapts protocol health into the reconciler's process-health boundary.
func (p *PreparedPlugin) CheckHealth(ctx context.Context) error {
	health, err := p.Health(ctx)
	if err != nil {
		return err
	}
	if !health.Healthy {
		return fmt.Errorf("plugin health check failed: %s", health.Message)
	}
	return nil
}

// Prepare verifies a staged package, starts it, and checks its protocol and declared collectors.
func (m *PluginManager) Prepare(ctx context.Context, relativePath string) (*PreparedPlugin, error) {
	if err := requirePluginDeadline(ctx); err != nil {
		return nil, err
	}
	staged, err := m.stager.VerifyStaged(relativePath)
	if err != nil {
		return nil, fmt.Errorf("verify staged plugin: %w", err)
	}
	if !staged.Manifest.Protocol.Supports(m.hostProtocolMajor) {
		return nil, &RPCError{Code: RPCIncompatibleProtocol, Message: "manifest does not support the host protocol major"}
	}
	if staged.Manifest.Platform.OS != runtime.GOOS || staged.Manifest.Platform.Arch != runtime.GOARCH {
		return nil, fmt.Errorf("plugin platform %s-%s does not match host %s-%s", staged.Manifest.Platform.OS, staged.Manifest.Platform.Arch, runtime.GOOS, runtime.GOARCH)
	}
	policyManifest := staged.Manifest
	policyManifest.Collectors = append([]CollectorSpec(nil), staged.Manifest.Collectors...)
	policyManifest.Permissions = append([]string(nil), staged.Manifest.Permissions...)
	if err := m.authorizer.AuthorizePlugin(policyManifest); err != nil {
		return nil, fmt.Errorf("authorize plugin package: %w", err)
	}
	process, err := m.supervisor.Start(ctx, ProcessSpec{Manifest: staged.Manifest, ArtifactPath: staged.ArtifactPath})
	if err != nil {
		if process != nil {
			return nil, errors.Join(fmt.Errorf("start plugin process: %w", err), process.Stop(ctx))
		}
		return nil, fmt.Errorf("start plugin process: %w", err)
	}
	if process == nil {
		return nil, errors.New("supervisor returned an empty process handle")
	}
	cleanup := func(host *GRPCHost) error {
		var hostErr error
		if host != nil {
			hostErr = host.Shutdown(ctx)
		}
		return errors.Join(hostErr, process.Stop(ctx))
	}
	host, err := NewGRPCHost(process.SocketPath())
	if err != nil {
		return nil, errors.Join(fmt.Errorf("connect to plugin host: %w", err), cleanup(nil))
	}
	if _, err := host.Handshake(ctx, HostIdentity{
		PluginID: staged.Manifest.PluginID, Version: staged.Manifest.Version, ProtocolMajor: m.hostProtocolMajor,
	}); err != nil {
		return nil, errors.Join(fmt.Errorf("plugin handshake: %w", err), cleanup(host))
	}
	collectors, err := host.Describe(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("describe plugin collectors: %w", err), cleanup(host))
	}
	describedIDs := make([]string, 0, len(collectors))
	for _, collector := range collectors {
		describedIDs = append(describedIDs, collector.ID)
	}
	wantedIDs := make([]string, 0, len(staged.Manifest.Collectors))
	for _, collector := range staged.Manifest.Collectors {
		wantedIDs = append(wantedIDs, collector.ID)
	}
	slices.Sort(describedIDs)
	slices.Sort(wantedIDs)
	if !slices.Equal(describedIDs, wantedIDs) {
		return nil, errors.Join(errors.New("plugin collector descriptions do not match the signed manifest"), cleanup(host))
	}
	health, err := host.Health(ctx)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("check plugin health: %w", err), cleanup(host))
	}
	if !health.Healthy {
		return nil, errors.Join(fmt.Errorf("plugin is not healthy: %s", health.Message), cleanup(host))
	}
	return &PreparedPlugin{Package: staged, Collectors: collectors, process: process, host: host}, nil
}

// PreparePlugin adapts PluginManager to the Agent reconciliation boundary.
func (m *PluginManager) PreparePlugin(ctx context.Context, relativePath string) (ManagedPlugin, error) {
	return m.Prepare(ctx, relativePath)
}

// Close requests Plugin Host shutdown and then stops the supervised process.
func (p *PreparedPlugin) Close(ctx context.Context) error {
	if p == nil {
		return nil
	}
	if err := requirePluginDeadline(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	if p.process == nil {
		p.mu.Unlock()
		return nil
	}
	if p.closing {
		p.mu.Unlock()
		return errors.New("prepared plugin is already closing")
	}
	p.closing = true
	host, process := p.host, p.process
	p.host = nil
	p.mu.Unlock()

	var hostErr error
	if host != nil {
		hostErr = host.Shutdown(ctx)
	}
	processErr := process.Stop(ctx)
	p.mu.Lock()
	p.closing = false
	if processErr == nil {
		if p.process == process {
			p.process = nil
		}
	}
	p.mu.Unlock()
	return errors.Join(hostErr, processErr)
}

func requirePluginDeadline(ctx context.Context) error {
	if ctx == nil {
		return errors.New("plugin operation context is required")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("plugin operation context must have a deadline")
	}
	return nil
}
