/*
Package storage verifies persistent Dashboard revision, visibility, audit, and
compare-and-swap behavior against an isolated core.db.
*/
package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestDashboardRepositoryPersistsDraftPublicationHistoryAndVisibility(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	ownerID := insertDashboardTestUser(t, databases, "owner", auth.RoleOwner)
	viewerID := insertDashboardTestUser(t, databases, "viewer", auth.RoleViewer)
	repository, err := NewDashboardRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	created, err := repository.CreateSharedDraft(ctx, ownerID, "Shared", dashboardTestDocument("initial"), func(id string) (audit.Event, error) {
		return audit.NewEvent(time.Now(), ownerID, dashboard.DashboardAuditCreateShared, dashboard.DashboardAuditResourceType, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	if visible, err := repository.ListVisible(ctx, viewerID, auth.RoleViewer); err != nil || len(visible) != 0 {
		t.Fatalf("unpublished Viewer list=%+v err=%v", visible, err)
	}
	publishEvent := dashboardTestAudit(t, ownerID, dashboard.DashboardAuditPublish, created.ID)
	published, err := repository.PublishShared(ctx, ownerID, created.ID, 1, publishEvent)
	if err != nil || published.Published == nil || published.Published.Number != 1 {
		t.Fatalf("published=%+v err=%v", published, err)
	}
	saveEvent := dashboardTestAudit(t, ownerID, dashboard.DashboardAuditSaveDraft, created.ID)
	updated, err := repository.SaveSharedDraft(ctx, ownerID, created.ID, 1, dashboardTestDocument("changed"), saveEvent)
	if err != nil || updated.Draft == nil || updated.Draft.Number != 2 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	viewer, err := repository.GetVisible(ctx, viewerID, auth.RoleViewer, created.ID)
	if err != nil || viewer.Draft != nil || viewer.Published == nil || viewer.Published.Document.Widgets[0].Title != "initial" {
		t.Fatalf("Viewer Dashboard=%+v err=%v", viewer, err)
	}
	if _, err := repository.PublishShared(ctx, ownerID, created.ID, 2, dashboardTestAudit(t, ownerID, dashboard.DashboardAuditPublish, created.ID)); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := repository.PublishHistoricalAsNew(ctx, ownerID, created.ID, 1, 2, dashboardTestAudit(t, ownerID, dashboard.DashboardAuditRollback, created.ID))
	if err != nil || rolledBack.Published == nil || rolledBack.Published.Number != 3 || rolledBack.Published.Document.Widgets[0].Title != "initial" {
		t.Fatalf("rollback=%+v err=%v", rolledBack, err)
	}
	revisions, err := repository.ListPublishedRevisions(ctx, ownerID, created.ID)
	if err != nil || len(revisions) != 3 || revisions[0].Number != 3 || revisions[1].Number != 2 || revisions[2].Number != 1 {
		t.Fatalf("revisions=%+v err=%v", revisions, err)
	}
	if err := databases.Close(); err != nil {
		t.Fatal(err)
	}
	databases, err = Open(ctx, directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	repository, err = NewDashboardRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	reopened, err := repository.GetVisible(ctx, ownerID, auth.RoleOwner, created.ID)
	if err != nil || reopened.Draft == nil || reopened.Draft.Number != 2 || reopened.Published == nil || reopened.Published.Number != 3 {
		t.Fatalf("reopened=%+v err=%v", reopened, err)
	}
	var events int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM audit_events WHERE resource_type = ? AND resource_id = ?`, dashboard.DashboardAuditResourceType, created.ID).Scan(&events); err != nil || events != 5 {
		t.Fatalf("Dashboard audit events=%d err=%v", events, err)
	}
}

func TestDashboardRepositoryPersonalCASCopyAndActiveActorChecks(t *testing.T) {
	ctx := context.Background()
	databases := openTestDatabases(t)
	ownerID := insertDashboardTestUser(t, databases, "owner", auth.RoleOwner)
	viewerID := insertDashboardTestUser(t, databases, "viewer", auth.RoleViewer)
	repository, err := NewDashboardRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	personal, err := repository.CreatePersonal(ctx, viewerID, "Personal", dashboardTestDocument("initial"))
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, title := range []string{"left", "right"} {
		group.Add(1)
		go func(title string) {
			defer group.Done()
			_, saveErr := repository.SavePersonal(ctx, viewerID, personal.ID, 1, dashboardTestDocument(title))
			results <- saveErr
		}(title)
	}
	group.Wait()
	close(results)
	var succeeded, conflicted int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, dashboard.ErrVersionConflict):
			conflicted++
		default:
			t.Fatalf("concurrent save error=%v", err)
		}
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	shared, err := repository.CreateSharedDraft(ctx, ownerID, "Shared", dashboardTestDocument("shared"), func(id string) (audit.Event, error) {
		return audit.NewEvent(time.Now(), ownerID, dashboard.DashboardAuditCreateShared, dashboard.DashboardAuditResourceType, id)
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.PublishShared(ctx, ownerID, shared.ID, 1, dashboardTestAudit(t, ownerID, dashboard.DashboardAuditPublish, shared.ID)); err != nil {
		t.Fatal(err)
	}
	copied, err := repository.CopyVisibleToPersonal(ctx, viewerID, auth.RoleViewer, shared.ID, "Copied")
	if err != nil || copied.Scope != dashboard.ScopePersonal || copied.OwnerUserID != viewerID || copied.Draft.Document.Widgets[0].Title != "shared" {
		t.Fatalf("copied=%+v err=%v", copied, err)
	}
	if _, err := databases.Core.Exec(`UPDATE users SET disabled = 1 WHERE user_id = ?`, viewerID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.ListVisible(ctx, viewerID, auth.RoleViewer); !errors.Is(err, dashboard.ErrForbidden) {
		t.Fatalf("disabled actor error=%v", err)
	}
}

func TestDashboardRepositoryRejectsMismatchedAuditAtomically(t *testing.T) {
	databases := openTestDatabases(t)
	ownerID := insertDashboardTestUser(t, databases, "owner", auth.RoleOwner)
	repository, err := NewDashboardRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	_, err = repository.CreateSharedDraft(context.Background(), ownerID, "Shared", dashboardTestDocument("initial"), func(id string) (audit.Event, error) {
		return audit.NewEvent(time.Now(), ownerID, dashboard.DashboardAuditPublish, dashboard.DashboardAuditResourceType, id)
	})
	if err == nil {
		t.Fatal("mismatched audit event was accepted")
	}
	var dashboards, revisions int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM dashboards`).Scan(&dashboards); err != nil {
		t.Fatal(err)
	}
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM dashboard_revisions`).Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if dashboards != 0 || revisions != 0 {
		t.Fatalf("rejected creation wrote dashboards=%d revisions=%d", dashboards, revisions)
	}
}

func insertDashboardTestUser(t *testing.T, databases *Databases, username string, role auth.Role) string {
	t.Helper()
	id := username + "-id"
	if _, err := databases.Core.Exec(`INSERT INTO users (user_id, username, role, password_hash, disabled) VALUES (?, ?, ?, 'fixture', 0)`, id, username, role); err != nil {
		t.Fatal(err)
	}
	return id
}

func dashboardTestDocument(title string) dashboard.Document {
	return dashboard.Document{Widgets: []dashboard.Widget{{ID: "cpu", Kind: dashboard.WidgetMetricCard, Title: title, DescriptorID: "cpu.usage_percent"}}}
}

func dashboardTestAudit(t *testing.T, actorID, action, resourceID string) audit.Event {
	t.Helper()
	event, err := audit.NewEvent(time.Now(), actorID, action, dashboard.DashboardAuditResourceType, resourceID)
	if err != nil {
		t.Fatal(err)
	}
	return event
}
