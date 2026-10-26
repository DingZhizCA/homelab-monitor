/*
Package notifications defines deterministic retry decisions for failed intent
handling. Persistence enforces the decision atomically with the outbox row.
*/
package notifications

import (
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

var ErrDeadLetterCapacity = errors.New("notification dead letter capacity reached")

type RetryPolicy struct {
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	MaxErrorBytes  int
}

func (p RetryPolicy) Validate() error {
	if p.MaxAttempts < 1 || p.InitialBackoff <= 0 || p.MaxBackoff < p.InitialBackoff || p.MaxErrorBytes < 1 {
		return errors.New("notification retry policy requires positive ordered limits")
	}
	return nil
}

// Failure is the complete next state after one failed handler attempt.
type Failure struct {
	IntentID     string
	AttemptCount int
	FailedAt     time.Time
	NextAttempt  time.Time
	LastError    string
	DeadLetter   bool
}

func (f Failure) Validate() error {
	if ValidateIntentID(f.IntentID) != nil || f.AttemptCount < 1 || f.FailedAt.IsZero() || strings.TrimSpace(f.LastError) == "" {
		return errors.New("notification failure is invalid")
	}
	if f.DeadLetter && !f.NextAttempt.IsZero() {
		return errors.New("dead-letter notification failure cannot have a next attempt")
	}
	if !f.DeadLetter && f.NextAttempt.IsZero() {
		return errors.New("deferred notification failure requires a next attempt")
	}
	return nil
}

// PlanFailure applies capped exponential backoff without relying on product defaults.
func PlanFailure(intentID string, priorAttempts int, failedAt time.Time, cause error, policy RetryPolicy) (Failure, error) {
	if err := policy.Validate(); err != nil {
		return Failure{}, err
	}
	if ValidateIntentID(intentID) != nil || priorAttempts < 0 || failedAt.IsZero() || cause == nil {
		return Failure{}, errors.New("notification failure input is invalid")
	}
	attempts := priorAttempts + 1
	failure := Failure{IntentID: intentID, AttemptCount: attempts, FailedAt: failedAt.UTC(), LastError: boundedError(cause.Error(), policy.MaxErrorBytes)}
	if attempts >= policy.MaxAttempts {
		failure.DeadLetter = true
		return failure, nil
	}
	backoff := policy.InitialBackoff
	for step := 1; step < attempts && backoff < policy.MaxBackoff; step++ {
		if backoff > policy.MaxBackoff/2 {
			backoff = policy.MaxBackoff
		} else {
			backoff *= 2
		}
	}
	if backoff > policy.MaxBackoff {
		backoff = policy.MaxBackoff
	}
	failure.NextAttempt = failure.FailedAt.Add(backoff)
	return failure, nil
}

func boundedError(message string, maxBytes int) string {
	message = strings.Join(strings.Fields(message), " ")
	if message == "" {
		message = "notification handler failed"
	}
	if len(message) <= maxBytes {
		return message
	}
	message = message[:maxBytes]
	for !utf8.ValidString(message) {
		message = message[:len(message)-1]
	}
	if message == "" {
		return "?"
	}
	return message
}
