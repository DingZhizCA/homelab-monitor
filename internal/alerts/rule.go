/*
Package alerts defines bounded, declarative metric alert rules.

Rules contain comparisons and AND/OR groups only; they cannot execute code,
schedule collection, or select implicit threshold defaults.
*/
package alerts

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarning  Severity = "warning"
	SeverityInfo     Severity = "info"
)

type Operator string

const (
	OperatorGreaterThan        Operator = "gt"
	OperatorGreaterThanOrEqual Operator = "gte"
	OperatorLessThan           Operator = "lt"
	OperatorLessThanOrEqual    Operator = "lte"
)

type Rule struct {
	ID         string     `json:"id"`
	DeviceID   string     `json:"device_id"`
	Revision   uint64     `json:"revision"`
	Severity   Severity   `json:"severity"`
	Enabled    bool       `json:"enabled"`
	Expression Expression `json:"expression"`
}

type Expression struct {
	Metric *MetricComparison `json:"metric,omitempty"`
	All    []Expression      `json:"all,omitempty"`
	Any    []Expression      `json:"any,omitempty"`
}

type MetricComparison struct {
	DescriptorID string            `json:"descriptor_id"`
	Labels       map[string]string `json:"labels,omitempty"`
	Operator     Operator          `json:"operator"`
	Threshold    float64           `json:"threshold"`
	Unit         string            `json:"unit"`
}

type Limits struct {
	MaxDepth      int
	MaxConditions int
}

type Registry interface {
	Lookup(string) (dashboard.MetricDescriptor, bool)
}

var ruleIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

func (rule Rule) Validate(registry Registry, limits Limits) error {
	if registry == nil || limits.MaxDepth < 1 || limits.MaxConditions < 1 {
		return errors.New("metric registry and positive expression limits are required")
	}
	if !ruleIDPattern.MatchString(rule.ID) || strings.TrimSpace(rule.DeviceID) == "" || strings.TrimSpace(rule.DeviceID) != rule.DeviceID || !utf8.ValidString(rule.DeviceID) || rule.Revision == 0 {
		return errors.New("valid rule ID, trimmed UTF-8 device ID, and positive revision are required")
	}
	if rule.Severity != SeverityCritical && rule.Severity != SeverityWarning && rule.Severity != SeverityInfo {
		return errors.New("rule severity is unsupported")
	}
	count := 0
	if err := validateExpression(rule.Expression, 1, &count, registry, limits); err != nil {
		return err
	}
	return nil
}

func validateExpression(expression Expression, depth int, count *int, registry Registry, limits Limits) error {
	if depth > limits.MaxDepth {
		return errors.New("rule expression exceeds maximum depth")
	}
	branches := 0
	if expression.Metric != nil {
		branches++
	}
	if len(expression.All) > 0 {
		branches++
	}
	if len(expression.Any) > 0 {
		branches++
	}
	if branches != 1 {
		return errors.New("each expression must contain exactly one metric, all, or any node")
	}
	if expression.Metric != nil {
		*count++
		if *count > limits.MaxConditions {
			return errors.New("rule expression exceeds maximum condition count")
		}
		return validateComparison(*expression.Metric, registry)
	}
	children := expression.All
	if len(expression.Any) > 0 {
		children = expression.Any
	}
	if len(children) == 0 {
		return errors.New("logical groups must contain at least one expression")
	}
	for _, child := range children {
		if err := validateExpression(child, depth+1, count, registry, limits); err != nil {
			return err
		}
	}
	return nil
}

func validateComparison(comparison MetricComparison, registry Registry) error {
	descriptor, ok := registry.Lookup(comparison.DescriptorID)
	if !ok {
		return fmt.Errorf("metric descriptor %q is unsupported", comparison.DescriptorID)
	}
	if len(comparison.Labels) != len(descriptor.Dimensions) {
		return fmt.Errorf("metric descriptor %q requires exactly %d dimension selectors", descriptor.ID, len(descriptor.Dimensions))
	}
	for _, dimension := range descriptor.Dimensions {
		value, exists := comparison.Labels[dimension]
		if !exists || !utf8.ValidString(value) {
			return fmt.Errorf("metric descriptor %q requires a valid selector for dimension %q", descriptor.ID, dimension)
		}
	}
	for label := range comparison.Labels {
		if !slices.Contains(descriptor.Dimensions, label) {
			return fmt.Errorf("metric descriptor %q does not declare dimension %q", descriptor.ID, label)
		}
	}
	if comparison.Unit == "" || comparison.Unit != descriptor.Unit {
		return fmt.Errorf("metric descriptor %q requires unit %q", descriptor.ID, descriptor.Unit)
	}
	if math.IsNaN(comparison.Threshold) || math.IsInf(comparison.Threshold, 0) {
		return errors.New("metric threshold must be finite")
	}
	switch comparison.Operator {
	case OperatorGreaterThan, OperatorGreaterThanOrEqual, OperatorLessThan, OperatorLessThanOrEqual:
		return nil
	default:
		return errors.New("metric comparison operator is unsupported")
	}
}
