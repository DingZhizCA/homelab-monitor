/*
Package server exposes the optional local human-account HTTP boundary.

Mutating requests require HTTPS and an explicitly configured Origin. Session
tokens stay in Secure, HttpOnly cookies and are never returned in JSON bodies.
*/
package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

type authenticatedPrincipalKey struct{}

const (
	humanSessionCookie = "hm_session"
	maxHumanAuthBody   = 16 << 10
)

// LoginRateLimiter decides whether an authentication action may proceed.
type LoginRateLimiter interface {
	Allow(key string) bool
}

// AuthHandler serves local account, login, and session operations.
type AuthHandler struct {
	service       *auth.Service
	limiter       LoginRateLimiter
	sessionTTL    time.Duration
	allowedOrigin map[string]struct{}
}

// NewAuthHandler requires repositories behind the service, explicit limits, and allowed HTTPS Origins.
func NewAuthHandler(service *auth.Service, limiter LoginRateLimiter, sessionTTL time.Duration, allowedOrigins []string) (*AuthHandler, error) {
	if service == nil || limiter == nil || sessionTTL <= 0 || len(allowedOrigins) == 0 {
		return nil, errors.New("auth service, login limiter, positive session TTL, and HTTPS origins are required")
	}
	origins := make(map[string]struct{}, len(allowedOrigins))
	for _, origin := range allowedOrigins {
		normalized, err := normalizeHTTPSOrigin(origin)
		if err != nil {
			return nil, fmt.Errorf("invalid allowed Origin: %w", err)
		}
		origins[normalized] = struct{}{}
	}
	return &AuthHandler{service: service, limiter: limiter, sessionTTL: sessionTTL, allowedOrigin: origins}, nil
}

// Register mounts authentication routes and user-account creation.
func (h *AuthHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/auth/bootstrap", h.Bootstrap)
	mux.HandleFunc("/api/v1/auth/login", h.Login)
	mux.HandleFunc("/api/v1/auth/session", h.Session)
	mux.HandleFunc("/api/v1/auth/logout", h.Logout)
	mux.HandleFunc("/api/v1/auth/users", h.Users)
}

// Bootstrap creates the first Owner only; repository uniqueness must be atomic.
func (h *AuthHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !h.requireMutationOrigin(w, r) {
		return
	}
	var input credentialRequest
	if !decodeAuthBody(w, r, &input) {
		return
	}
	if !h.allowAttempt(w, r, input.Username) {
		return
	}
	if err := h.service.BootstrapOwner(r.Context(), input.Username, input.Password); err != nil {
		switch {
		case errors.Is(err, auth.ErrOwnerExists):
			writeError(w, http.StatusConflict, "OWNER_EXISTS", "initial Owner setup is already complete")
		case errors.Is(err, auth.ErrUsernameExists):
			writeError(w, http.StatusConflict, "USERNAME_EXISTS", "username is already in use")
		case errors.Is(err, auth.ErrInvalidAccount):
			writeError(w, http.StatusBadRequest, "INVALID_ACCOUNT", "account details are invalid")
		default:
			writeError(w, http.StatusInternalServerError, "AUTH_UNAVAILABLE", "account setup could not be completed")
		}
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// Login verifies a human account and sets a protected server-side session cookie.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !h.requireMutationOrigin(w, r) {
		return
	}
	var input credentialRequest
	if !decodeAuthBody(w, r, &input) {
		return
	}
	if !h.allowAttempt(w, r, input.Username) {
		return
	}
	token, expiresAt, err := h.service.Login(r.Context(), input.Username, input.Password, h.sessionTTL)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "username or password is invalid")
		} else {
			writeError(w, http.StatusInternalServerError, "AUTH_UNAVAILABLE", "login could not be completed")
		}
		return
	}
	h.setSessionCookie(w, token, expiresAt)
	writeJSON(w, http.StatusOK, struct {
		Authenticated bool `json:"authenticated"`
	}{true})
}

// Session returns the current authenticated principal without exposing its token.
func (h *AuthHandler) Session(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	if !requireHTTPS(w, r) {
		return
	}
	principal, err := h.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	writePrincipal(w, principal)
}

// Logout revokes the current session and clears the browser cookie.
func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !h.requireMutationOrigin(w, r) {
		return
	}
	cookie, err := r.Cookie(humanSessionCookie)
	if err == nil {
		if err := h.service.Logout(r.Context(), cookie.Value); err != nil && !errors.Is(err, auth.ErrUnauthenticated) {
			writeError(w, http.StatusInternalServerError, "AUTH_UNAVAILABLE", "session revocation failed")
			return
		}
	}
	h.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// CreateUser permits only a current Owner session to create another account.
func (h *AuthHandler) CreateUser(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) || !h.requireMutationOrigin(w, r) {
		return
	}
	principal, err := h.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	if principal.Role != auth.RoleOwner {
		writeError(w, http.StatusForbidden, "OWNER_REQUIRED", "Owner role is required")
		return
	}
	var input createUserRequest
	if !decodeAuthBody(w, r, &input) {
		return
	}
	if !h.allowAttempt(w, r, input.Username) {
		return
	}
	if err := h.service.CreateUser(r.Context(), principal, input.Username, input.Password, input.Role); err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden):
			writeError(w, http.StatusForbidden, "OWNER_REQUIRED", "Owner role is required")
		case errors.Is(err, auth.ErrInvalidAccount):
			writeError(w, http.StatusBadRequest, "INVALID_ACCOUNT", "account details are invalid")
		case errors.Is(err, auth.ErrUsernameExists):
			writeError(w, http.StatusConflict, "USERNAME_EXISTS", "username is already in use")
		default:
			writeError(w, http.StatusInternalServerError, "AUTH_UNAVAILABLE", "account creation could not be completed")
		}
		return
	}
	w.WriteHeader(http.StatusCreated)
}

// Users lists sanitized account summaries for Owners and preserves POST creation.
func (h *AuthHandler) Users(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		h.CreateUser(w, r)
		return
	}
	if r.Method == http.MethodPut {
		h.UpdateUser(w, r)
		return
	}
	if !method(w, r, http.MethodGet) || !requireHTTPS(w, r) {
		return
	}
	principal, err := h.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	users, err := h.service.ListUsers(r.Context(), principal)
	if err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			writeError(w, http.StatusForbidden, "OWNER_REQUIRED", "Owner role is required")
			return
		}
		writeError(w, http.StatusInternalServerError, "AUTH_UNAVAILABLE", "account list could not be loaded")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Users []auth.UserSummary `json:"users"`
	}{users})
}

// UpdateUser changes an account only after HTTPS, Origin, and current Owner checks.
func (h *AuthHandler) UpdateUser(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPut) || !h.requireMutationOrigin(w, r) {
		return
	}
	principal, err := h.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	var input updateUserRequest
	if !decodeAuthBody(w, r, &input) {
		return
	}
	if !h.allowAttempt(w, r, input.UserID) {
		return
	}
	if err := h.service.UpdateUser(r.Context(), principal, input.UserID, input.Role, input.Disabled); err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden):
			writeError(w, http.StatusForbidden, "OWNER_REQUIRED", "Owner role is required")
		case errors.Is(err, auth.ErrInvalidAccount):
			writeError(w, http.StatusBadRequest, "INVALID_ACCOUNT", "account update is invalid")
		case errors.Is(err, auth.ErrLastOwner):
			writeError(w, http.StatusConflict, "LAST_OWNER_REQUIRED", "at least one active Owner account must remain")
		case errors.Is(err, auth.ErrAccountNotFound):
			writeError(w, http.StatusNotFound, "ACCOUNT_NOT_FOUND", "account does not exist")
		default:
			writeError(w, http.StatusInternalServerError, "AUTH_UNAVAILABLE", "account could not be updated")
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// RequireUser rejects requests without a valid HTTPS session.
func (h *AuthHandler) RequireUser(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireHTTPS(w, r) {
			return
		}
		principal, err := h.principal(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
			return
		}
		next.ServeHTTP(w, withAuthenticatedPrincipal(r, principal))
	})
}

// RequireOwner rejects requests without an active Owner session.
func (h *AuthHandler) RequireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireHTTPS(w, r) {
			return
		}
		principal, err := h.principal(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
			return
		}
		if principal.Role != auth.RoleOwner {
			writeError(w, http.StatusForbidden, "OWNER_REQUIRED", "Owner role is required")
			return
		}
		next.ServeHTTP(w, withAuthenticatedPrincipal(r, principal))
	})
}

// RequireOwnerMutation protects a state-changing route with Owner, HTTPS, and Origin checks.
func (h *AuthHandler) RequireOwnerMutation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.requireMutationOrigin(w, r) {
			return
		}
		h.RequireOwner(next).ServeHTTP(w, r)
	})
}

// RequireDeviceViewer protects device routes and limits explicit refresh to Owners.
func (h *AuthHandler) RequireDeviceViewer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !requireHTTPS(w, r) {
			return
		}
		principal, err := h.principal(r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
			return
		}
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/refresh") && principal.Role != auth.RoleOwner {
			writeError(w, http.StatusForbidden, "OWNER_REQUIRED", "Owner role is required for explicit refresh")
			return
		}
		next.ServeHTTP(w, withAuthenticatedPrincipal(r, principal))
	})
}

func withAuthenticatedPrincipal(r *http.Request, principal auth.Principal) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), authenticatedPrincipalKey{}, principal))
}

func authenticatedPrincipal(r *http.Request) (auth.Principal, bool) {
	principal, ok := r.Context().Value(authenticatedPrincipalKey{}).(auth.Principal)
	return principal, ok
}

func (h *AuthHandler) principal(r *http.Request) (auth.Principal, error) {
	if r.TLS == nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	cookie, err := r.Cookie(humanSessionCookie)
	if err != nil {
		return auth.Principal{}, auth.ErrUnauthenticated
	}
	return h.service.ResolveSession(r.Context(), cookie.Value)
}

func (h *AuthHandler) requireMutationOrigin(w http.ResponseWriter, r *http.Request) bool {
	if !requireHTTPS(w, r) {
		return false
	}
	origins := r.Header.Values("Origin")
	if len(origins) != 1 {
		writeError(w, http.StatusForbidden, "ORIGIN_REQUIRED", "exactly one configured HTTPS Origin is required")
		return false
	}
	origin, err := normalizeHTTPSOrigin(origins[0])
	if err != nil {
		writeError(w, http.StatusForbidden, "ORIGIN_REQUIRED", "a configured HTTPS Origin is required")
		return false
	}
	if _, ok := h.allowedOrigin[origin]; !ok {
		writeError(w, http.StatusForbidden, "ORIGIN_REJECTED", "request Origin is not allowed")
		return false
	}
	return true
}

func (h *AuthHandler) allowAttempt(w http.ResponseWriter, r *http.Request, username string) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || host == "" {
		writeError(w, http.StatusBadRequest, "INVALID_CLIENT", "client address is unavailable")
		return false
	}
	digest := sha256.Sum256([]byte(host + "\x00" + username))
	if !h.limiter.Allow(hex.EncodeToString(digest[:])) {
		writeError(w, http.StatusTooManyRequests, "LOGIN_RATE_LIMITED", "authentication attempts are temporarily limited")
		return false
	}
	return true
}

func (h *AuthHandler) setSessionCookie(w http.ResponseWriter, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: humanSessionCookie, Value: token, Path: "/api/v1", Expires: expires,
		HttpOnly: true, Secure: true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *AuthHandler) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name: humanSessionCookie, Value: "", Path: "/api/v1", Expires: time.Unix(1, 0),
		MaxAge: -1, HttpOnly: true, Secure: true, SameSite: http.SameSiteLaxMode,
	})
}

type credentialRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type createUserRequest struct {
	Username string    `json:"username"`
	Password string    `json:"password"`
	Role     auth.Role `json:"role"`
}

type updateUserRequest struct {
	UserID   string    `json:"user_id"`
	Role     auth.Role `json:"role"`
	Disabled bool      `json:"disabled"`
}

func decodeAuthBody(w http.ResponseWriter, r *http.Request, target any) bool {
	return decodeSingleJSONBody(w, r, maxHumanAuthBody, target, "INVALID_JSON", "request body must be one unambiguous authentication JSON object", "")
}

func requireHTTPS(w http.ResponseWriter, r *http.Request) bool {
	if r.TLS == nil {
		writeError(w, http.StatusForbidden, "HTTPS_REQUIRED", "human authentication requires HTTPS")
		return false
	}
	return true
}

func normalizeHTTPSOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("Origin must be an HTTPS origin without path, query, or fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", errors.New("Origin host is required")
	}
	port := parsed.Port()
	if port != "" && port != "443" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "https://" + host, nil
}

func writePrincipal(w http.ResponseWriter, principal auth.Principal) {
	type publicUser struct {
		ID       string    `json:"id"`
		Username string    `json:"username"`
		Role     auth.Role `json:"role"`
	}
	writeJSON(w, http.StatusOK, struct {
		User publicUser `json:"user"`
	}{User: publicUser{principal.UserID, principal.Username, principal.Role}})
}
