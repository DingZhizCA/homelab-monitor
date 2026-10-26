/*
Package notifications verifies that scheduled processing applies its timeout
and reports one bounded pass without creating overlapping work.
*/
package notifications

import (
	"context"
	"testing"
	"time"
)

func TestEngineRunOnceReportsProcessorResult(t *testing.T) {
	clock := time.Date(2026, 10, 26, 20, 0, 0, 0, time.UTC)
	outbox := &processorOutbox{}
	processor, err := NewProcessor(outbox, &processorHandler{}, 1, RetryPolicy{MaxAttempts: 1, InitialBackoff: time.Second, MaxBackoff: time.Second, MaxErrorBytes: 20}, func(ProcessObservation) {}, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	var observed EngineObservation
	engine, err := NewEngine(processor, EngineConfig{Interval: time.Minute, Timeout: time.Second}, func(value EngineObservation) { observed = value }, func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	if result := engine.RunOnce(context.Background()); result != (ProcessResult{}) || observed.Result != result || observed.Err != nil {
		t.Fatalf("result=%+v observation=%+v", result, observed)
	}
}
