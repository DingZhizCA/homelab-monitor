/*
Package alerts provides a bounded in-memory Incident state repository.

The repository serializes one rule's transitions and applies explicit CAS. It
does not choose transition policy, migrate revisions, persist, or notify.
*/
package alerts

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	ErrIncidentVersionConflict = errors.New("incident state version conflict")
	ErrIncidentCapacity        = errors.New("incident repository capacity reached")
)

// IncidentRepository stores current state with timestamp compare-and-swap.
type IncidentRepository interface {
	Current(context.Context, string, string) (IncidentState, bool, error)
	ListCurrent(context.Context, string) ([]IncidentState, error)
	Advance(context.Context, Rule, EvaluationResult, time.Time, IncidentPolicy, time.Time) (IncidentState, error)
}

type incidentKey struct {
	deviceID string
	ruleID   string
}

// MemoryIncidentRepository stores current transition state for development and tests.
type MemoryIncidentRepository struct {
	mu         sync.RWMutex
	maxEntries int
	states     map[incidentKey]IncidentState
}

var _ IncidentRepository = (*MemoryIncidentRepository)(nil)

// NewMemoryIncidentRepository requires an explicit upper bound for retained states.
func NewMemoryIncidentRepository(maxEntries int) (*MemoryIncidentRepository, error) {
	if maxEntries < 1 {
		return nil, errors.New("positive incident repository capacity is required")
	}
	return &MemoryIncidentRepository{maxEntries: maxEntries, states: make(map[incidentKey]IncidentState)}, nil
}

// Current returns the current state for one device-scoped rule.
func (r *MemoryIncidentRepository) Current(ctx context.Context, deviceID, ruleID string) (IncidentState, bool, error) {
	if err := validateIncidentRepositoryRequest(ctx, deviceID, ruleID); err != nil {
		return IncidentState{}, false, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	state, ok := r.states[incidentKey{deviceID: deviceID, ruleID: ruleID}]
	return state, ok, nil
}

// ListCurrent returns a stable snapshot sorted by rule ID for one device.
func (r *MemoryIncidentRepository) ListCurrent(ctx context.Context, deviceID string) ([]IncidentState, error) {
	if ctx == nil {
		return nil, errors.New("incident repository context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return nil, ErrIncidentIdentity
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	states := make([]IncidentState, 0)
	for key, state := range r.states {
		if key.deviceID == deviceID {
			states = append(states, state)
		}
	}
	sort.Slice(states, func(i, j int) bool { return states[i].RuleID < states[j].RuleID })
	return states, nil
}

// Advance applies one evaluation when expectedLastEvaluatedAt matches current state.
func (r *MemoryIncidentRepository) Advance(ctx context.Context, rule Rule, result EvaluationResult, at time.Time, policy IncidentPolicy, expectedLastEvaluatedAt time.Time) (IncidentState, error) {
	if err := validateIncidentRepositoryRequest(ctx, rule.DeviceID, rule.ID); err != nil {
		return IncidentState{}, err
	}
	key := incidentKey{deviceID: rule.DeviceID, ruleID: rule.ID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return IncidentState{}, err
	}
	current, exists := r.states[key]
	if exists && current.LastEvaluatedAt.Equal(at.UTC()) {
		if current.LastEvaluation != result.State || current.RuleRevision != rule.Revision {
			return IncidentState{}, ErrIncidentVersionConflict
		}
		next, err := AdvanceIncident(current, rule, result, at, policy)
		if err != nil {
			return IncidentState{}, err
		}
		if next != current {
			return IncidentState{}, ErrIncidentVersionConflict
		}
		return current, nil
	}
	if (!exists && !expectedLastEvaluatedAt.IsZero()) || (exists && !current.LastEvaluatedAt.Equal(expectedLastEvaluatedAt.UTC())) {
		return IncidentState{}, ErrIncidentVersionConflict
	}
	next, err := AdvanceIncident(current, rule, result, at, policy)
	if err != nil {
		return IncidentState{}, err
	}
	if !exists && len(r.states) >= r.maxEntries {
		return IncidentState{}, ErrIncidentCapacity
	}
	r.states[key] = next
	return next, nil
}

func validateIncidentRepositoryRequest(ctx context.Context, deviceID, ruleID string) error {
	if ctx == nil {
		return errors.New("incident repository context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || !ruleIDPattern.MatchString(ruleID) {
		return ErrIncidentIdentity
	}
	return nil
}
