/*
Package storage verifies durable alert-rule revisions, CAS, active Owner
checks, idempotent retries, and transactional audit records in core.db.
*/
package storage

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

func TestAlertRuleRepositoryPersistsHistoryAuditAndIdempotentRetries(t *testing.T) {
	databases := openTestDatabases(t)
	ownerID := insertDashboardTestUser(t, databases, "owner", auth.RoleOwner)
	registry := dashboard.NewMetricRegistry()
	repository, err := NewAlertRuleRepository(databases.Core, registry, alerts.Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	v1 := storedRule(1, 80)
	if err := repository.SaveRevisionWithAudit(context.Background(), ownerID, v1, 0, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, v1)); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveRevisionWithAudit(context.Background(), ownerID, v1, 0, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, v1)); err != nil {
		t.Fatalf("identical retry: %v", err)
	}
	v2 := storedRule(2, 90)
	if err := repository.SaveRevisionWithAudit(context.Background(), ownerID, v2, 1, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, v2)); err != nil {
		t.Fatal(err)
	}
	current, found, err := repository.Current(context.Background(), v1.DeviceID, v1.ID)
	if err != nil || !found || current.Revision != 2 || current.Expression.Metric.Threshold != 90 {
		t.Fatalf("current=%+v found=%t err=%v", current, found, err)
	}
	history, err := repository.ListRevisions(context.Background(), v1.DeviceID, v1.ID)
	if err != nil || len(history) != 2 || history[0].Revision != 2 || history[1].Revision != 1 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	var events int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE resource_type = ?`, alerts.RuleAuditResourceType).Scan(&events); err != nil || events != 2 {
		t.Fatalf("events=%d err=%v", events, err)
	}
}

func TestAlertRuleRepositoryCASAndOwnerCheckAreAtomic(t *testing.T) {
	databases := openTestDatabases(t)
	ownerID := insertDashboardTestUser(t, databases, "owner", auth.RoleOwner)
	repository, err := NewAlertRuleRepository(databases.Core, dashboard.NewMetricRegistry(), alerts.Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	v1 := storedRule(1, 80)
	if err := repository.SaveRevisionWithAudit(context.Background(), ownerID, v1, 0, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, v1)); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, rule := range []alerts.Rule{storedRule(2, 90), storedRule(3, 95)} {
		group.Add(1)
		go func(rule alerts.Rule) {
			defer group.Done()
			results <- repository.SaveRevisionWithAudit(context.Background(), ownerID, rule, 1, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, rule))
		}(rule)
	}
	group.Wait()
	close(results)
	var successes, conflicts int
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, alerts.ErrRuleVersionConflict):
			conflicts++
		default:
			t.Fatalf("save error=%v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	if _, err := databases.Core.Exec(`UPDATE users SET disabled = 1 WHERE user_id = ?`, ownerID); err != nil {
		t.Fatal(err)
	}
	current, _, err := repository.Current(context.Background(), v1.DeviceID, v1.ID)
	if err != nil {
		t.Fatal(err)
	}
	next := storedRule(current.Revision+1, 99)
	if err := repository.SaveRevisionWithAudit(context.Background(), ownerID, next, current.Revision, storedRuleAudit(t, ownerID, alerts.RuleAuditAction, next)); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("disabled Owner error=%v", err)
	}
}

func storedRule(revision uint64, threshold float64) alerts.Rule {
	return alerts.Rule{
		ID: "high-cpu", DeviceID: "node/a", Revision: revision, Severity: alerts.SeverityWarning, Enabled: true,
		Expression: alerts.Expression{Metric: &alerts.MetricComparison{DescriptorID: "cpu.usage_percent", Operator: alerts.OperatorGreaterThan, Threshold: threshold, Unit: "percent"}},
	}
}

func storedRuleAudit(t *testing.T, actorID, action string, rule alerts.Rule) audit.Event {
	t.Helper()
	event, err := audit.NewEvent(time.Now(), actorID, action, alerts.RuleAuditResourceType, alerts.RuleAuditResourceID(rule))
	if err != nil {
		t.Fatal(err)
	}
	return event
}
