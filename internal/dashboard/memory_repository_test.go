/*
Package dashboard tests the ephemeral repository's atomic revision and visibility rules.
*/
package dashboard

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

func TestMemoryRepositoryEnforcesPublicationVisibilityAndRollbackHistory(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository()
	service, err := NewService(repository, &publicationPolicyFake{})
	if err != nil {
		t.Fatal(err)
	}
	owner := auth.Principal{UserID: "owner-a", Role: auth.RoleOwner}
	otherOwner := auth.Principal{UserID: "owner-b", Role: auth.RoleOwner}
	viewer := auth.Principal{UserID: "viewer-a", Role: auth.RoleViewer}
	draftOne := testDashboardDocument("first")
	shared, err := service.CreateSharedDraft(ctx, owner, "Shared", draftOne)
	if err != nil {
		t.Fatal(err)
	}
	if shared.Draft == nil || shared.Draft.Number != 1 || shared.Published != nil {
		t.Fatalf("new shared dashboard revisions=%+v", shared)
	}
	if visible, err := service.ListVisible(ctx, viewer); err != nil || len(visible) != 0 {
		t.Fatalf("unpublished dashboard visible to Viewer: %+v err=%v", visible, err)
	}
	if _, err := service.GetVisible(ctx, otherOwner, shared.ID); !errors.Is(err, ErrDashboardNotFound) {
		t.Fatalf("unowned draft read error=%v, want not found", err)
	}

	draftTwo := testDashboardDocument("second")
	shared, err = service.SaveSharedDraft(ctx, owner, shared.ID, 1, draftTwo)
	if err != nil || shared.Draft.Number != 2 {
		t.Fatalf("save shared draft result=%+v err=%v", shared, err)
	}
	if _, err := service.SaveSharedDraft(ctx, owner, shared.ID, 1, draftOne); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale draft save error=%v, want revision conflict", err)
	}
	shared, err = service.PublishShared(ctx, owner, shared.ID, 2)
	if err != nil || shared.Published == nil || shared.Published.Number != 2 {
		t.Fatalf("publish result=%+v err=%v", shared, err)
	}
	viewerCopy, err := service.GetVisible(ctx, viewer, shared.ID)
	if err != nil || viewerCopy.Draft != nil || viewerCopy.Published == nil || viewerCopy.Published.Number != 2 {
		t.Fatalf("Viewer visible dashboard=%+v err=%v", viewerCopy, err)
	}

	draftThree := testDashboardDocument("third")
	shared, err = service.SaveSharedDraft(ctx, owner, shared.ID, 2, draftThree)
	if err != nil || shared.Published.Number != 2 || shared.Draft.Number != 3 {
		t.Fatalf("draft save changed published revision: %+v err=%v", shared, err)
	}
	shared, err = service.PublishShared(ctx, owner, shared.ID, 3)
	if err != nil || shared.Published.Number != 3 {
		t.Fatalf("second publish result=%+v err=%v", shared, err)
	}
	shared, err = service.RollbackShared(ctx, owner, shared.ID, 2, 3)
	if err != nil || shared.Published.Number != 4 || shared.Published.Document.Widgets[0].Title != "second" {
		t.Fatalf("rollback result=%+v err=%v", shared, err)
	}
	history, err := service.ListPublishedRevisions(ctx, owner, shared.ID)
	if err != nil || len(history) != 3 || history[0].Number != 4 || history[1].Number != 3 || history[2].Number != 2 {
		t.Fatalf("published history=%+v err=%v", history, err)
	}
	history[0].Document.Widgets[0].Title = "caller mutation"
	history, err = service.ListPublishedRevisions(ctx, owner, shared.ID)
	if err != nil || history[0].Document.Widgets[0].Title != "second" {
		t.Fatalf("history response mutated repository data: %+v err=%v", history, err)
	}
	if _, err := service.ListPublishedRevisions(ctx, viewer, shared.ID); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Viewer history error=%v, want forbidden", err)
	}
	if _, err := service.ListPublishedRevisions(ctx, otherOwner, shared.ID); !errors.Is(err, ErrDashboardNotFound) {
		t.Fatalf("unowned published history error=%v, want not found", err)
	}
	if _, err := service.RollbackShared(ctx, owner, shared.ID, 2, 3); !errors.Is(err, ErrVersionConflict) {
		t.Fatalf("stale rollback error=%v, want revision conflict", err)
	}
	events := repository.AuditEvents()
	wantActions := []string{DashboardAuditCreateShared, DashboardAuditSaveDraft, DashboardAuditPublish, DashboardAuditSaveDraft, DashboardAuditPublish, DashboardAuditRollback}
	if len(events) != len(wantActions) {
		t.Fatalf("audit events=%+v", events)
	}
	for index, action := range wantActions {
		if events[index].ActorID != owner.UserID || events[index].Action != action || events[index].ResourceType != DashboardAuditResourceType || events[index].ResourceID != shared.ID {
			t.Fatalf("audit event %d=%+v", index, events[index])
		}
	}
	copy, err := service.CopyVisibleToPersonal(ctx, viewer, shared.ID, "Copied")
	if err != nil || copy.Scope != ScopePersonal || copy.OwnerUserID != viewer.UserID || copy.Draft.Document.Widgets[0].Title != "second" {
		t.Fatalf("Viewer copy=%+v err=%v", copy, err)
	}
	if _, err := service.GetVisible(ctx, owner, copy.ID); !errors.Is(err, ErrDashboardNotFound) {
		t.Fatalf("personal copy visible to another user: err=%v", err)
	}
}

func TestMemoryRepositoryRejectsMismatchedSharedDashboardAuditAtomically(t *testing.T) {
	repository := NewMemoryRepository()
	eventFactory := func(string) (audit.Event, error) {
		return audit.NewEvent(time.Unix(10, 0), "owner-a", DashboardAuditCreateShared, DashboardAuditResourceType, "wrong-id")
	}
	if _, err := repository.CreateSharedDraft(context.Background(), "owner-a", "Shared", testDashboardDocument("draft"), eventFactory); err == nil {
		t.Fatal("shared Dashboard with mismatched audit target was created")
	}
	visible, err := repository.ListVisible(context.Background(), "owner-a", auth.RoleOwner)
	if err != nil {
		t.Fatal(err)
	}
	if len(visible) != 0 || len(repository.AuditEvents()) != 0 {
		t.Fatalf("rejected creation changed repository: dashboards=%+v events=%+v", visible, repository.AuditEvents())
	}
}

func TestMemoryRepositoryPersonalRevisionCompareAndSwapIsAtomic(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository()
	dashboard, err := repository.CreatePersonal(ctx, "user-a", "Personal", testDashboardDocument("initial"))
	if err != nil {
		t.Fatal(err)
	}
	documents := []Document{testDashboardDocument("left"), testDashboardDocument("right")}
	results := make(chan error, len(documents))
	var group sync.WaitGroup
	for _, document := range documents {
		group.Add(1)
		go func(document Document) {
			defer group.Done()
			_, saveErr := repository.SavePersonal(ctx, "user-a", dashboard.ID, 1, document)
			results <- saveErr
		}(document)
	}
	group.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrVersionConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent save error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent save results: successes=%d conflicts=%d", successes, conflicts)
	}
}

func TestMemoryRepositoryDoesNotLeakMutableDocumentSlices(t *testing.T) {
	ctx := context.Background()
	repository := NewMemoryRepository()
	original := testDashboardDocument("original")
	dashboard, err := repository.CreatePersonal(ctx, "user-a", "Personal", original)
	if err != nil {
		t.Fatal(err)
	}
	dashboard.Draft.Document.Widgets[0].Title = "caller mutation"
	original.Widgets[0].Title = "input mutation"
	loaded, err := repository.GetVisible(ctx, "user-a", auth.RoleViewer, dashboard.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Draft.Document.Widgets[0].Title; got != "original" {
		t.Fatalf("stored document title changed through alias: %q", got)
	}
}

func testDashboardDocument(title string) Document {
	return Document{Widgets: []Widget{{ID: "cpu", Kind: WidgetMetricCard, Title: title, DescriptorID: "cpu.usage_percent"}}}
}
