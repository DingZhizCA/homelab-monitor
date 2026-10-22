/*
Package alerts tests immutable rule revision storage, compare-and-swap, and copy boundaries.
*/
package alerts

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

func TestMemoryRuleRepositoryStoresImmutableHistoryAndRollbackAsNewRevision(t *testing.T) {
	ctx := context.Background()
	repository, err := NewMemoryRuleRepository(dashboard.NewMetricRegistry(), Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	v1 := repositoryTestRule(1, 80)
	if err := repository.SaveRevision(ctx, v1, 0); err != nil {
		t.Fatalf("create first rule revision: %v", err)
	}
	if err := repository.SaveRevision(ctx, v1, 0); err != nil {
		t.Fatalf("identical first revision retry: %v", err)
	}
	conflictingV1 := v1
	conflictingV1.Severity = SeverityCritical
	if err := repository.SaveRevision(ctx, conflictingV1, 0); !errors.Is(err, ErrRuleVersionConflict) {
		t.Fatalf("same revision replacement error=%v, want conflict", err)
	}
	v2 := repositoryTestRule(2, 90)
	if err := repository.SaveRevision(ctx, v2, 1); err != nil {
		t.Fatalf("create second rule revision: %v", err)
	}
	if err := repository.SaveRevision(ctx, repositoryTestRule(4, 95), 1); !errors.Is(err, ErrRuleVersionConflict) {
		t.Fatalf("stale revision write error=%v, want conflict", err)
	}

	rollback := repositoryTestRule(3, 80)
	if err := repository.SaveRevision(ctx, rollback, 2); err != nil {
		t.Fatalf("rollback should append a new revision: %v", err)
	}
	current, ok, err := repository.Current(ctx, "node/a", "high-cpu")
	if err != nil || !ok || current.Revision != 3 || current.Expression.Metric.Threshold != 80 {
		t.Fatalf("current after rollback=%+v found=%t err=%v", current, ok, err)
	}
	old, ok, err := repository.Revision(ctx, "node/a", "high-cpu", 2)
	if err != nil || !ok || old.Expression.Metric.Threshold != 90 {
		t.Fatalf("historical revision 2=%+v found=%t err=%v", old, ok, err)
	}
	if listed, err := repository.ListCurrent(ctx, "node/a"); err != nil || len(listed) != 1 || listed[0].Revision != 3 {
		t.Fatalf("current rule list=%+v err=%v", listed, err)
	}
	history, err := repository.ListRevisions(ctx, "node/a", "high-cpu")
	if err != nil || len(history) != 3 || history[0].Revision != 3 || history[2].Revision != 1 {
		t.Fatalf("rule revision history=%+v err=%v", history, err)
	}
}

func TestMemoryRuleRepositoryCompareAndSwapIsAtomic(t *testing.T) {
	ctx := context.Background()
	repository, err := NewMemoryRuleRepository(dashboard.NewMetricRegistry(), Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveRevision(ctx, repositoryTestRule(1, 80), 0); err != nil {
		t.Fatal(err)
	}
	updates := []Rule{repositoryTestRule(2, 90), repositoryTestRule(3, 95)}
	results := make(chan error, len(updates))
	var group sync.WaitGroup
	for _, rule := range updates {
		group.Add(1)
		go func(rule Rule) {
			defer group.Done()
			results <- repository.SaveRevision(ctx, rule, 1)
		}(rule)
	}
	group.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRuleVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent save error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent writes: successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestMemoryRuleRepositoryDetachesInputAndReturnedExpressions(t *testing.T) {
	ctx := context.Background()
	repository, err := NewMemoryRuleRepository(dashboard.NewMetricRegistry(), Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	rule := repositoryTestRule(1, 80)
	rule.Expression.Metric.DescriptorID = "network.rx_bytes_per_second"
	rule.Expression.Metric.Unit = "bytes_per_second"
	rule.Expression.Metric.Labels = map[string]string{"interface": "eth0"}
	if err := repository.SaveRevision(ctx, rule, 0); err != nil {
		t.Fatal(err)
	}
	rule.Expression.Metric.Labels["interface"] = "mutated input"
	loaded, ok, err := repository.Current(ctx, "node/a", "high-cpu")
	if err != nil || !ok {
		t.Fatalf("load stored rule: found=%t err=%v", ok, err)
	}
	if loaded.Expression.Metric.Labels["interface"] != "eth0" {
		t.Fatal("input mutation changed stored rule")
	}
	loaded.Expression.Metric.Labels["interface"] = "mutated result"
	loaded, _, _ = repository.Current(ctx, "node/a", "high-cpu")
	if loaded.Expression.Metric.Labels["interface"] != "eth0" {
		t.Fatal("returned rule mutation changed stored rule")
	}
}

func TestMemoryRuleRepositoryRejectsInvalidRulesAndContexts(t *testing.T) {
	if _, err := NewMemoryRuleRepository(nil, Limits{MaxDepth: 1, MaxConditions: 1}); err == nil {
		t.Fatal("repository without a metric registry was accepted")
	}
	repository, err := NewMemoryRuleRepository(dashboard.NewMetricRegistry(), Limits{MaxDepth: 1, MaxConditions: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveRevision(nil, repositoryTestRule(1, 80), 0); err == nil {
		t.Fatal("nil context was accepted")
	}
	invalid := repositoryTestRule(1, 80)
	invalid.Expression.Metric.Threshold = math.Inf(1)
	if err := repository.SaveRevision(context.Background(), invalid, 0); err == nil {
		t.Fatal("invalid rule was stored")
	}
	if _, ok, err := repository.Current(context.Background(), "node/a", "high-cpu"); err != nil || ok {
		t.Fatalf("rejected rule changed repository: found=%t err=%v", ok, err)
	}
}

func repositoryTestRule(revision uint64, threshold float64) Rule {
	return Rule{
		ID: "high-cpu", DeviceID: "node/a", Revision: revision,
		Severity: SeverityWarning, Enabled: true,
		Expression: Expression{Metric: &MetricComparison{
			DescriptorID: "cpu.usage_percent", Operator: OperatorGreaterThan,
			Threshold: threshold, Unit: "percent",
		}},
	}
}
