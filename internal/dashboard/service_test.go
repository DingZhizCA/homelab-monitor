/*
Package dashboard tests permission gates, revision forwarding, and document validation.

The fake repository does not persist data and stands in for implementations
that must enforce ownership and compare revisions atomically.
*/
package dashboard

import (
	"context"
	"errors"
	"testing"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

type dashboardRepoFake struct {
	called   string
	actorID  string
	version  uint64
	expected uint64
	doc      Document
}

func (f *dashboardRepoFake) result(action, actorID string, version uint64, doc Document) (Dashboard, error) {
	f.called, f.actorID, f.version, f.doc = action, actorID, version, doc
	return Dashboard{ID: "dash-1", Scope: ScopePersonal}, nil
}

func (f *dashboardRepoFake) ListVisible(_ context.Context, actorID string, _ auth.Role) ([]Dashboard, error) {
	dashboard, err := f.result("list", actorID, 0, Document{})
	return []Dashboard{dashboard}, err
}

func (f *dashboardRepoFake) GetVisible(_ context.Context, actorID string, _ auth.Role, _ string) (Dashboard, error) {
	return f.result("get", actorID, 0, Document{})
}

func (f *dashboardRepoFake) DraftForOwner(_ context.Context, actorID, _ string, version uint64) (Document, error) {
	f.called, f.actorID, f.version = "draft_for_owner", actorID, version
	return Document{}, nil
}

func (f *dashboardRepoFake) CreatePersonal(_ context.Context, actorID, _ string, doc Document) (Dashboard, error) {
	return f.result("create_personal", actorID, 0, doc)
}

func (f *dashboardRepoFake) CreateSharedDraft(_ context.Context, actorID, _ string, doc Document, _ AuditEventFactory) (Dashboard, error) {
	return f.result("create_shared", actorID, 0, doc)
}

func (f *dashboardRepoFake) SavePersonal(_ context.Context, actorID, _ string, version uint64, doc Document) (Dashboard, error) {
	return f.result("save_personal", actorID, version, doc)
}

func (f *dashboardRepoFake) SaveSharedDraft(_ context.Context, actorID, _ string, version uint64, doc Document, _ audit.Event) (Dashboard, error) {
	return f.result("save_shared", actorID, version, doc)
}

func (f *dashboardRepoFake) PublishShared(_ context.Context, actorID, _ string, version uint64, _ audit.Event) (Dashboard, error) {
	return f.result("publish_shared", actorID, version, Document{})
}

func (f *dashboardRepoFake) ListPublishedRevisions(_ context.Context, actorID, _ string) ([]Revision, error) {
	f.called, f.actorID = "list_published_revisions", actorID
	return []Revision{{Number: 3}}, nil
}

func (f *dashboardRepoFake) PublishHistoricalAsNew(_ context.Context, actorID, _ string, target, expected uint64, _ audit.Event) (Dashboard, error) {
	f.expected = expected
	return f.result("rollback", actorID, target, Document{})
}

func (f *dashboardRepoFake) CopyVisibleToPersonal(_ context.Context, actorID string, _ auth.Role, _, _ string) (Dashboard, error) {
	return f.result("copy", actorID, 0, Document{})
}

type publicationPolicyFake struct {
	err   error
	calls int
}

func (f *publicationPolicyFake) ValidateViewerAccess(context.Context, Document) error {
	f.calls++
	return f.err
}

func TestDashboardServiceAppliesRoleGatesAndRevisionPreconditions(t *testing.T) {
	repository := &dashboardRepoFake{}
	policy := &publicationPolicyFake{}
	service, err := NewService(repository, policy)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	viewer := auth.Principal{UserID: "viewer-1", Role: auth.RoleViewer}
	owner := auth.Principal{UserID: "owner-1", Role: auth.RoleOwner}
	document := Document{Widgets: []Widget{{ID: "cpu", Kind: WidgetMetricCard, Title: "CPU", DescriptorID: "host.cpu"}}}

	if _, err := service.CreateSharedDraft(ctx, viewer, "Shared", document); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Viewer create shared error=%v, want forbidden", err)
	}
	if repository.called != "" {
		t.Fatal("forbidden operation reached the repository")
	}
	if _, err := service.SavePersonal(ctx, viewer, "dash-1", 0, document); err == nil {
		t.Fatal("zero expected revision was accepted")
	}
	if _, err := service.CreatePersonal(ctx, viewer, "Personal", document); err != nil {
		t.Fatal(err)
	}
	if repository.called != "create_personal" || repository.actorID != viewer.UserID {
		t.Fatalf("personal create repository call = %q actor %q", repository.called, repository.actorID)
	}
	if listed, err := service.ListVisible(ctx, viewer); err != nil || len(listed) != 1 || repository.called != "list" {
		t.Fatalf("viewer dashboard list=%+v call=%q err=%v", listed, repository.called, err)
	}
	if dashboard, err := service.GetVisible(ctx, viewer, "dash-1"); err != nil || dashboard.ID != "dash-1" || repository.called != "get" {
		t.Fatalf("viewer dashboard get=%+v call=%q err=%v", dashboard, repository.called, err)
	}
	if _, err := service.PublishShared(ctx, owner, "dash-1", 4); err != nil {
		t.Fatal(err)
	}
	if repository.called != "publish_shared" || repository.version != 4 || policy.calls != 1 {
		t.Fatalf("publish repository call=%q revision=%d", repository.called, repository.version)
	}
	if revisions, err := service.ListPublishedRevisions(ctx, owner, "dash-1"); err != nil || len(revisions) != 1 || revisions[0].Number != 3 || repository.called != "list_published_revisions" {
		t.Fatalf("Owner revision history=%+v call=%q err=%v", revisions, repository.called, err)
	}
	if _, err := service.ListPublishedRevisions(ctx, viewer, "dash-1"); !errors.Is(err, ErrForbidden) || repository.called != "list_published_revisions" {
		t.Fatalf("Viewer revision history error=%v repository call=%q", err, repository.called)
	}
	if _, err := service.RollbackShared(ctx, owner, "dash-1", 2, 4); err != nil {
		t.Fatal(err)
	}
	if repository.called != "rollback" || repository.version != 2 || repository.expected != 4 {
		t.Fatalf("rollback repository call=%q revisions=%d", repository.called, repository.version)
	}
	if _, err := service.RollbackShared(ctx, owner, "dash-1", 4, 4); err == nil {
		t.Fatal("rollback to current published revision was accepted")
	}
}

func TestDashboardPublicationRequiresViewerPolicyApproval(t *testing.T) {
	repository := &dashboardRepoFake{}
	policy := &publicationPolicyFake{err: ErrForbidden}
	service, err := NewService(repository, policy)
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.PublishShared(context.Background(), auth.Principal{UserID: "owner-1", Role: auth.RoleOwner}, "dash-1", 3)
	if !errors.Is(err, ErrForbidden) {
		t.Fatalf("Viewer policy error=%v, want forbidden", err)
	}
	if repository.called != "draft_for_owner" || policy.calls != 1 {
		t.Fatalf("denied publication reached commit: repository action=%q policy calls=%d", repository.called, policy.calls)
	}
}

func TestDashboardDocumentRejectsExecutableOrInvalidLayoutData(t *testing.T) {
	valid := Document{
		Widgets: []Widget{{ID: "cpu", Kind: WidgetTimeSeries, Title: "CPU history", DescriptorID: "host.cpu"}},
		Layouts: []Layout{{Breakpoint: "desktop", Items: []Placement{{WidgetID: "cpu", Width: 4, Height: 2}}}},
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid document rejected: %v", err)
	}
	invalid := []Document{
		{Widgets: []Widget{{ID: "script", Kind: WidgetKind("expression"), Title: "expression", DescriptorID: "host.cpu"}}},
		{Widgets: []Widget{{ID: "cpu", Kind: WidgetMetricCard, Title: "CPU"}}, Layouts: []Layout{{Breakpoint: "desktop", Items: []Placement{{WidgetID: "missing", Width: 1, Height: 1}}}}},
		{Widgets: []Widget{{ID: "cpu", Kind: WidgetMetricCard, Title: "CPU"}}, Layouts: []Layout{{Breakpoint: "desktop", Items: []Placement{{WidgetID: "cpu", Width: 0, Height: 1}}}}},
		{Widgets: []Widget{{ID: "cpu", Kind: WidgetMetricCard, Title: "CPU"}, {ID: "cpu", Kind: WidgetStatus, Title: "duplicate"}}},
	}
	for index, document := range invalid {
		if err := document.Validate(); err == nil {
			t.Errorf("invalid document %d was accepted", index)
		}
	}
}
