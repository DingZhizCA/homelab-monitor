/*
Package alerts evaluates one rule against already available metric samples.

Non-fresh or cross-device values become Unknown; evaluation never requests
missing data from an Agent or alters the collection schedule.
*/
package alerts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
)

type Quality string

const (
	QualityFresh       Quality = "fresh"
	QualityStale       Quality = "stale"
	QualityMissing     Quality = "missing"
	QualityUnsupported Quality = "unsupported"
)

type MetricSample struct {
	DeviceID string
	Value    float64
	Unit     string
	Quality  Quality
	Labels   map[string]string
}

type Samples map[string]MetricSample

// SampleKey returns the deterministic Samples key for one metric series.
// Dimensionless samples retain the descriptor ID key used by earlier callers.
func SampleKey(descriptorID string, labels map[string]string) string {
	if len(labels) == 0 {
		return descriptorID
	}
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var key strings.Builder
	key.WriteString(descriptorID)
	key.WriteByte(0)
	for _, label := range keys {
		fmt.Fprintf(&key, "%d:%s%d:%s", len(label), label, len(labels[label]), labels[label])
	}
	return key.String()
}

type State string

const (
	StateTrue     State = "true"
	StateFalse    State = "false"
	StateUnknown  State = "unknown"
	StateDisabled State = "disabled"
)

type EvaluationResult struct {
	RuleID   string   `json:"rule_id"`
	Revision uint64   `json:"revision"`
	State    State    `json:"state"`
	Reasons  []string `json:"reasons,omitempty"`
}

type Evaluator struct {
	registry Registry
	limits   Limits
}

func NewEvaluator(registry Registry, limits Limits) (*Evaluator, error) {
	if registry == nil || limits.MaxDepth < 1 || limits.MaxConditions < 1 {
		return nil, errors.New("metric registry and positive expression limits are required")
	}
	return &Evaluator{registry: registry, limits: limits}, nil
}

func (e *Evaluator) Evaluate(ctx context.Context, rule Rule, samples Samples) (EvaluationResult, error) {
	if ctx == nil {
		return EvaluationResult{}, errors.New("evaluation context is required")
	}
	if err := ctx.Err(); err != nil {
		return EvaluationResult{}, err
	}
	if err := rule.Validate(e.registry, e.limits); err != nil {
		return EvaluationResult{}, fmt.Errorf("invalid alert rule: %w", err)
	}
	if !rule.Enabled {
		return EvaluationResult{RuleID: rule.ID, Revision: rule.Revision, State: StateDisabled}, nil
	}
	state, reasons := evaluateExpression(rule.Expression, rule.DeviceID, samples)
	return EvaluationResult{RuleID: rule.ID, Revision: rule.Revision, State: state, Reasons: reasons}, nil
}

func evaluateExpression(expression Expression, deviceID string, samples Samples) (State, []string) {
	if expression.Metric != nil {
		comparison := *expression.Metric
		sample, ok := samples[SampleKey(comparison.DescriptorID, comparison.Labels)]
		if !ok || !sameLabels(sample.Labels, comparison.Labels) || sample.Quality != QualityFresh || sample.Unit != comparison.Unit || sample.DeviceID != deviceID || math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			reason := "metric value is missing, stale, unsupported, invalid, or belongs to another device"
			if ok && sample.Quality != "" {
				reason = "metric quality is " + string(sample.Quality)
			}
			return StateUnknown, []string{comparison.DescriptorID + ": " + reason}
		}
		return compare(sample.Value, comparison.Operator, comparison.Threshold), nil
	}
	children := expression.All
	isAll := len(expression.All) > 0
	if len(expression.Any) > 0 {
		children = expression.Any
	}
	unknown := false
	reasons := make([]string, 0)
	for _, child := range children {
		state, childReasons := evaluateExpression(child, deviceID, samples)
		if isAll && state == StateFalse {
			return StateFalse, nil
		}
		if !isAll && state == StateTrue {
			return StateTrue, nil
		}
		if state == StateUnknown {
			unknown = true
			reasons = append(reasons, childReasons...)
		}
	}
	if unknown {
		return StateUnknown, reasons
	}
	if isAll {
		return StateTrue, nil
	}
	return StateFalse, nil
}

func sameLabels(actual, expected map[string]string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func compare(value float64, operator Operator, threshold float64) State {
	switch operator {
	case OperatorGreaterThan:
		if value > threshold {
			return StateTrue
		}
	case OperatorGreaterThanOrEqual:
		if value >= threshold {
			return StateTrue
		}
	case OperatorLessThan:
		if value < threshold {
			return StateTrue
		}
	case OperatorLessThanOrEqual:
		if value <= threshold {
			return StateTrue
		}
	}
	return StateFalse
}
