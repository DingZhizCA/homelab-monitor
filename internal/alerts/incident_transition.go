/*
Package alerts defines durable events for meaningful Incident status changes.

Repeated evaluations that leave the status unchanged do not create events. The
event identifier is deterministic so persistence and notification consumers can
use it as an idempotency key.
*/
package alerts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"time"
)

// IncidentTransition is one immutable status change and its resulting state.
type IncidentTransition struct {
	ID           string         `json:"id"`
	DeviceID     string         `json:"device_id"`
	RuleID       string         `json:"rule_id"`
	RuleRevision uint64         `json:"rule_revision"`
	FromStatus   IncidentStatus `json:"from_status"`
	ToStatus     IncidentStatus `json:"to_status"`
	OccurredAt   time.Time      `json:"occurred_at"`
	State        IncidentState  `json:"state"`
}

// BuildIncidentTransition returns an event only when the status changed.
func BuildIncidentTransition(previous IncidentState, existed bool, next IncidentState) (IncidentTransition, bool, error) {
	if err := ValidateIncidentState(next); err != nil {
		return IncidentTransition{}, false, err
	}
	from := IncidentInactive
	if existed {
		if err := ValidateIncidentState(previous); err != nil || previous.DeviceID != next.DeviceID || previous.RuleID != next.RuleID || previous.RuleRevision != next.RuleRevision || next.LastEvaluatedAt.Before(previous.LastEvaluatedAt) {
			return IncidentTransition{}, false, ErrIncidentState
		}
		from = previous.Status
	}
	if from == next.Status {
		return IncidentTransition{}, false, nil
	}
	event := IncidentTransition{
		DeviceID: next.DeviceID, RuleID: next.RuleID, RuleRevision: next.RuleRevision,
		FromStatus: from, ToStatus: next.Status, OccurredAt: next.LastEvaluatedAt.UTC(), State: next,
	}
	event.ID = incidentTransitionID(event)
	return event, true, nil
}

// ValidateIncidentTransition verifies an event loaded from persistence.
func ValidateIncidentTransition(event IncidentTransition) error {
	if len(event.ID) != sha256.Size*2 {
		return errors.New("Incident transition ID is invalid")
	}
	if _, err := hex.DecodeString(event.ID); err != nil {
		return errors.New("Incident transition ID is invalid")
	}
	if err := ValidateIncidentState(event.State); err != nil || event.DeviceID != event.State.DeviceID || event.RuleID != event.State.RuleID || event.RuleRevision != event.State.RuleRevision || !validIncidentTransitionStatus(event.FromStatus) || event.FromStatus == event.ToStatus || event.ToStatus != event.State.Status || !event.OccurredAt.Equal(event.State.LastEvaluatedAt) || event.ID != incidentTransitionID(event) {
		return errors.New("Incident transition is invalid")
	}
	return nil
}

func incidentTransitionID(event IncidentTransition) string {
	parts := []string{event.DeviceID, event.RuleID, strconv.FormatUint(event.RuleRevision, 10), event.OccurredAt.UTC().Format(time.RFC3339Nano), string(event.FromStatus), string(event.ToStatus)}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:])
}

func validIncidentTransitionStatus(status IncidentStatus) bool {
	switch status {
	case IncidentInactive, IncidentPending, IncidentFiring, IncidentRecovering, IncidentResolved:
		return true
	default:
		return false
	}
}
