/*
Package storage applies explicitly configured, bounded retention to superseded
Incident revisions. It does not choose a retention window or remove current
Incident state.
*/
package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"
)

// IncidentArchiveRetentionConfig bounds archive cleanup by age, work, and time.
type IncidentArchiveRetentionConfig struct {
	Interval    time.Duration
	Retention   time.Duration
	DeleteLimit int
	Timeout     time.Duration
}

// IncidentArchiveRetentionObservation describes one cleanup transaction.
type IncidentArchiveRetentionObservation struct {
	Cutoff   time.Time
	Deleted  int64
	Duration time.Duration
	Err      error
}

// IncidentArchiveRetentionObserver receives every cleanup result.
type IncidentArchiveRetentionObserver func(IncidentArchiveRetentionObservation)

// IncidentArchiveRetentionManager removes expired superseded revisions.
type IncidentArchiveRetentionManager struct {
	database *sql.DB
	config   IncidentArchiveRetentionConfig
	observe  IncidentArchiveRetentionObserver
	now      func() time.Time
	mu       sync.Mutex
}

// NewIncidentArchiveRetentionManager requires a complete explicit policy.
func NewIncidentArchiveRetentionManager(database *sql.DB, config IncidentArchiveRetentionConfig, observer IncidentArchiveRetentionObserver) (*IncidentArchiveRetentionManager, error) {
	if database == nil || observer == nil {
		return nil, errors.New("core database and Incident archive retention observer are required")
	}
	if config.Interval <= 0 || config.Retention <= 0 || config.DeleteLimit < 1 || config.Timeout <= 0 {
		return nil, errors.New("positive Incident archive retention settings are required")
	}
	return &IncidentArchiveRetentionManager{database: database, config: config, observe: observer, now: time.Now}, nil
}

// Run performs one bounded cleanup per interval until the context ends.
func (m *IncidentArchiveRetentionManager) Run(ctx context.Context) {
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

// Cleanup removes the oldest expired archives up to the configured limit.
func (m *IncidentArchiveRetentionManager) Cleanup(ctx context.Context) IncidentArchiveRetentionObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	started := time.Now()
	observation := IncidentArchiveRetentionObservation{Cutoff: m.now().UTC().Add(-m.config.Retention)}
	operationContext, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	result, err := m.database.ExecContext(operationContext, `DELETE FROM incident_revision_archives
		WHERE rowid IN (
			SELECT rowid FROM incident_revision_archives
			WHERE superseded_at < ?
			ORDER BY superseded_at, device_id, rule_id, rule_revision
			LIMIT ?
		)`, storageTimestamp(observation.Cutoff), m.config.DeleteLimit)
	if err != nil {
		observation.Err = fmt.Errorf("delete expired Incident archives: %w", err)
		return m.finish(started, observation)
	}
	observation.Deleted, err = result.RowsAffected()
	if err != nil {
		observation.Deleted = 0
		observation.Err = fmt.Errorf("count deleted Incident archives: %w", err)
	}
	return m.finish(started, observation)
}

func (m *IncidentArchiveRetentionManager) finish(started time.Time, observation IncidentArchiveRetentionObservation) IncidentArchiveRetentionObservation {
	observation.Duration = time.Since(started)
	m.observe(observation)
	return observation
}
