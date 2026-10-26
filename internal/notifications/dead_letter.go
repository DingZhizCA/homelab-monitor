/*
Package notifications defines the inspectable terminal failure record and the
storage boundary for explicit operator requeue and bounded retention cleanup.
*/
package notifications

import (
	"context"
	"errors"
	"strings"
	"time"
)

type DeadLetter struct {
	Intent       Intent    `json:"intent"`
	AttemptCount int       `json:"attempt_count"`
	FailedAt     time.Time `json:"failed_at"`
	LastError    string    `json:"last_error"`
}

func (d DeadLetter) Validate() error {
	if ValidateIntent(d.Intent) != nil || d.AttemptCount < 1 || d.FailedAt.IsZero() || strings.TrimSpace(d.LastError) == "" {
		return errors.New("notification dead letter is invalid")
	}
	return nil
}

type DeadLetterStore interface {
	ListDeadLetters(ctx context.Context, limit int) ([]DeadLetter, error)
	RequeueDeadLetter(ctx context.Context, intentID string) error
	DeleteDeadLettersBefore(ctx context.Context, cutoff time.Time, limit int) (int, error)
}
