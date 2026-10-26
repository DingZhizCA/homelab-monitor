/*
Package notifications verifies bounded single-pass processing, per-intent
failure isolation, and acknowledgement only after a successful handler call.
*/
package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
)

func TestProcessorIsolatesHandlerFailureAndAcknowledgesSuccess(t *testing.T) {
	first := processorIntent(t, "a", time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC))
	second := processorIntent(t, "b", first.CreatedAt.Add(time.Second))
	outbox := &processorOutbox{pending: []Intent{first, second}}
	handler := &processorHandler{fail: map[string]error{first.ID: errors.New("sink unavailable")}}
	var observations []ProcessObservation
	processor, err := NewProcessor(outbox, handler, 2, func(observation ProcessObservation) {
		observations = append(observations, observation)
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.RunOnce(context.Background())
	if err != nil || result != (ProcessResult{Read: 2, Handled: 1, Failed: 1, Acknowledged: 1}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if len(handler.handled) != 2 || len(outbox.acknowledged) != 1 || outbox.acknowledged[0] != second.ID {
		t.Fatalf("handled=%v acknowledged=%v", handler.handled, outbox.acknowledged)
	}
	if len(observations) != 2 || observations[0].Stage != ProcessStageHandle || observations[0].Err == nil || observations[1].Stage != ProcessStageAcknowledge || observations[1].Err != nil {
		t.Fatalf("observations=%+v", observations)
	}
}

func TestProcessorLeavesHandledIntentWhenAcknowledgementFails(t *testing.T) {
	intent := processorIntent(t, "a", time.Date(2026, 10, 26, 16, 0, 0, 0, time.UTC))
	outbox := &processorOutbox{pending: []Intent{intent}, acknowledgeErr: errors.New("database busy")}
	processor, err := NewProcessor(outbox, &processorHandler{}, 1, func(ProcessObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	result, err := processor.RunOnce(context.Background())
	if err != nil || result != (ProcessResult{Read: 1, Handled: 1, Failed: 1}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

type processorOutbox struct {
	pending        []Intent
	acknowledged   []string
	acknowledgeErr error
}

func (o *processorOutbox) Pending(context.Context, int) ([]Intent, error) {
	return append([]Intent(nil), o.pending...), nil
}

func (o *processorOutbox) Acknowledge(_ context.Context, id string) error {
	if o.acknowledgeErr != nil {
		return o.acknowledgeErr
	}
	o.acknowledged = append(o.acknowledged, id)
	return nil
}

type processorHandler struct {
	fail    map[string]error
	handled []string
}

func (h *processorHandler) Handle(_ context.Context, intent Intent) error {
	h.handled = append(h.handled, intent.ID)
	return h.fail[intent.ID]
}

func processorIntent(t *testing.T, suffix string, at time.Time) Intent {
	t.Helper()
	transitionID := intentID("transition-"+suffix, IntentFiring)
	intent := Intent{
		TransitionID:   transitionID,
		DeviceID:       "node-a",
		RuleID:         "high-cpu",
		RuleRevision:   1,
		Severity:       alerts.SeverityWarning,
		Kind:           IntentFiring,
		DeliveryPolicy: DeliveryPushEligible,
		CreatedAt:      at,
	}
	intent.ID = intentID(intent.TransitionID, intent.Kind)
	if err := ValidateIntent(intent); err != nil {
		t.Fatal(err)
	}
	return intent
}
