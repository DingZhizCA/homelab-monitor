/*
Package notifications processes one explicitly bounded outbox batch. Scheduling
stays with the Server lifecycle, while each downstream handler must make its
effect idempotent by intent ID before the outbox item is acknowledged.
*/
package notifications

import (
	"context"
	"errors"
)

type Handler interface {
	Handle(ctx context.Context, intent Intent) error
}

type ProcessResult struct {
	Read         int
	Handled      int
	Failed       int
	Acknowledged int
}

type ProcessStage string

const (
	ProcessStageRead        ProcessStage = "read"
	ProcessStageHandle      ProcessStage = "handle"
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
}

func NewProcessor(outbox Outbox, handler Handler, maxBatch int, observe Observer) (*Processor, error) {
	if outbox == nil || handler == nil || maxBatch < 1 || observe == nil {
		return nil, errors.New("notification outbox, handler, positive batch limit, and observer are required")
	}
	return &Processor{outbox: outbox, handler: handler, maxBatch: maxBatch, observe: observe}, nil
}

// RunOnce handles the oldest bounded batch and isolates failures by intent.
func (p *Processor) RunOnce(ctx context.Context) (ProcessResult, error) {
	intents, err := p.outbox.Pending(ctx, p.maxBatch)
	if err != nil {
		p.observe(ProcessObservation{Stage: ProcessStageRead, Err: err})
		return ProcessResult{}, err
	}
	result := ProcessResult{Read: len(intents)}
	for _, intent := range intents {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if err := p.handler.Handle(ctx, intent); err != nil {
			result.Failed++
			p.observe(ProcessObservation{IntentID: intent.ID, Stage: ProcessStageHandle, Err: err})
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
