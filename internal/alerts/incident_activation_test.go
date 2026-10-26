/*
Package alerts tests Owner authorization and request validation for explicit
Incident revision activation.
*/
package alerts

import (
	"context"
	"errors"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

type activationRepositoryStub struct {
	actorID        string
	deviceID       string
	ruleID         string
	targetRevision uint64
	event          audit.Event
}

func (r *activationRepositoryStub) ActivateRuleRevisionWithAudit(_ context.Context, actorID, deviceID, ruleID string, targetRevision uint64, event audit.Event) (IncidentActivationResult, error) {
	r.actorID, r.deviceID, r.ruleID, r.targetRevision, r.event = actorID, deviceID, ruleID, targetRevision, event
	return IncidentActivationResult{Ready: true}, nil
}

func TestIncidentActivationServiceRequiresOwnerAndValidTarget(t *testing.T) {
	repository := &activationRepositoryStub{}
	service, err := NewIncidentActivationService(repository)
	if err != nil {
		t.Fatal(err)
	}
	viewer := auth.Principal{UserID: "viewer-a", Role: auth.RoleViewer}
	if _, err := service.ActivateRevision(context.Background(), viewer, "node/a", "high-cpu", 2); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("Viewer activation error=%v", err)
	}
	owner := auth.Principal{UserID: "owner-a", Role: auth.RoleOwner}
	if _, err := service.ActivateRevision(context.Background(), owner, " node/a ", "high-cpu", 2); !errors.Is(err, ErrInvalidIncidentRequest) {
		t.Fatalf("invalid device error=%v", err)
	}
	result, err := service.ActivateRevision(context.Background(), owner, "node/a", "high-cpu", 2)
	if err != nil || !result.Ready {
		t.Fatalf("activation result=%+v err=%v", result, err)
	}
	if repository.actorID != owner.UserID || repository.deviceID != "node/a" || repository.ruleID != "high-cpu" || repository.targetRevision != 2 || repository.event.Action != IncidentSupersedeAuditAction || repository.event.ResourceID != "node/a/high-cpu" {
		t.Fatalf("repository call=%+v", repository)
	}
}

func TestIncidentActivationServiceRequiresRepository(t *testing.T) {
	if _, err := NewIncidentActivationService(nil); err == nil {
		t.Fatal("activation service accepted nil repository")
	}
}
