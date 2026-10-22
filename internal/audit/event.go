/*
Package audit defines storage-neutral records for security-sensitive changes.

Events identify an actor, action, and target without carrying credentials,
request bodies, or other free-form data that could leak secrets into logs.
*/
package audit

import (
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const maxIdentityBytes = 256

var namePattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,127}$`)

// Event is the minimum immutable record that must commit with a managed change.
type Event struct {
	OccurredAt   time.Time `json:"occurred_at"`
	ActorID      string    `json:"actor_id"`
	Action       string    `json:"action"`
	ResourceType string    `json:"resource_type"`
	ResourceID   string    `json:"resource_id"`
}

// NewEvent validates bounded identifiers and normalizes the timestamp to UTC.
func NewEvent(at time.Time, actorID, action, resourceType, resourceID string) (Event, error) {
	event := Event{
		OccurredAt:   at.UTC(),
		ActorID:      actorID,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
	}
	if err := event.Validate(); err != nil {
		return Event{}, err
	}
	return event, nil
}

// Validate rejects ambiguous or unbounded audit identities.
func (e Event) Validate() error {
	if e.OccurredAt.IsZero() {
		return errors.New("audit event time is required")
	}
	if !validIdentity(e.ActorID) || !validIdentity(e.ResourceID) {
		return errors.New("audit actor and resource IDs must be bounded, trimmed UTF-8 strings")
	}
	if !namePattern.MatchString(e.Action) || !namePattern.MatchString(e.ResourceType) {
		return errors.New("audit action and resource type must be stable lowercase names")
	}
	return nil
}

func validIdentity(value string) bool {
	return value != "" && len(value) <= maxIdentityBytes && utf8.ValidString(value) && strings.TrimSpace(value) == value
}
