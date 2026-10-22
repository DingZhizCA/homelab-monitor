/*
Package alerts tests Owner authorization and validation at the rule service boundary.
*/
package alerts

import (
	"context"
	"errors"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

func TestRuleServiceRequiresOwnerForRuleReadsAndWrites(t *testing.T) {
	registry := dashboard.NewMetricRegistry()
	repository, err := NewMemoryRuleRepository(registry, Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRuleService(repository, registry, Limits{MaxDepth: 4, MaxConditions: 8})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	viewer := auth.Principal{UserID: "viewer-a", Role: auth.RoleViewer}
	rule := repositoryTestRule(1, 80)
	if err := service.SaveRevision(ctx, viewer, rule, 0); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("Viewer rule write error=%v", err)
	}
	if _, err := service.ListCurrent(ctx, viewer, rule.DeviceID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("Viewer rule listing error=%v", err)
	}
	if _, ok, err := repository.Current(ctx, rule.DeviceID, rule.ID); err != nil || ok {
		t.Fatalf("Viewer write changed repository: found=%t err=%v", ok, err)
	}
}

func TestRuleServiceValidatesAndPersistsOwnerRevisionWithCAS(t *testing.T) {
	registry := dashboard.NewMetricRegistry()
	limits := Limits{MaxDepth: 4, MaxConditions: 8}
	repository, err := NewMemoryRuleRepository(registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewRuleService(repository, registry, limits)
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.Principal{UserID: "owner-a", Role: auth.RoleOwner}
	ctx := context.Background()
	rule := repositoryTestRule(1, 80)
	if err := service.SaveRevision(ctx, owner, rule, 0); err != nil {
		t.Fatalf("Owner rule write: %v", err)
	}
	updated := repositoryTestRule(2, 90)
	if err := service.SaveRevision(ctx, owner, updated, 0); !errors.Is(err, ErrRuleVersionConflict) {
		t.Fatalf("stale Owner write error=%v, want revision conflict", err)
	}
	if err := service.SaveRevision(ctx, owner, updated, 1); err != nil {
		t.Fatalf("Owner update: %v", err)
	}
	if err := service.SaveRevision(ctx, owner, updated, 1); err != nil {
		t.Fatalf("identical Owner retry: %v", err)
	}
	events := repository.AuditEvents()
	if len(events) != 2 || events[0].ActorID != owner.UserID || events[1].ActorID != owner.UserID || events[1].Action != RuleAuditAction || events[1].ResourceType != RuleAuditResourceType || events[1].ResourceID != RuleAuditResourceID(updated) {
		t.Fatalf("rule audit events=%+v", events)
	}
	current, ok, err := service.Current(ctx, owner, rule.DeviceID, rule.ID)
	if err != nil || !ok || current.Revision != 2 || current.Expression.Metric.Threshold != 90 {
		t.Fatalf("Owner current rule=%+v found=%t err=%v", current, ok, err)
	}
	if _, err := service.ListCurrent(ctx, owner, " node/a "); !errors.Is(err, ErrInvalidRuleRequest) {
		t.Fatalf("untrimmed device request error=%v", err)
	}
	invalid := repositoryTestRule(3, 80)
	invalid.Expression.Metric.Unit = "bytes"
	if err := service.SaveRevision(ctx, owner, invalid, 2); err == nil {
		t.Fatal("Owner wrote a rule with an incompatible metric unit")
	}
}
