/*
Package storage verifies persistent account, Session, audit, and last-Owner
transactions in core.db without exposing credentials outside test fixtures.
*/
package storage

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

func TestAuthRepositoryPersistsAccountsSessionsAndAuditAcrossReopen(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	repository, err := NewAuthRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	service, err := auth.NewService(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := service.BootstrapOwner(context.Background(), "owner", "owner-password"); err != nil {
		t.Fatal(err)
	}
	token, expiresAt, err := service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil || expiresAt != now.Add(time.Hour) {
		t.Fatalf("login expires=%s err=%v", expiresAt, err)
	}
	if err := databases.Close(); err != nil {
		t.Fatal(err)
	}

	databases, err = Open(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	repository, err = NewAuthRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	service, err = auth.NewService(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.ResolveSession(context.Background(), token)
	if err != nil || principal.Username != "owner" || principal.Role != auth.RoleOwner {
		t.Fatalf("principal=%+v err=%v", principal, err)
	}
	if err := service.Logout(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveSession(context.Background(), token); !errors.Is(err, auth.ErrUnauthenticated) {
		t.Fatalf("revoked session error=%v", err)
	}
	assertCoreCounts(t, databases, 1, 0, 1)
}

func TestAuthRepositoryEnforcesCurrentAndLastOwnerWithAtomicAudit(t *testing.T) {
	databases := openTestDatabases(t)
	repository, err := NewAuthRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 1, 0, 0, 0, time.UTC)
	service, err := auth.NewService(repository, repository, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := service.BootstrapOwner(ctx, "owner-a", "owner-a-password"); err != nil {
		t.Fatal(err)
	}
	ownerA, found, err := repository.ByUsername(ctx, "owner-a")
	if err != nil || !found {
		t.Fatalf("owner A found=%t err=%v", found, err)
	}
	actorA := auth.Principal{UserID: ownerA.ID, Username: ownerA.Username, Role: auth.RoleOwner}
	if err := service.CreateUser(ctx, actorA, "owner-b", "owner-b-password", auth.RoleOwner); err != nil {
		t.Fatal(err)
	}
	ownerB, _, err := repository.ByUsername(ctx, "owner-b")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateUser(ctx, actorA, ownerA.ID, auth.RoleViewer, false); err != nil {
		t.Fatal(err)
	}
	if err := service.CreateUser(ctx, actorA, "forbidden", "some-password", auth.RoleViewer); !errors.Is(err, auth.ErrForbidden) {
		t.Fatalf("stale Owner create error=%v", err)
	}
	actorB := auth.Principal{UserID: ownerB.ID, Username: ownerB.Username, Role: auth.RoleOwner}
	if err := service.UpdateUser(ctx, actorB, ownerB.ID, auth.RoleViewer, false); !errors.Is(err, auth.ErrLastOwner) {
		t.Fatalf("last Owner update error=%v", err)
	}
	storedB, found, err := repository.ByID(ctx, ownerB.ID)
	if err != nil || !found || storedB.Role != auth.RoleOwner || storedB.Disabled {
		t.Fatalf("owner B after rejected update=%+v found=%t err=%v", storedB, found, err)
	}
	assertCoreCounts(t, databases, 2, 0, 3)
}

func TestAuthRepositoryRejectsMismatchedAuditWithoutWriting(t *testing.T) {
	databases := openTestDatabases(t)
	repository, err := NewAuthRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	event, err := audit.NewEvent(time.Now(), "different", "auth.user.bootstrap", "user", "owner-id")
	if err != nil {
		t.Fatal(err)
	}
	err = repository.CreateInitialOwner(context.Background(), auth.User{ID: "owner-id", Username: "owner", Role: auth.RoleOwner, PasswordHash: "fixture"}, event)
	if !errors.Is(err, auth.ErrInvalidAccount) {
		t.Fatalf("mismatched audit error=%v", err)
	}
	assertCoreCounts(t, databases, 0, 0, 0)
}

func TestAuthRepositorySerializesConcurrentInitialOwnerCreation(t *testing.T) {
	databases := openTestDatabases(t)
	repository, err := NewAuthRepository(databases.Core)
	if err != nil {
		t.Fatal(err)
	}
	var created, conflicts, failures atomic.Int32
	var group sync.WaitGroup
	for index := range 12 {
		group.Add(1)
		go func() {
			defer group.Done()
			id := "owner-" + string(rune('a'+index))
			event, err := audit.NewEvent(time.Now(), id, "auth.user.bootstrap", "user", id)
			if err != nil {
				failures.Add(1)
				return
			}
			err = repository.CreateInitialOwner(context.Background(), auth.User{ID: id, Username: id, Role: auth.RoleOwner, PasswordHash: "fixture"}, event)
			switch {
			case err == nil:
				created.Add(1)
			case errors.Is(err, auth.ErrOwnerExists):
				conflicts.Add(1)
			default:
				failures.Add(1)
			}
		}()
	}
	group.Wait()
	if created.Load() != 1 || conflicts.Load() != 11 || failures.Load() != 0 {
		t.Fatalf("created=%d conflicts=%d failures=%d", created.Load(), conflicts.Load(), failures.Load())
	}
	assertCoreCounts(t, databases, 1, 0, 1)
}

func assertCoreCounts(t *testing.T, databases *Databases, wantUsers, wantSessions, wantEvents int) {
	t.Helper()
	var users, sessions, events int
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if err := databases.Core.QueryRow(`SELECT COUNT(*) FROM audit_events`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if users != wantUsers || sessions != wantSessions || events != wantEvents {
		t.Fatalf("users=%d sessions=%d events=%d, want %d %d %d", users, sessions, events, wantUsers, wantSessions, wantEvents)
	}
}
