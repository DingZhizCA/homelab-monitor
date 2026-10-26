/*
Package alerts verifies authorization and bounds for transition history reads.
*/
package alerts

import (
	"context"
	"errors"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

type transitionSourceStub struct{ transitions []IncidentTransition }

func (s transitionSourceStub) ListTransitions(context.Context, string, string, int) ([]IncidentTransition, error) {
	return s.transitions, nil
}

func TestIncidentTransitionServiceAuthorizesAndBoundsQueries(t *testing.T) {
	service, err := NewIncidentTransitionService(transitionSourceStub{}, 5)
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []auth.Principal{{UserID: "owner-a", Role: auth.RoleOwner}, {UserID: "viewer-a", Role: auth.RoleViewer}} {
		transitions, err := service.List(context.Background(), actor, "node/a", "high-cpu", 5)
		if err != nil || transitions == nil || len(transitions) != 0 {
			t.Fatalf("actor=%+v transitions=%+v err=%v", actor, transitions, err)
		}
	}
	viewer := auth.Principal{UserID: "viewer-a", Role: auth.RoleViewer}
	if _, err := service.List(context.Background(), viewer, "node/a", "high-cpu", 6); !errors.Is(err, ErrInvalidIncidentRequest) {
		t.Fatalf("over-limit query error=%v", err)
	}
	if _, err := service.List(context.Background(), auth.Principal{}, "node/a", "high-cpu", 1); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("anonymous query error=%v", err)
	}
}
