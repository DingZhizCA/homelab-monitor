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

type archiveSourceStub struct{ archives []IncidentArchive }

func (s archiveSourceStub) ListArchives(_ context.Context, _, _ string, _ int) ([]IncidentArchive, error) {
	return s.archives, nil
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

func TestIncidentArchiveServiceAuthorizesAndBoundsQueries(t *testing.T) {
	archive := IncidentArchive{Disposition: IncidentArchiveSuperseded}
	service, err := NewIncidentArchiveService(archiveSourceStub{archives: []IncidentArchive{archive}}, 5)
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.Principal{UserID: "owner-a", Role: auth.RoleOwner}
	viewer := auth.Principal{UserID: "viewer-a", Role: auth.RoleViewer}
	for _, actor := range []auth.Principal{owner, viewer} {
		archives, err := service.List(context.Background(), actor, "node/a", "high-cpu", 5)
		if err != nil || len(archives) != 1 {
			t.Fatalf("actor=%+v archives=%+v err=%v", actor, archives, err)
		}
	}
	if _, err := service.List(context.Background(), viewer, "node/a", "high-cpu", 6); !errors.Is(err, ErrInvalidIncidentRequest) {
		t.Fatalf("over-limit query error=%v", err)
	}
	if _, err := service.List(context.Background(), auth.Principal{}, "node/a", "high-cpu", 1); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("anonymous query error=%v", err)
	}
	if _, err := NewIncidentArchiveService(nil, 1); err == nil {
		t.Fatal("archive service accepted nil source")
	}
}
