/*
Package auth tests the user and server-side session workflow.

Test repositories stay in memory and verify behavior without selecting the
application's eventual persistent storage implementation.
*/
package auth

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
)

type memoryUsers struct {
	mu     sync.Mutex
	users  map[string]User
	events []audit.Event
}

func newMemoryUsers() *memoryUsers { return &memoryUsers{users: make(map[string]User)} }

func (m *memoryUsers) CreateInitialOwner(_ context.Context, user User, event audit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.users {
		if existing.Role == RoleOwner {
			return ErrOwnerExists
		}
	}
	if err := m.create(user); err != nil {
		return err
	}
	m.events = append(m.events, event)
	return nil
}

func (m *memoryUsers) CreateForOwner(_ context.Context, actorID string, user User, event audit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	actor, ok := m.users[actorID]
	if !ok || actor.Disabled || actor.Role != RoleOwner {
		return ErrForbidden
	}
	if err := m.create(user); err != nil {
		return err
	}
	m.events = append(m.events, event)
	return nil
}

func (m *memoryUsers) create(user User) error {
	for _, existing := range m.users {
		if existing.Username == user.Username {
			return errors.New("username already exists")
		}
	}
	m.users[user.ID] = user
	return nil
}

func (m *memoryUsers) ByUsername(_ context.Context, username string) (User, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, user := range m.users {
		if user.Username == username {
			return user, true, nil
		}
	}
	return User{}, false, nil
}

func (m *memoryUsers) ByID(_ context.Context, id string) (User, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[id]
	return user, ok, nil
}

func (m *memoryUsers) ListForOwner(_ context.Context, actorID string) ([]User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	actor, ok := m.users[actorID]
	if !ok || actor.Disabled || actor.Role != RoleOwner {
		return nil, ErrForbidden
	}
	users := make([]User, 0, len(m.users))
	for _, user := range m.users {
		users = append(users, user)
	}
	return users, nil
}

func (m *memoryUsers) UpdateAccountState(_ context.Context, actorID, id string, role Role, disabled bool, event audit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	actor, actorExists := m.users[actorID]
	if !actorExists || actor.Disabled || actor.Role != RoleOwner {
		return ErrForbidden
	}
	user, ok := m.users[id]
	if !ok {
		return ErrAccountNotFound
	}
	user.Role, user.Disabled = role, disabled
	activeOwners := 0
	for currentID, current := range m.users {
		if currentID == id {
			current = user
		}
		if current.Role == RoleOwner && !current.Disabled {
			activeOwners++
		}
	}
	if activeOwners == 0 {
		return ErrLastOwner
	}
	m.users[id] = user
	m.events = append(m.events, event)
	return nil
}

func (m *memoryUsers) setDisabled(username string, disabled bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, user := range m.users {
		if user.Username == username {
			user.Disabled = disabled
			m.users[id] = user
			return
		}
	}
}

func (m *memoryUsers) setRole(username string, role Role) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, user := range m.users {
		if user.Username == username {
			user.Role = role
			m.users[id] = user
			return
		}
	}
}

func (m *memoryUsers) auditEvents() []audit.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]audit.Event(nil), m.events...)
}

type memorySessions struct {
	mu       sync.Mutex
	sessions map[[sha256.Size]byte]Session
}

func newMemorySessions() *memorySessions {
	return &memorySessions{sessions: make(map[[sha256.Size]byte]Session)}
}

func (m *memorySessions) Create(_ context.Context, session Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.TokenHash] = session
	return nil
}

func (m *memorySessions) ByTokenHash(_ context.Context, hash [sha256.Size]byte) (Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[hash]
	return session, ok, nil
}

func (m *memorySessions) Revoke(_ context.Context, hash [sha256.Size]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, hash)
	return nil
}

func (m *memorySessions) RevokeUser(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for hash, session := range m.sessions {
		if session.UserID == userID {
			delete(m.sessions, hash)
		}
	}
	return nil
}

func (m *memorySessions) countUserSessions(userID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, session := range m.sessions {
		if session.UserID == userID {
			count++
		}
	}
	return count
}

func TestServiceBootstrapLoginResolveAndLogout(t *testing.T) {
	users, sessions := newMemoryUsers(), newMemorySessions()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	service, err := NewService(users, sessions, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := service.BootstrapOwner(ctx, "owner", "owner-password"); err != nil {
		t.Fatal(err)
	}
	if err := service.BootstrapOwner(ctx, "second-owner", "another-password"); !errors.Is(err, ErrOwnerExists) {
		t.Fatalf("second bootstrap error = %v", err)
	}
	token, expiresAt, err := service.Login(ctx, "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if expiresAt != now.Add(time.Hour) {
		t.Fatalf("session expiry = %v", expiresAt)
	}
	principal, err := service.ResolveSession(ctx, token)
	if err != nil || principal.Role != RoleOwner || principal.Username != "owner" {
		t.Fatalf("ResolveSession() = %+v, %v", principal, err)
	}
	if _, stored := sessions.sessions[sha256.Sum256([]byte(token))]; !stored {
		t.Fatal("session token digest was not stored")
	}
	if err := service.Logout(ctx, token); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveSession(ctx, token); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("revoked session error = %v", err)
	}
}

func TestServiceEnforcesRolesExpiryAndSessionRevocation(t *testing.T) {
	users, sessions := newMemoryUsers(), newMemorySessions()
	now := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	service, err := NewService(users, sessions, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := service.BootstrapOwner(ctx, "owner", "owner-password"); err != nil {
		t.Fatal(err)
	}
	owner, _, _ := users.ByUsername(ctx, "owner")
	if err := service.CreateUser(ctx, Principal{UserID: owner.ID, Role: RoleViewer}, "viewer", "viewer-password", RoleViewer); err == nil {
		t.Fatal("Viewer created another account")
	}
	if err := service.CreateUser(ctx, Principal{UserID: owner.ID, Role: RoleOwner}, "viewer", "viewer-password", RoleViewer); err != nil {
		t.Fatal(err)
	}
	users.setRole("owner", RoleViewer)
	if err := service.CreateUser(ctx, Principal{UserID: owner.ID, Role: RoleOwner}, "stale-owner", "some-password", RoleViewer); err == nil {
		t.Fatal("stale Owner principal bypassed current repository role")
	}
	users.setRole("owner", RoleOwner)
	if _, _, err := service.Login(ctx, "owner", "wrong-password", time.Hour); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("invalid login error = %v", err)
	}
	if _, _, err := service.Login(ctx, "missing", "wrong-password", time.Hour); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("unknown account login error = %v", err)
	}
	viewerToken, _, err := service.Login(ctx, "viewer", "viewer-password", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := service.ResolveSession(ctx, viewerToken)
	if err != nil || principal.Role != RoleViewer {
		t.Fatalf("viewer principal = %+v, %v", principal, err)
	}
	if err := service.RevokeUserSessions(ctx, principal.UserID); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ResolveSession(ctx, viewerToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("user-revoked session error = %v", err)
	}
	disabledToken, _, err := service.Login(ctx, "viewer", "viewer-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	users.setDisabled("viewer", true)
	if _, err := service.ResolveSession(ctx, disabledToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("disabled user session error = %v", err)
	}
	if sessions.countUserSessions(principal.UserID) != 0 {
		t.Fatal("disabled user's existing sessions were retained")
	}
	if _, _, err := service.Login(ctx, "viewer", "viewer-password", time.Hour); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("disabled user login error = %v", err)
	}
	ownerToken, _, err := service.Login(ctx, "owner", "owner-password", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Second)
	if _, err := service.ResolveSession(ctx, ownerToken); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("expired session error = %v", err)
	}
}

func TestServiceListsNonSecretUsersOnlyForCurrentOwner(t *testing.T) {
	users, sessions := newMemoryUsers(), newMemorySessions()
	service, err := NewService(users, sessions, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := service.BootstrapOwner(ctx, "owner", "owner-password"); err != nil {
		t.Fatal(err)
	}
	owner, _, err := users.ByUsername(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerPrincipal := Principal{UserID: owner.ID, Role: RoleOwner}
	if err := service.CreateUser(ctx, ownerPrincipal, "viewer", "viewer-password", RoleViewer); err != nil {
		t.Fatal(err)
	}
	listed, err := service.ListUsers(ctx, ownerPrincipal)
	if err != nil || len(listed) != 2 {
		t.Fatalf("listed users=%+v err=%v", listed, err)
	}
	if listed[0].Username != "owner" || listed[1].Username != "viewer" {
		t.Fatalf("users are not returned in stable username order: %+v", listed)
	}
	if _, err := service.ListUsers(ctx, Principal{UserID: owner.ID, Role: RoleViewer}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Viewer list error=%v, want forbidden", err)
	}
	users.setDisabled("owner", true)
	if _, err := service.ListUsers(ctx, ownerPrincipal); !errors.Is(err, ErrForbidden) {
		t.Fatalf("disabled Owner list error=%v, want forbidden", err)
	}
}

func TestStaleOwnerCannotManageAccounts(t *testing.T) {
	users, sessions := newMemoryUsers(), newMemorySessions()
	service, err := NewService(users, sessions, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := service.BootstrapOwner(ctx, "owner", "owner-password"); err != nil {
		t.Fatal(err)
	}
	owner, _, err := users.ByUsername(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	stalePrincipal := Principal{UserID: owner.ID, Role: RoleOwner}
	users.setRole("owner", RoleViewer)
	if err := service.CreateUser(ctx, stalePrincipal, "unauthorized", "long-enough-password", RoleViewer); !errors.Is(err, ErrForbidden) {
		t.Errorf("stale Owner create error=%v, want forbidden", err)
	}
	if _, err := service.ListUsers(ctx, stalePrincipal); !errors.Is(err, ErrForbidden) {
		t.Errorf("stale Owner list error=%v, want forbidden", err)
	}
	if err := service.UpdateUser(ctx, stalePrincipal, owner.ID, RoleOwner, true); !errors.Is(err, ErrForbidden) {
		t.Errorf("stale Owner update error=%v, want forbidden", err)
	}
}

func TestServiceUpdatesAccountsWithLastActiveOwnerProtection(t *testing.T) {
	users, sessions := newMemoryUsers(), newMemorySessions()
	service, err := NewService(users, sessions, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := service.BootstrapOwner(ctx, "owner-a", "owner-password"); err != nil {
		t.Fatal(err)
	}
	owner, _, err := users.ByUsername(ctx, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	ownerPrincipal := Principal{UserID: owner.ID, Role: RoleOwner}
	if err := service.UpdateUser(ctx, ownerPrincipal, owner.ID, RoleViewer, false); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("last Owner demotion error=%v, want last-Owner rejection", err)
	}
	if err := service.CreateUser(ctx, ownerPrincipal, "owner-b", "another-owner-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	if err := service.UpdateUser(ctx, ownerPrincipal, owner.ID, RoleViewer, false); err != nil {
		t.Fatalf("demote Owner while another active Owner exists: %v", err)
	}
	ownerB, _, err := users.ByUsername(ctx, "owner-b")
	if err != nil {
		t.Fatal(err)
	}
	ownerBPrincipal := Principal{UserID: ownerB.ID, Role: RoleOwner}
	if err := service.UpdateUser(ctx, ownerBPrincipal, ownerB.ID, RoleOwner, true); !errors.Is(err, ErrLastOwner) {
		t.Fatalf("last active Owner disable error=%v, want last-Owner rejection", err)
	}
	if err := service.UpdateUser(ctx, ownerBPrincipal, "missing-user", RoleViewer, true); !errors.Is(err, ErrAccountNotFound) {
		t.Fatalf("missing account update error=%v, want not found", err)
	}
	events := users.auditEvents()
	if len(events) != 3 {
		t.Fatalf("successful account audit events=%d, want bootstrap, create, and update only", len(events))
	}
	if events[0].Action != auditBootstrapOwner || events[0].ActorID != owner.ID || events[0].ResourceID != owner.ID {
		t.Fatalf("bootstrap audit event=%+v", events[0])
	}
	if events[1].Action != auditCreateUser || events[1].ActorID != owner.ID || events[1].ResourceID != ownerB.ID {
		t.Fatalf("create audit event=%+v", events[1])
	}
	if events[2].Action != auditUpdateUser || events[2].ActorID != owner.ID || events[2].ResourceID != owner.ID {
		t.Fatalf("update audit event=%+v", events[2])
	}
}

func TestConcurrentOwnerChangesCannotRemoveEveryActiveOwner(t *testing.T) {
	users, sessions := newMemoryUsers(), newMemorySessions()
	service, err := NewService(users, sessions, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := service.BootstrapOwner(ctx, "owner-a", "owner-a-password"); err != nil {
		t.Fatal(err)
	}
	ownerA, _, err := users.ByUsername(ctx, "owner-a")
	if err != nil {
		t.Fatal(err)
	}
	principalA := Principal{UserID: ownerA.ID, Role: RoleOwner}
	if err := service.CreateUser(ctx, principalA, "owner-b", "owner-b-password", RoleOwner); err != nil {
		t.Fatal(err)
	}
	ownerB, _, err := users.ByUsername(ctx, "owner-b")
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, update := range []struct {
		principal Principal
		id        string
	}{{principalA, ownerA.ID}, {Principal{UserID: ownerB.ID, Role: RoleOwner}, ownerB.ID}} {
		group.Add(1)
		go func(principal Principal, id string) {
			defer group.Done()
			results <- service.UpdateUser(ctx, principal, id, RoleViewer, false)
		}(update.principal, update.id)
	}
	group.Wait()
	close(results)
	succeeded, rejected := 0, 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if errors.Is(err, ErrLastOwner) {
			rejected++
		} else {
			t.Fatalf("unexpected concurrent update error: %v", err)
		}
	}
	if succeeded != 1 || rejected != 1 {
		t.Fatalf("concurrent Owner updates succeeded=%d rejected=%d, want one each", succeeded, rejected)
	}
}
