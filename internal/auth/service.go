/*
Package auth implements local human-user authentication and server-side sessions.

Persistence is supplied through repository interfaces so authentication does
not select a storage engine or share state with Agent device credentials.
*/
package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
)

var (
	ErrOwnerExists                 = errors.New("an owner account already exists")
	ErrInvalidCredentials          = errors.New("invalid username or password")
	ErrUnauthenticated             = errors.New("session is invalid or expired")
	ErrInvalidLimiterConfiguration = errors.New("login limiter requires positive bounds and a clock")
	ErrInvalidAccount              = errors.New("account input is invalid")
	ErrForbidden                   = errors.New("account role does not permit this operation")
	ErrUsernameExists              = errors.New("username already exists")
	ErrAccountNotFound             = errors.New("account does not exist")
	ErrLastOwner                   = errors.New("the last active Owner account cannot be disabled or demoted")
)

// Role names the coarse permissions granted to a human account.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleViewer Role = "viewer"
)

// User contains account metadata and a password verifier for repository use.
type User struct {
	ID           string
	Username     string
	Role         Role
	PasswordHash string
	Disabled     bool
}

// Principal is the non-secret identity returned to authorized application code.
type Principal struct {
	UserID   string
	Username string
	Role     Role
}

// Session stores only a digest of the opaque browser token.
type Session struct {
	TokenHash [sha256.Size]byte
	UserID    string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

// UserRepository owns uniqueness, current-Owner checks, and atomic last-Owner protection.
type UserRepository interface {
	// Mutations must atomically persist the supplied audit event with the user change.
	CreateInitialOwner(context.Context, User, audit.Event) error
	CreateForOwner(context.Context, string, User, audit.Event) error
	ListForOwner(context.Context, string) ([]User, error)
	UpdateAccountState(context.Context, string, string, Role, bool, audit.Event) error
	ByUsername(context.Context, string) (User, bool, error)
	ByID(context.Context, string) (User, bool, error)
}

const (
	auditBootstrapOwner = "auth.user.bootstrap"
	auditCreateUser     = "auth.user.create"
	auditUpdateUser     = "auth.user.update"
)

// UserSummary exposes account state without returning a password verifier.
type UserSummary struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Role     Role   `json:"role"`
	Disabled bool   `json:"disabled"`
}

// SessionRepository persists server-side sessions keyed by token digest.
type SessionRepository interface {
	Create(context.Context, Session) error
	ByTokenHash(context.Context, [sha256.Size]byte) (Session, bool, error)
	Revoke(context.Context, [sha256.Size]byte) error
	RevokeUser(context.Context, string) error
}

// Service coordinates account operations without prescribing repository storage.
type Service struct {
	users         UserRepository
	sessions      SessionRepository
	now           func() time.Time
	dummyVerifier string
}

// NewService requires user and session repositories and an explicit clock.
func NewService(users UserRepository, sessions SessionRepository, now func() time.Time) (*Service, error) {
	if users == nil || sessions == nil || now == nil {
		return nil, errors.New("user repository, session repository, and clock are required")
	}
	dummyVerifier, err := HashPassword("homelab-monitor-invalid-account-check")
	if err != nil {
		return nil, fmt.Errorf("initialize account lookup verifier: %w", err)
	}
	return &Service{users: users, sessions: sessions, now: now, dummyVerifier: dummyVerifier}, nil
}

// BootstrapOwner creates the initial Owner through an atomic repository operation.
func (s *Service) BootstrapOwner(ctx context.Context, username, password string) error {
	if err := validateUsername(username); err != nil {
		return err
	}
	passwordHash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAccount, err)
	}
	userID, err := randomToken(18)
	if err != nil {
		return fmt.Errorf("generate owner ID: %w", err)
	}
	event, err := audit.NewEvent(s.now(), userID, auditBootstrapOwner, "user", userID)
	if err != nil {
		return fmt.Errorf("create bootstrap audit event: %w", err)
	}
	if err := s.users.CreateInitialOwner(ctx, User{ID: userID, Username: username, Role: RoleOwner, PasswordHash: passwordHash}, event); err != nil {
		return fmt.Errorf("create initial owner: %w", err)
	}
	return nil
}

// CreateUser permits an authenticated Owner to add a human account.
func (s *Service) CreateUser(ctx context.Context, actor Principal, username, password string, role Role) error {
	if actor.Role != RoleOwner {
		return ErrForbidden
	}
	if err := validateUsername(username); err != nil {
		return err
	}
	if role != RoleOwner && role != RoleViewer {
		return fmt.Errorf("%w: user role must be owner or viewer", ErrInvalidAccount)
	}
	passwordHash, err := HashPassword(password)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidAccount, err)
	}
	userID, err := randomToken(18)
	if err != nil {
		return fmt.Errorf("generate user ID: %w", err)
	}
	event, err := audit.NewEvent(s.now(), actor.UserID, auditCreateUser, "user", userID)
	if err != nil {
		return fmt.Errorf("create account audit event: %w", err)
	}
	if err := s.users.CreateForOwner(ctx, actor.UserID, User{ID: userID, Username: username, Role: role, PasswordHash: passwordHash}, event); err != nil {
		return fmt.Errorf("create user: %w", err)
	}
	return nil
}

// ListUsers returns stable, non-secret account summaries to a current Owner.
func (s *Service) ListUsers(ctx context.Context, actor Principal) ([]UserSummary, error) {
	if actor.Role != RoleOwner {
		return nil, ErrForbidden
	}
	users, err := s.users.ListForOwner(ctx, actor.UserID)
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	summaries := make([]UserSummary, 0, len(users))
	for _, user := range users {
		summaries = append(summaries, UserSummary{ID: user.ID, Username: user.Username, Role: user.Role, Disabled: user.Disabled})
	}
	slices.SortFunc(summaries, func(left, right UserSummary) int {
		if left.Username < right.Username {
			return -1
		}
		if left.Username > right.Username {
			return 1
		}
		return 0
	})
	return summaries, nil
}

// UpdateUser changes role or enabled state through an atomic last-Owner guard.
func (s *Service) UpdateUser(ctx context.Context, actor Principal, userID string, role Role, disabled bool) error {
	if actor.Role != RoleOwner {
		return ErrForbidden
	}
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(userID) != userID {
		return fmt.Errorf("%w: user_id is required", ErrInvalidAccount)
	}
	if role != RoleOwner && role != RoleViewer {
		return fmt.Errorf("%w: user role must be owner or viewer", ErrInvalidAccount)
	}
	event, err := audit.NewEvent(s.now(), actor.UserID, auditUpdateUser, "user", userID)
	if err != nil {
		return fmt.Errorf("create account audit event: %w", err)
	}
	if err := s.users.UpdateAccountState(ctx, actor.UserID, userID, role, disabled, event); err != nil {
		return fmt.Errorf("update user account: %w", err)
	}
	return nil
}

// Login verifies credentials and returns a one-time plaintext session token.
func (s *Service) Login(ctx context.Context, username, password string, ttl time.Duration) (string, time.Time, error) {
	if validateUsername(username) != nil || password == "" || ttl <= 0 {
		return "", time.Time{}, ErrInvalidCredentials
	}
	user, ok, err := s.users.ByUsername(ctx, username)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("find user: %w", err)
	}
	if !ok || user.Disabled {
		VerifyPassword(s.dummyVerifier, password)
		return "", time.Time{}, ErrInvalidCredentials
	}
	if !VerifyPassword(user.PasswordHash, password) {
		return "", time.Time{}, ErrInvalidCredentials
	}
	plainToken, err := randomToken(32)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate session token: %w", err)
	}
	issuedAt := s.now()
	expiresAt := issuedAt.Add(ttl)
	session := Session{TokenHash: sha256.Sum256([]byte(plainToken)), UserID: user.ID, IssuedAt: issuedAt, ExpiresAt: expiresAt}
	if err := s.sessions.Create(ctx, session); err != nil {
		return "", time.Time{}, fmt.Errorf("create session: %w", err)
	}
	return plainToken, expiresAt, nil
}

// ResolveSession validates a token and returns the user's current role.
func (s *Service) ResolveSession(ctx context.Context, token string) (Principal, error) {
	tokenBytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(tokenBytes) != 32 {
		return Principal{}, ErrUnauthenticated
	}
	session, ok, err := s.sessions.ByTokenHash(ctx, sha256.Sum256([]byte(token)))
	if err != nil {
		return Principal{}, fmt.Errorf("load session: %w", err)
	}
	if !ok || !s.now().Before(session.ExpiresAt) {
		return Principal{}, ErrUnauthenticated
	}
	user, ok, err := s.users.ByID(ctx, session.UserID)
	if err != nil {
		return Principal{}, fmt.Errorf("load session user: %w", err)
	}
	if !ok {
		if err := s.sessions.Revoke(ctx, session.TokenHash); err != nil {
			return Principal{}, fmt.Errorf("revoke orphaned session: %w", err)
		}
		return Principal{}, ErrUnauthenticated
	}
	if user.Disabled || (user.Role != RoleOwner && user.Role != RoleViewer) {
		if err := s.sessions.RevokeUser(ctx, user.ID); err != nil {
			return Principal{}, fmt.Errorf("revoke inactive user sessions: %w", err)
		}
		return Principal{}, ErrUnauthenticated
	}
	return Principal{UserID: user.ID, Username: user.Username, Role: user.Role}, nil
}

// Logout revokes the server-side session represented by the provided token.
func (s *Service) Logout(ctx context.Context, token string) error {
	tokenBytes, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(tokenBytes) != 32 {
		return ErrUnauthenticated
	}
	return s.sessions.Revoke(ctx, sha256.Sum256([]byte(token)))
}

// RevokeUserSessions terminates all active sessions after an account security change.
func (s *Service) RevokeUserSessions(ctx context.Context, userID string) error {
	if strings.TrimSpace(userID) == "" || strings.TrimSpace(userID) != userID {
		return errors.New("valid user ID is required")
	}
	return s.sessions.RevokeUser(ctx, userID)
}

func validateUsername(username string) error {
	if strings.TrimSpace(username) == "" || strings.TrimSpace(username) != username {
		return fmt.Errorf("%w: username must be non-empty and have no surrounding whitespace", ErrInvalidAccount)
	}
	return nil
}

func randomToken(size int) (string, error) {
	data := make([]byte, size)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}
