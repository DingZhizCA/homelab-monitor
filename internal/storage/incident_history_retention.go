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

// IncidentHistoryRetentionConfig bounds archive cleanup by age, work, and time.
type IncidentHistoryRetentionConfig struct {
	Interval    time.Duration
	Retention   time.Duration
	DeleteLimit int
	Timeout     time.Duration
}

// IncidentHistoryRetentionObservation describes one cleanup transaction.
type IncidentHistoryRetentionObservation struct {
	Cutoff             time.Time
	Deleted            int64
	DeletedArchives    int64
	DeletedTransitions int64
	Duration           time.Duration
	Err                error
}

// IncidentHistoryRetentionObserver receives every cleanup result.
type IncidentHistoryRetentionObserver func(IncidentHistoryRetentionObservation)

// IncidentHistoryRetentionManager removes expired superseded revisions.
type IncidentHistoryRetentionManager struct {
	database *sql.DB
	config   IncidentHistoryRetentionConfig
	observe  IncidentHistoryRetentionObserver
	now      func() time.Time
	mu       sync.Mutex
}

// NewIncidentHistoryRetentionManager requires a complete explicit policy.
func NewIncidentHistoryRetentionManager(database *sql.DB, config IncidentHistoryRetentionConfig, observer IncidentHistoryRetentionObserver) (*IncidentHistoryRetentionManager, error) {
	if database == nil || observer == nil {
		return nil, errors.New("core database and Incident history retention observer are required")
	}
	if config.Interval <= 0 || config.Retention <= 0 || config.DeleteLimit < 1 || config.Timeout <= 0 {
		return nil, errors.New("positive Incident history retention settings are required")
	}
	return &IncidentHistoryRetentionManager{database: database, config: config, observe: observer, now: time.Now}, nil
}

// Run performs one bounded cleanup per interval until the context ends.
func (m *IncidentHistoryRetentionManager) Run(ctx context.Context) {
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
func (m *IncidentHistoryRetentionManager) Cleanup(ctx context.Context) IncidentHistoryRetentionObservation {
	m.mu.Lock()
	defer m.mu.Unlock()
	started := time.Now()
	observation := IncidentHistoryRetentionObservation{Cutoff: m.now().UTC().Add(-m.config.Retention)}
	operationContext, cancel := context.WithTimeout(ctx, m.config.Timeout)
	defer cancel()
	transaction, err := m.database.BeginTx(operationContext, nil)
	if err != nil {
		observation.Err = fmt.Errorf("begin Incident history retention: %w", err)
		return m.finish(started, observation)
	}
	defer transaction.Rollback()
	result, err := transaction.ExecContext(operationContext, `DELETE FROM incident_revision_archives
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
	observation.DeletedArchives, err = result.RowsAffected()
	if err != nil {
		observation.Err = fmt.Errorf("count deleted Incident archives: %w", err)
		return m.finish(started, observation)
	}
	result, err = transaction.ExecContext(operationContext, `DELETE FROM incident_transitions
		WHERE rowid IN (
			SELECT rowid FROM incident_transitions
			WHERE occurred_at < ?
			ORDER BY occurred_at, event_id
			LIMIT ?
		)`, storageTimestamp(observation.Cutoff), m.config.DeleteLimit)
	if err != nil {
		observation.DeletedArchives = 0
		observation.Err = fmt.Errorf("delete expired Incident transitions: %w", err)
		return m.finish(started, observation)
	}
	observation.DeletedTransitions, err = result.RowsAffected()
	if err != nil {
		observation.DeletedArchives = 0
		observation.Err = fmt.Errorf("count deleted Incident transitions: %w", err)
		return m.finish(started, observation)
	}
	if err := transaction.Commit(); err != nil {
		observation.DeletedArchives = 0
		observation.DeletedTransitions = 0
		observation.Err = fmt.Errorf("commit Incident history retention: %w", err)
		return m.finish(started, observation)
	}
	observation.Deleted = observation.DeletedArchives + observation.DeletedTransitions
	return m.finish(started, observation)
}

func (m *IncidentHistoryRetentionManager) finish(started time.Time, observation IncidentHistoryRetentionObservation) IncidentHistoryRetentionObservation {
	observation.Duration = time.Since(started)
	m.observe(observation)
	return observation
}
