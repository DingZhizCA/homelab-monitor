/*
Package notifications defines the durable handoff boundary between Incident
evaluation and later notification processing. A consumer acknowledges an
intent only after its idempotent downstream effect has committed.
*/
package notifications

import "context"

// Outbox exposes bounded oldest-first reads and idempotent acknowledgement.
type Outbox interface {
	Pending(ctx context.Context, limit int) ([]Intent, error)
	Acknowledge(ctx context.Context, intentID string) error
}
