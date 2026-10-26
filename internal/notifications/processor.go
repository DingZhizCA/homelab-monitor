/*
Package notifications processes one explicitly bounded outbox batch. Scheduling
stays with the Server lifecycle, while each downstream handler must make its
effect idempotent by intent ID before the outbox item is acknowledged.
*/
package notifications

import (
	"context"
	"errors"
	"time"
)

type Handler interface {
	Handle(ctx context.Context, intent Intent) error
}

type ProcessResult struct {
	Read         int
	Handled      int
	Deferred     int
	DeadLettered int
	Failed       int
	Acknowledged int
}

type ProcessStage string

const (
	ProcessStageRead        ProcessStage = "read"
	ProcessStageHandle      ProcessStage = "handle"
	ProcessStageDefer       ProcessStage = "defer"
	ProcessStageDeadLetter  ProcessStage = "dead_letter"
	ProcessStageAcknowledge ProcessStage = "acknowledge"
)

type ProcessObservation struct {
	IntentID string
	Stage    ProcessStage
	Err      error
}

type Observer func(ProcessObservation)

// Processor runs deterministic single-pass work without owning a goroutine.
type Processor struct {
	outbox   Outbox
	handler  Handler
	maxBatch int
	observe  Observer
	retry    RetryPolicy
	now      func() time.Time
}

func NewProcessor(outbox Outbox, handler Handler, maxBatch int, retry RetryPolicy, observe Observer, now func() time.Time) (*Processor, error) {
	if outbox == nil || handler == nil || maxBatch < 1 || retry.Validate() != nil || observe == nil || now == nil {
		return nil, errors.New("notification processor requires outbox, handler, positive limits, observer, and clock")
	}
	return &Processor{outbox: outbox, handler: handler, maxBatch: maxBatch, retry: retry, observe: observe, now: now}, nil
}

// RunOnce handles the oldest bounded batch and isolates failures by intent.
func (p *Processor) RunOnce(ctx context.Context) (ProcessResult, error) {
	intents, err := p.outbox.Pending(ctx, p.now().UTC(), p.maxBatch)
	if err != nil {
		p.observe(ProcessObservation{Stage: ProcessStageRead, Err: err})
		return ProcessResult{}, err
	}
	result := ProcessResult{Read: len(intents)}
	for _, item := range intents {
		intent := item.Intent
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := p.handler.Handle(ctx, intent); err != nil {
			p.observe(ProcessObservation{IntentID: intent.ID, Stage: ProcessStageHandle, Err: err})
			failure, planErr := PlanFailure(intent.ID, item.AttemptCount, p.now(), err, p.retry)
			if planErr != nil {
				result.Failed++
				p.observe(ProcessObservation{IntentID: intent.ID, Stage: ProcessStageDefer, Err: planErr})
				continue
			}
			if persistErr := p.outbox.RecordFailure(ctx, failure); persistErr != nil {
				result.Failed++
				p.observe(ProcessObservation{IntentID: intent.ID, Stage: ProcessStageDefer, Err: persistErr})
				continue
			}
			if failure.DeadLetter {
				result.DeadLettered++
				p.observe(ProcessObservation{IntentID: intent.ID, Stage: ProcessStageDeadLetter})
			} else {
				result.Deferred++
				p.observe(ProcessObservation{IntentID: intent.ID, Stage: ProcessStageDefer})
			}
			continue
		}
		result.Handled++
		if err := p.outbox.Acknowledge(ctx, intent.ID); err != nil {
			result.Failed++
			p.observe(ProcessObservation{IntentID: intent.ID, Stage: ProcessStageAcknowledge, Err: err})
			continue
		}
		result.Acknowledged++
		p.observe(ProcessObservation{IntentID: intent.ID, Stage: ProcessStageAcknowledge})
	}
	return result, nil
}
