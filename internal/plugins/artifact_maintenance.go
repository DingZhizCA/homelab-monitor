/*
Package plugins schedules bounded cleanup of immutable Server artifact packages.

Cleanup obtains a caller supplied publication lock before reading Desired State
and pruning, so an Owner publication cannot race package removal.
*/
package plugins

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ArtifactReferenceSource lists every package that current Desired State protects.
type ArtifactReferenceSource interface {
	DesiredArtifactKeysContext(context.Context) ([]ArtifactKey, error)
}

// ArtifactMaintenanceConfig bounds cleanup age, schedule, work, and duration.
type ArtifactMaintenanceConfig struct {
	Interval    time.Duration
	OrphanGrace time.Duration
	DeleteLimit int
	Timeout     time.Duration
}

// ArtifactMaintenanceObservation reports one cleanup pass.
type ArtifactMaintenanceObservation struct {
	Cutoff          time.Time
	ScannedEntries  int
	Packages        int
	UsedBytes       int64
	DeletedPackages int
	DeletedBytes    int64
	Duration        time.Duration
	Err             error
}

// ArtifactMaintenanceObserver receives each scheduled or direct cleanup result.
type ArtifactMaintenanceObserver func(ArtifactMaintenanceObservation)

// ArtifactMaintenance removes old unreferenced packages without owning a goroutine.
type ArtifactMaintenance struct {
	repository *FileArtifactRepository
	references ArtifactReferenceSource
	mutation   sync.Locker
	config     ArtifactMaintenanceConfig
	observe    ArtifactMaintenanceObserver
	now        func() time.Time
	mu         sync.Mutex
}

// NewArtifactMaintenance binds explicit cleanup policy and publication coordination.
func NewArtifactMaintenance(repository *FileArtifactRepository, references ArtifactReferenceSource, mutation sync.Locker, config ArtifactMaintenanceConfig, observer ArtifactMaintenanceObserver) (*ArtifactMaintenance, error) {
	if repository == nil || references == nil || mutation == nil || observer == nil {
		return nil, errors.New("artifact repository, references, publication lock, and observer are required")
	}
	if config.Interval <= 0 || config.OrphanGrace <= 0 || config.DeleteLimit < 1 || config.Timeout <= 0 {
		return nil, errors.New("positive artifact maintenance interval, grace period, delete limit, and timeout are required")
	}
	return &ArtifactMaintenance{repository: repository, references: references, mutation: mutation, config: config, observe: observer, now: time.Now}, nil
}

// Run performs one bounded cleanup per interval until the context ends.
func (m *ArtifactMaintenance) Run(ctx context.Context) {
	ticker := time.NewTicker(m.config.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.Cleanup(ctx)
		}
	}
}

// Cleanup removes packages that are both unreferenced and older than the grace period.
func (m *ArtifactMaintenance) Cleanup(ctx context.Context) ArtifactMaintenanceObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	started := time.Now()
	observation := ArtifactMaintenanceObservation{Cutoff: m.now().UTC().Add(-m.config.OrphanGrace)}
	operationContext, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	m.mutation.Lock()
	defer m.mutation.Unlock()
	keys, err := m.references.DesiredArtifactKeysContext(operationContext)
	if err != nil {
		observation.Err = fmt.Errorf("list protected plugin artifacts: %w", err)
		return m.finish(started, observation)
	}
	protected := make(map[ArtifactKey]struct{}, len(keys))
	for _, key := range keys {
		if err := key.Validate(); err != nil {
			observation.Err = fmt.Errorf("validate protected plugin artifact: %w", err)
			return m.finish(started, observation)
		}
		protected[key] = struct{}{}
	}
	result, err := m.repository.Prune(operationContext, protected, observation.Cutoff, m.config.DeleteLimit)
	observation.ScannedEntries = result.ScannedEntries
	observation.Packages = result.Packages
	observation.UsedBytes = result.UsedBytes
	observation.DeletedPackages = result.DeletedPackages
	observation.DeletedBytes = result.DeletedBytes
	if err != nil {
		observation.Err = fmt.Errorf("prune plugin artifacts: %w", err)
	}
	return m.finish(started, observation)
}

func (m *ArtifactMaintenance) finish(started time.Time, observation ArtifactMaintenanceObservation) ArtifactMaintenanceObservation {
	observation.Duration = time.Since(started)
	m.observe(observation)
	return observation
}
