/*
Package alerts tests rule validation and three-valued evaluation semantics.

No test loads a default threshold, writes a rule, or starts a collector.
*/
package alerts

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

func TestEvaluatorUsesThreeValuedANDORLogic(t *testing.T) {
	evaluator, err := NewEvaluator(dashboard.NewMetricRegistry(), Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	base := Rule{ID: "high-cpu", DeviceID: "node-a", Revision: 1, Severity: SeverityWarning, Enabled: true}
	cpu := Expression{Metric: &MetricComparison{DescriptorID: "cpu.usage_percent", Operator: OperatorGreaterThan, Threshold: 80, Unit: "percent"}}
	memory := Expression{Metric: &MetricComparison{DescriptorID: "memory.usage_percent", Operator: OperatorGreaterThan, Threshold: 90, Unit: "percent"}}
	unknown := Samples{"cpu.usage_percent": {DeviceID: "node-a", Quality: QualityStale, Unit: "percent", Value: 99}}

	cases := []struct {
		name       string
		expression Expression
		samples    Samples
		want       State
	}{
		{"and unknown", Expression{All: []Expression{cpu, memory}}, unknown, StateUnknown},
		{"and false dominates unknown", Expression{All: []Expression{cpu, memory}}, Samples{
			"cpu.usage_percent":    {DeviceID: "node-a", Quality: QualityFresh, Unit: "percent", Value: 20},
			"memory.usage_percent": {DeviceID: "node-a", Quality: QualityStale, Unit: "percent", Value: 99},
		}, StateFalse},
		{"or true dominates unknown", Expression{Any: []Expression{cpu, memory}}, Samples{
			"cpu.usage_percent":    {DeviceID: "node-a", Quality: QualityFresh, Unit: "percent", Value: 95},
			"memory.usage_percent": {DeviceID: "node-a", Quality: QualityMissing, Unit: "percent", Value: 0},
		}, StateTrue},
		{"or false and unknown", Expression{Any: []Expression{cpu, memory}}, Samples{
			"cpu.usage_percent": {DeviceID: "node-a", Quality: QualityFresh, Unit: "percent", Value: 20},
		}, StateUnknown},
		{"fresh false", cpu, Samples{"cpu.usage_percent": {DeviceID: "node-a", Quality: QualityFresh, Unit: "percent", Value: 20}}, StateFalse},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			rule := base
			rule.Expression = test.expression
			result, err := evaluator.Evaluate(context.Background(), rule, test.samples)
			if err != nil {
				t.Fatal(err)
			}
			if result.State != test.want {
				t.Fatalf("state=%q, want %q; reasons=%v", result.State, test.want, result.Reasons)
			}
		})
	}
}

func TestEvaluatorRejectsUnboundedOrInvalidRuleExpressions(t *testing.T) {
	registry := dashboard.NewMetricRegistry()
	limits := Limits{MaxDepth: 2, MaxConditions: 2}
	evaluator, err := NewEvaluator(registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	leaf := func(threshold float64) Expression {
		return Expression{Metric: &MetricComparison{DescriptorID: "cpu.usage_percent", Operator: OperatorGreaterThan, Threshold: threshold, Unit: "percent"}}
	}
	base := Rule{ID: "cpu-rule", DeviceID: "node-a", Revision: 1, Severity: SeverityCritical, Enabled: true}
	cases := []struct {
		name string
		rule Rule
	}{
		{"too deep", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: Expression{All: []Expression{{Any: []Expression{leaf(1)}}}}}},
		{"too many leaves", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: Expression{All: []Expression{leaf(1), leaf(2), leaf(3)}}}},
		{"unknown metric", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: Expression{Metric: &MetricComparison{DescriptorID: "no.such.metric", Operator: OperatorGreaterThan, Threshold: 1, Unit: "x"}}}},
		{"dimensioned metric without selector", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: Expression{Metric: &MetricComparison{DescriptorID: "network.rx_bytes_per_second", Operator: OperatorGreaterThan, Threshold: 1, Unit: "bytes_per_second"}}}},
		{"undeclared selector", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: Expression{Metric: &MetricComparison{DescriptorID: "network.rx_bytes_per_second", Labels: map[string]string{"interface": "eth0", "unknown": "x"}, Operator: OperatorGreaterThan, Threshold: 1, Unit: "bytes_per_second"}}}},
		{"invalid selector value", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: Expression{Metric: &MetricComparison{DescriptorID: "network.rx_bytes_per_second", Labels: map[string]string{"interface": string([]byte{0xff})}, Operator: OperatorGreaterThan, Threshold: 1, Unit: "bytes_per_second"}}}},
		{"unit mismatch", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: Expression{Metric: &MetricComparison{DescriptorID: "cpu.usage_percent", Operator: OperatorGreaterThan, Threshold: 1, Unit: "bytes"}}}},
		{"non-finite threshold", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: leaf(math.Inf(1))}},
		{"unsupported operator", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: Expression{Metric: &MetricComparison{DescriptorID: "cpu.usage_percent", Operator: "eq", Threshold: 1, Unit: "percent"}}}},
		{"mixed expression node", Rule{ID: base.ID, DeviceID: base.DeviceID, Revision: base.Revision, Severity: base.Severity, Enabled: true, Expression: Expression{Metric: leaf(1).Metric, Any: []Expression{leaf(2)}}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := evaluator.Evaluate(context.Background(), test.rule, nil); err == nil {
				t.Fatal("invalid rule was accepted")
			}
		})
	}
}

func TestEvaluatorSelectsTheExactMetricDimension(t *testing.T) {
	evaluator, err := NewEvaluator(dashboard.NewMetricRegistry(), Limits{MaxDepth: 3, MaxConditions: 4})
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]string{"interface": "eth0"}
	comparison := &MetricComparison{
		DescriptorID: "network.rx_bytes_per_second", Labels: labels,
		Operator: OperatorGreaterThan, Threshold: 100, Unit: "bytes_per_second",
	}
	rule := Rule{ID: "eth0-rx", DeviceID: "node-a", Revision: 1, Severity: SeverityWarning, Enabled: true, Expression: Expression{Metric: comparison}}
	eth1 := MetricSample{DeviceID: "node-a", Quality: QualityFresh, Unit: comparison.Unit, Value: 500, Labels: map[string]string{"interface": "eth1"}}
	eth0 := MetricSample{DeviceID: "node-a", Quality: QualityFresh, Unit: comparison.Unit, Value: 150, Labels: map[string]string{"interface": "eth0"}}
	if SampleKey("metric", map[string]string{"a": "1", "b": "2"}) != SampleKey("metric", map[string]string{"b": "2", "a": "1"}) {
		t.Fatal("sample keys depend on map iteration order")
	}
	result, err := evaluator.Evaluate(context.Background(), rule, Samples{
		SampleKey(comparison.DescriptorID, eth1.Labels): eth1,
		SampleKey(comparison.DescriptorID, eth0.Labels): eth0,
	})
	if err != nil || result.State != StateTrue {
		t.Fatalf("selected interface result=%+v err=%v", result, err)
	}
	result, err = evaluator.Evaluate(context.Background(), rule, Samples{SampleKey(comparison.DescriptorID, eth1.Labels): eth1})
	if err != nil || result.State != StateUnknown || !strings.Contains(strings.Join(result.Reasons, " "), comparison.DescriptorID) {
		t.Fatalf("missing selected interface result=%+v err=%v", result, err)
	}
	wrongKey := eth1
	result, err = evaluator.Evaluate(context.Background(), rule, Samples{SampleKey(comparison.DescriptorID, labels): wrongKey})
	if err != nil || result.State != StateUnknown {
		t.Fatalf("mismatched sample labels result=%+v err=%v", result, err)
	}
}

func TestEvaluatorRequiresMatchingFreshDeviceSampleAndSkipsDisabledRule(t *testing.T) {
	evaluator, err := NewEvaluator(dashboard.NewMetricRegistry(), Limits{MaxDepth: 3, MaxConditions: 4})
	if err != nil {
		t.Fatal(err)
	}
	rule := Rule{
		ID: "load-rule", DeviceID: "node-a", Revision: 7, Severity: SeverityInfo, Enabled: true,
		Expression: Expression{Metric: &MetricComparison{DescriptorID: "system.load1", Operator: OperatorGreaterThanOrEqual, Threshold: 0, Unit: "load"}},
	}
	result, err := evaluator.Evaluate(context.Background(), rule, Samples{"system.load1": {DeviceID: "node-b", Quality: QualityFresh, Unit: "load", Value: 0}})
	if err != nil || result.State != StateUnknown {
		t.Fatalf("cross-device sample result=%+v err=%v", result, err)
	}
	rule.Enabled = false
	result, err = evaluator.Evaluate(context.Background(), rule, nil)
	if err != nil || result.State != StateDisabled || result.Revision != 7 {
		t.Fatalf("disabled rule result=%+v err=%v", result, err)
	}
}

func TestAlertRuleUsesDeviceIdentityRulesInsteadOfRuleIdentifierRules(t *testing.T) {
	registry := dashboard.NewMetricRegistry()
	limits := Limits{MaxDepth: 2, MaxConditions: 2}
	rule := Rule{
		ID: "cpu-rule", DeviceID: "node/a", Revision: 1, Severity: SeverityWarning, Enabled: true,
		Expression: Expression{Metric: &MetricComparison{DescriptorID: "cpu.usage_percent", Operator: OperatorGreaterThan, Threshold: 80, Unit: "percent"}},
	}
	if err := rule.Validate(registry, limits); err != nil {
		t.Fatalf("valid slash-containing device ID rejected: %v", err)
	}
	rule.DeviceID = " node/a "
	if err := rule.Validate(registry, limits); err == nil {
		t.Fatal("device ID with surrounding whitespace was accepted")
	}
}
