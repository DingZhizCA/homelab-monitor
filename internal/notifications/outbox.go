/*
Package notifications defines the durable handoff boundary between Incident
evaluation and later notification processing. A consumer acknowledges an
intent only after its idempotent downstream effect has committed.
*/
package notifications

import (
	"context"
	"time"
)

// PendingIntent carries the persisted failure count needed to plan the next attempt.
type PendingIntent struct {
	Intent       Intent
	AttemptCount int
}

// Outbox exposes bounded ready reads, failure transitions, and acknowledgement.
type Outbox interface {
	Pending(ctx context.Context, readyAt time.Time, limit int) ([]PendingIntent, error)
	RecordFailure(ctx context.Context, failure Failure) error
	Acknowledge(ctx context.Context, intentID string) error
}
