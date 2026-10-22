/*
Package alerts provides an ephemeral versioned alert-rule repository.

The repository is process-local and intended for development and tests. It
validates rule revisions and compare-and-swap writes but is not durable storage
or an authorization boundary.
*/
package alerts

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
)

const (
	RuleAuditAction       = "alert.rule.save"
	RuleAuditResourceType = "alert_rule"
)

// RuleAuditResourceID identifies one device-scoped rule in audit records.
func RuleAuditResourceID(rule Rule) string {
	return rule.DeviceID + "/" + rule.ID
}

// RuleRepository stores immutable revisions and an independently selected current revision.
type RuleRepository interface {
	Current(context.Context, string, string) (Rule, bool, error)
	Revision(context.Context, string, string, uint64) (Rule, bool, error)
	ListCurrent(context.Context, string) ([]Rule, error)
	SaveRevisionWithAudit(context.Context, string, Rule, uint64, audit.Event) error
}

type ruleKey struct {
	deviceID string
	ruleID   string
}

type versionedRule struct {
	current   uint64
	revisions map[uint64]Rule
}

// MemoryRuleRepository stores alert-rule history in process memory for development and tests.
type MemoryRuleRepository struct {
	mu       sync.RWMutex
	registry Registry
	limits   Limits
	rules    map[ruleKey]*versionedRule
	audits   []audit.Event
}

// NewMemoryRuleRepository requires an explicit metric registry and expression limits.
func NewMemoryRuleRepository(registry Registry, limits Limits) (*MemoryRuleRepository, error) {
	if registry == nil || limits.MaxDepth < 1 || limits.MaxConditions < 1 {
		return nil, errors.New("metric registry and positive expression limits are required")
	}
	return &MemoryRuleRepository{registry: registry, limits: limits, rules: make(map[ruleKey]*versionedRule)}, nil
}

func (r *MemoryRuleRepository) Current(ctx context.Context, deviceID, ruleID string) (Rule, bool, error) {
	if err := checkRuleRepositoryContext(ctx); err != nil {
		return Rule{}, false, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	stored, ok := r.rules[ruleKey{deviceID: deviceID, ruleID: ruleID}]
	if !ok {
		return Rule{}, false, nil
	}
	return cloneRule(stored.revisions[stored.current]), true, nil
}

func (r *MemoryRuleRepository) Revision(ctx context.Context, deviceID, ruleID string, revision uint64) (Rule, bool, error) {
	if err := checkRuleRepositoryContext(ctx); err != nil {
		return Rule{}, false, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	stored, ok := r.rules[ruleKey{deviceID: deviceID, ruleID: ruleID}]
	if !ok {
		return Rule{}, false, nil
	}
	rule, ok := stored.revisions[revision]
	if !ok {
		return Rule{}, false, nil
	}
	return cloneRule(rule), true, nil
}

func (r *MemoryRuleRepository) ListCurrent(ctx context.Context, deviceID string) ([]Rule, error) {
	if err := checkRuleRepositoryContext(ctx); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	rules := make([]Rule, 0)
	for key, stored := range r.rules {
		if key.deviceID == deviceID {
			rules = append(rules, cloneRule(stored.revisions[stored.current]))
		}
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	return rules, nil
}

// SaveRevision atomically appends an immutable revision when expectedCurrent matches.
func (r *MemoryRuleRepository) SaveRevision(ctx context.Context, rule Rule, expectedCurrent uint64) error {
	return r.saveRevision(ctx, rule, expectedCurrent, nil)
}

// SaveRevisionWithAudit atomically appends a rule revision and its management event.
func (r *MemoryRuleRepository) SaveRevisionWithAudit(ctx context.Context, actorID string, rule Rule, expectedCurrent uint64, event audit.Event) error {
	if event.ActorID != actorID {
		return errors.New("alert-rule audit actor does not match mutation actor")
	}
	return r.saveRevision(ctx, rule, expectedCurrent, &event)
}

func (r *MemoryRuleRepository) saveRevision(ctx context.Context, rule Rule, expectedCurrent uint64, event *audit.Event) error {
	if err := checkRuleRepositoryContext(ctx); err != nil {
		return err
	}
	if err := rule.Validate(r.registry, r.limits); err != nil {
		return fmt.Errorf("invalid alert rule: %w", err)
	}
	if event != nil {
		if err := validateRuleAudit(*event, rule); err != nil {
			return err
		}
	}
	key := ruleKey{deviceID: rule.DeviceID, ruleID: rule.ID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRuleRepositoryContext(ctx); err != nil {
		return err
	}
	stored, exists := r.rules[key]
	if !exists {
		if expectedCurrent != 0 {
			return ErrRuleVersionConflict
		}
		r.rules[key] = &versionedRule{current: rule.Revision, revisions: map[uint64]Rule{rule.Revision: cloneRule(rule)}}
		if event != nil {
			r.audits = append(r.audits, *event)
		}
		return nil
	}
	if existing, duplicate := stored.revisions[rule.Revision]; duplicate {
		if equalRule(existing, rule) {
			return nil
		}
		return ErrRuleVersionConflict
	}
	if stored.current != expectedCurrent {
		return ErrRuleVersionConflict
	}
	if rule.Revision <= expectedCurrent {
		return ErrRuleVersionConflict
	}
	stored.revisions[rule.Revision] = cloneRule(rule)
	stored.current = rule.Revision
	if event != nil {
		r.audits = append(r.audits, *event)
	}
	return nil
}

func validateRuleAudit(event audit.Event, rule Rule) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate alert-rule audit event: %w", err)
	}
	if event.Action != RuleAuditAction || event.ResourceType != RuleAuditResourceType || event.ResourceID != RuleAuditResourceID(rule) {
		return errors.New("alert-rule audit event does not match mutation")
	}
	return nil
}

// AuditEvents returns a copy of development-only in-memory management events.
func (r *MemoryRuleRepository) AuditEvents() []audit.Event {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]audit.Event(nil), r.audits...)
}

func checkRuleRepositoryContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("alert rule repository context is required")
	}
	return ctx.Err()
}

func cloneRule(rule Rule) Rule {
	return Rule{
		ID: rule.ID, DeviceID: rule.DeviceID, Revision: rule.Revision,
		Severity: rule.Severity, Enabled: rule.Enabled, Expression: cloneExpression(rule.Expression),
	}
}

func cloneExpression(expression Expression) Expression {
	cloned := Expression{}
	if expression.Metric != nil {
		comparison := *expression.Metric
		if comparison.Labels != nil {
			comparison.Labels = make(map[string]string, len(expression.Metric.Labels))
			for key, value := range expression.Metric.Labels {
				comparison.Labels[key] = value
			}
		}
		cloned.Metric = &comparison
	}
	if expression.All != nil {
		cloned.All = make([]Expression, len(expression.All))
		for index := range expression.All {
			cloned.All[index] = cloneExpression(expression.All[index])
		}
	}
	if expression.Any != nil {
		cloned.Any = make([]Expression, len(expression.Any))
		for index := range expression.Any {
			cloned.Any[index] = cloneExpression(expression.Any[index])
		}
	}
	return cloned
}

func equalRule(left, right Rule) bool {
	if left.ID != right.ID || left.DeviceID != right.DeviceID || left.Revision != right.Revision || left.Severity != right.Severity || left.Enabled != right.Enabled {
		return false
	}
	return equalExpression(left.Expression, right.Expression)
}

func equalExpression(left, right Expression) bool {
	if (left.Metric == nil) != (right.Metric == nil) || len(left.All) != len(right.All) || len(left.Any) != len(right.Any) {
		return false
	}
	if left.Metric != nil {
		a, b := left.Metric, right.Metric
		if a.DescriptorID != b.DescriptorID || a.Operator != b.Operator || a.Threshold != b.Threshold || a.Unit != b.Unit || len(a.Labels) != len(b.Labels) {
			return false
		}
		for key, value := range a.Labels {
			if other, ok := b.Labels[key]; !ok || other != value {
				return false
			}
		}
	}
	for index := range left.All {
		if !equalExpression(left.All[index], right.All[index]) {
			return false
		}
	}
	for index := range left.Any {
		if !equalExpression(left.Any[index], right.Any[index]) {
			return false
		}
	}
	return true
}

var ErrRuleVersionConflict = errors.New("alert rule revision conflict")

var _ RuleRepository = (*MemoryRuleRepository)(nil)
