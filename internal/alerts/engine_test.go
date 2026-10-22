/*
Package alerts tests bounded scheduling, rule failure isolation, and shutdown.
*/
package alerts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

type fixedSamples Samples

func (samples fixedSamples) Samples(context.Context, string) (Samples, error) {
	return Samples(samples), nil
}

func TestEngineBoundsWorkAndIsolatesRevisionFailure(t *testing.T) {
	registry := dashboard.NewMetricRegistry()
	limits := Limits{MaxDepth: 2, MaxConditions: 2}
	rules, err := NewMemoryRuleRepository(registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	blockedV1 := engineRule("a-blocked", 1)
	for _, rule := range []Rule{blockedV1, engineRule("b-ok", 1), engineRule("c-limited", 1)} {
		if err := rules.SaveRevision(context.Background(), rule, 0); err != nil {
			t.Fatal(err)
		}
	}
	blockedV2 := engineRule("a-blocked", 2)
	if err := rules.SaveRevision(context.Background(), blockedV2, 1); err != nil {
		t.Fatal(err)
	}
	incidents, err := NewMemoryIncidentRepository(4)
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Duration(0)
	policy := IncidentPolicy{PendingFor: &zero, RecoveryFor: &zero, UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold}
	base := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	if _, err := incidents.Advance(context.Background(), blockedV1, EvaluationResult{RuleID: blockedV1.ID, Revision: 1, State: StateTrue}, base, policy, time.Time{}); err != nil {
		t.Fatal(err)
	}
	evaluator, err := NewEvaluator(registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	observations := make([]EngineObservation, 0)
	engine, err := NewEngine(fixedSamples{
		"cpu.usage_percent": {DeviceID: "node-a", Value: 90, Unit: "percent", Quality: QualityFresh},
	}, rules, incidents, evaluator, EngineConfig{Interval: time.Hour, Timeout: time.Second, MaxRulesPerRun: 2, IncidentPolicy: policy}, func(observation EngineObservation) {
		observations = append(observations, observation)
	})
	if err != nil {
		t.Fatal(err)
	}
	engine.now = func() time.Time { return base.Add(time.Second) }
	summary := engine.RunOnce(context.Background())
	if summary.Attempted != 2 || summary.Succeeded != 1 || summary.Failed != 1 || !summary.LimitReached {
		t.Fatalf("summary=%+v", summary)
	}
	if len(observations) != 2 || !errors.Is(observations[0].Err, ErrIncidentRevision) || observations[1].RuleID != "b-ok" || observations[1].Incident != IncidentFiring {
		t.Fatalf("observations=%+v", observations)
	}
	if _, found, err := incidents.Current(context.Background(), "node-a", "c-limited"); err != nil || found {
		t.Fatalf("limited rule state found=%t err=%v", found, err)
	}
	second := engine.RunOnce(context.Background())
	if second.Attempted != 2 || second.Succeeded != 1 || second.Failed != 1 {
		t.Fatalf("fairness run summary=%+v", second)
	}
	if _, found, err := incidents.Current(context.Background(), "node-a", "c-limited"); err != nil || !found {
		t.Fatalf("rotated rule state found=%t err=%v", found, err)
	}
}

func TestEngineRunStopsOnCancellation(t *testing.T) {
	registry := dashboard.NewMetricRegistry()
	limits := Limits{MaxDepth: 1, MaxConditions: 1}
	rules, _ := NewMemoryRuleRepository(registry, limits)
	incidents, _ := NewMemoryIncidentRepository(1)
	evaluator, _ := NewEvaluator(registry, limits)
	zero := time.Duration(0)
	engine, err := NewEngine(fixedSamples{}, rules, incidents, evaluator, EngineConfig{
		Interval: time.Millisecond, Timeout: time.Second, MaxRulesPerRun: 1,
		IncidentPolicy: IncidentPolicy{PendingFor: &zero, RecoveryFor: &zero, UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold},
	}, func(EngineObservation) {})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { engine.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("alert engine did not stop after cancellation")
	}
}

func engineRule(id string, revision uint64) Rule {
	return Rule{
		ID: id, DeviceID: "node-a", Revision: revision, Severity: SeverityWarning, Enabled: true,
		Expression: Expression{Metric: &MetricComparison{DescriptorID: "cpu.usage_percent", Operator: OperatorGreaterThan, Threshold: 80, Unit: "percent"}},
	}
}
