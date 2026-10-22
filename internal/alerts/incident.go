/*
Package alerts advances one rule's in-memory incident state from an evaluation.

Callers must supply all timing and Unknown/Disabled behavior explicitly. This
transition function does not persist state, migrate rule revisions, or deliver
notifications.
*/
package alerts

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

type IncidentStatus string

const (
	IncidentInactive   IncidentStatus = "inactive"
	IncidentPending    IncidentStatus = "pending"
	IncidentFiring     IncidentStatus = "firing"
	IncidentRecovering IncidentStatus = "recovering"
	IncidentResolved   IncidentStatus = "resolved"
)

type UncertainBehavior string

const (
	// UncertainHold preserves state timestamps, so elapsed wall time still counts.
	UncertainHold UncertainBehavior = "hold"
	// UncertainResetPending clears Pending or cancels Recovering back to Firing.
	UncertainResetPending UncertainBehavior = "reset_pending"
	// UncertainResolve closes any active Pending, Firing, or Recovering state.
	UncertainResolve UncertainBehavior = "resolve"
)

// IncidentPolicy contains caller-selected transition timing and uncertainty behavior.
type IncidentPolicy struct {
	PendingFor       *time.Duration
	RecoveryFor      *time.Duration
	UnknownBehavior  UncertainBehavior
	DisabledBehavior UncertainBehavior
}

// IncidentState is one rule revision's transition state, not a durable Incident record.
type IncidentState struct {
	DeviceID        string         `json:"device_id"`
	RuleID          string         `json:"rule_id"`
	RuleRevision    uint64         `json:"rule_revision"`
	Status          IncidentStatus `json:"status"`
	PendingSince    time.Time      `json:"pending_since,omitempty"`
	FiringSince     time.Time      `json:"firing_since,omitempty"`
	RecoveringSince time.Time      `json:"recovering_since,omitempty"`
	ResolvedAt      time.Time      `json:"resolved_at,omitempty"`
	LastEvaluatedAt time.Time      `json:"last_evaluated_at"`
	LastEvaluation  State          `json:"last_evaluation"`
}

var (
	// ErrIncidentPolicy reports missing or unsupported caller-selected behavior.
	ErrIncidentPolicy = errors.New("incident policy is invalid")
	// ErrIncidentIdentity reports a mismatch between a rule and its evaluation or state.
	ErrIncidentIdentity = errors.New("incident evaluation identity is invalid")
	// ErrIncidentRevision prevents silent reuse of state across rule revisions.
	ErrIncidentRevision = errors.New("incident state belongs to a different rule revision")
	// ErrIncidentTime reports zero or out-of-order evaluation time.
	ErrIncidentTime = errors.New("incident evaluation time is invalid")
	// ErrIncidentState reports malformed prior transition state.
	ErrIncidentState = errors.New("incident state is invalid")
)

// AdvanceIncident applies one timestamped evaluation without choosing defaults.
func AdvanceIncident(current IncidentState, rule Rule, result EvaluationResult, at time.Time, policy IncidentPolicy) (IncidentState, error) {
	if err := validateIncidentPolicy(policy); err != nil {
		return IncidentState{}, err
	}
	if !ruleIDPattern.MatchString(rule.ID) || strings.TrimSpace(rule.DeviceID) == "" || strings.TrimSpace(rule.DeviceID) != rule.DeviceID || rule.Revision == 0 || result.RuleID != rule.ID || result.Revision != rule.Revision {
		return IncidentState{}, ErrIncidentIdentity
	}
	if at.IsZero() {
		return IncidentState{}, ErrIncidentTime
	}
	at = at.UTC()
	if err := validateEvaluationForRule(rule, result.State); err != nil {
		return IncidentState{}, err
	}
	if current.DeviceID != "" {
		if err := validateIncidentState(current); err != nil {
			return IncidentState{}, err
		}
		if current.DeviceID != rule.DeviceID || current.RuleID != rule.ID {
			return IncidentState{}, ErrIncidentIdentity
		}
		if current.RuleRevision != rule.Revision {
			return IncidentState{}, ErrIncidentRevision
		}
		if at.Before(current.LastEvaluatedAt) {
			return IncidentState{}, ErrIncidentTime
		}
	} else {
		if current != (IncidentState{}) {
			return IncidentState{}, ErrIncidentState
		}
		current = IncidentState{DeviceID: rule.DeviceID, RuleID: rule.ID, RuleRevision: rule.Revision, Status: IncidentInactive}
	}

	current.LastEvaluatedAt = at
	current.LastEvaluation = result.State
	switch result.State {
	case StateUnknown:
		applyUncertainBehavior(&current, at, policy.UnknownBehavior)
	case StateDisabled:
		applyUncertainBehavior(&current, at, policy.DisabledBehavior)
	case StateTrue:
		applyTrueEvaluation(&current, at, *policy.PendingFor)
	case StateFalse:
		applyFalseEvaluation(&current, at, *policy.RecoveryFor)
	}
	return current, nil
}

func validateIncidentPolicy(policy IncidentPolicy) error {
	if policy.PendingFor == nil || policy.RecoveryFor == nil || *policy.PendingFor < 0 || *policy.RecoveryFor < 0 || !validUncertainBehavior(policy.UnknownBehavior) || !validUncertainBehavior(policy.DisabledBehavior) {
		return ErrIncidentPolicy
	}
	return nil
}

func validUncertainBehavior(behavior UncertainBehavior) bool {
	switch behavior {
	case UncertainHold, UncertainResetPending, UncertainResolve:
		return true
	default:
		return false
	}
}

func validateEvaluationForRule(rule Rule, evaluation State) error {
	if rule.Enabled {
		if evaluation == StateDisabled {
			return fmt.Errorf("%w: enabled rule cannot have a disabled result", ErrIncidentIdentity)
		}
		switch evaluation {
		case StateTrue, StateFalse, StateUnknown:
			return nil
		default:
			return ErrIncidentIdentity
		}
	}
	if evaluation != StateDisabled {
		return fmt.Errorf("%w: disabled rule requires a disabled result", ErrIncidentIdentity)
	}
	return nil
}

func validateIncidentState(state IncidentState) error {
	if !ruleIDPattern.MatchString(state.RuleID) || strings.TrimSpace(state.DeviceID) == "" || strings.TrimSpace(state.DeviceID) != state.DeviceID || state.RuleRevision == 0 || state.LastEvaluatedAt.IsZero() {
		return ErrIncidentState
	}
	if state.PendingSince.After(state.LastEvaluatedAt) || state.FiringSince.After(state.LastEvaluatedAt) || state.RecoveringSince.After(state.LastEvaluatedAt) || state.ResolvedAt.After(state.LastEvaluatedAt) {
		return ErrIncidentState
	}
	switch state.LastEvaluation {
	case StateTrue, StateFalse, StateUnknown, StateDisabled:
	default:
		return ErrIncidentState
	}
	switch state.Status {
	case IncidentInactive:
		if !state.PendingSince.IsZero() || !state.FiringSince.IsZero() || !state.RecoveringSince.IsZero() || !state.ResolvedAt.IsZero() {
			return ErrIncidentState
		}
	case IncidentPending:
		if state.PendingSince.IsZero() || !state.FiringSince.IsZero() || !state.RecoveringSince.IsZero() || !state.ResolvedAt.IsZero() {
			return ErrIncidentState
		}
	case IncidentFiring:
		if state.FiringSince.IsZero() || !state.PendingSince.IsZero() || !state.RecoveringSince.IsZero() || !state.ResolvedAt.IsZero() {
			return ErrIncidentState
		}
	case IncidentRecovering:
		if state.FiringSince.IsZero() || state.RecoveringSince.IsZero() || !state.PendingSince.IsZero() || !state.ResolvedAt.IsZero() {
			return ErrIncidentState
		}
	case IncidentResolved:
		if state.ResolvedAt.IsZero() || !state.PendingSince.IsZero() || !state.RecoveringSince.IsZero() {
			return ErrIncidentState
		}
	default:
		return ErrIncidentState
	}
	return nil
}

// ValidateIncidentState verifies a state loaded by a persistence adapter.
func ValidateIncidentState(state IncidentState) error {
	return validateIncidentState(state)
}

func applyUncertainBehavior(state *IncidentState, at time.Time, behavior UncertainBehavior) {
	switch behavior {
	case UncertainHold:
	case UncertainResetPending:
		switch state.Status {
		case IncidentPending:
			setIncidentInactive(state)
		case IncidentRecovering:
			state.Status = IncidentFiring
			state.RecoveringSince = time.Time{}
		}
	case UncertainResolve:
		if state.Status == IncidentPending || state.Status == IncidentFiring || state.Status == IncidentRecovering {
			setIncidentResolved(state, at)
		}
	}
}

func applyTrueEvaluation(state *IncidentState, at time.Time, pendingFor time.Duration) {
	switch state.Status {
	case IncidentInactive, IncidentResolved:
		if pendingFor == 0 {
			setIncidentFiring(state, at)
		} else {
			state.Status = IncidentPending
			state.PendingSince = at
			state.FiringSince = time.Time{}
			state.RecoveringSince = time.Time{}
			state.ResolvedAt = time.Time{}
		}
	case IncidentPending:
		if at.Sub(state.PendingSince) >= pendingFor {
			setIncidentFiring(state, at)
		}
	case IncidentFiring:
	case IncidentRecovering:
		state.Status = IncidentFiring
		state.RecoveringSince = time.Time{}
	}
}

func applyFalseEvaluation(state *IncidentState, at time.Time, recoveryFor time.Duration) {
	switch state.Status {
	case IncidentInactive, IncidentResolved:
	case IncidentPending:
		setIncidentInactive(state)
	case IncidentFiring:
		if recoveryFor == 0 {
			setIncidentResolved(state, at)
		} else {
			state.Status = IncidentRecovering
			state.RecoveringSince = at
		}
	case IncidentRecovering:
		if at.Sub(state.RecoveringSince) >= recoveryFor {
			setIncidentResolved(state, at)
		}
	}
}

func setIncidentInactive(state *IncidentState) {
	state.Status = IncidentInactive
	state.PendingSince = time.Time{}
	state.FiringSince = time.Time{}
	state.RecoveringSince = time.Time{}
	state.ResolvedAt = time.Time{}
}

func setIncidentFiring(state *IncidentState, at time.Time) {
	state.Status = IncidentFiring
	state.PendingSince = time.Time{}
	state.FiringSince = at
	state.RecoveringSince = time.Time{}
	state.ResolvedAt = time.Time{}
}

func setIncidentResolved(state *IncidentState, at time.Time) {
	state.Status = IncidentResolved
	state.PendingSince = time.Time{}
	state.RecoveringSince = time.Time{}
	state.ResolvedAt = at
}
