/*
Package alerts tests authorization and validation around current Incident reads.
*/
package alerts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

func TestIncidentServiceAuthorizesDeviceScopedReads(t *testing.T) {
	repository, err := NewMemoryIncidentRepository(2)
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewIncidentService(repository)
	if err != nil {
		t.Fatal(err)
	}
	zero := time.Duration(0)
	rule := Rule{ID: "high-cpu", DeviceID: "node/a", Revision: 1, Severity: SeverityWarning, Enabled: false, Expression: Expression{Metric: &MetricComparison{DescriptorID: "cpu.usage_percent", Operator: OperatorGreaterThan, Threshold: 80, Unit: "percent"}}}
	if _, err := repository.Advance(context.Background(), rule, EvaluationResult{RuleID: rule.ID, Revision: rule.Revision, State: StateDisabled}, time.Unix(100, 0), IncidentPolicy{PendingFor: &zero, RecoveryFor: &zero, UnknownBehavior: UncertainHold, DisabledBehavior: UncertainHold}, time.Time{}); err != nil {
		t.Fatal(err)
	}

	for _, role := range []auth.Role{auth.RoleOwner, auth.RoleViewer} {
		states, err := service.ListCurrent(context.Background(), auth.Principal{UserID: "user", Role: role}, rule.DeviceID)
		if err != nil || len(states) != 1 || states[0].RuleID != rule.ID {
			t.Fatalf("role=%q states=%+v err=%v", role, states, err)
		}
	}
	if _, err := service.ListCurrent(context.Background(), auth.Principal{UserID: "user", Role: "other"}, rule.DeviceID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("unsupported role error=%v", err)
	}
	if _, err := service.ListCurrent(context.Background(), auth.Principal{Role: auth.RoleViewer}, rule.DeviceID); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("empty identity error=%v", err)
	}
	if _, err := service.ListCurrent(context.Background(), auth.Principal{UserID: "user", Role: auth.RoleViewer}, " node/a "); !errors.Is(err, ErrInvalidIncidentRequest) {
		t.Fatalf("invalid device error=%v", err)
	}
}

func TestIncidentServiceRequiresSource(t *testing.T) {
	if _, err := NewIncidentService(nil); err == nil {
		t.Fatal("Incident service accepted a nil source")
	}
}
