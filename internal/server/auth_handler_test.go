/*
Package server tests HTTPS human authentication handlers and protected routes.

Repositories and rate limits are test-only in-memory implementations; these
tests do not configure the production executable or persistence backend.
*/
package server

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

const testAuthOrigin = "https://monitor.example"

type authTestUsers struct {
	mu     sync.Mutex
	users  map[string]auth.User
	events []audit.Event
}

func newAuthTestUsers() *authTestUsers { return &authTestUsers{users: make(map[string]auth.User)} }

func (m *authTestUsers) CreateInitialOwner(_ context.Context, user auth.User, event audit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, current := range m.users {
		if current.Role == auth.RoleOwner {
			return auth.ErrOwnerExists
		}
	}
	if err := m.create(user); err != nil {
		return err
	}
	m.events = append(m.events, event)
	return nil
}

func (m *authTestUsers) CreateForOwner(_ context.Context, actorID string, user auth.User, event audit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	actor, ok := m.users[actorID]
	if !ok || actor.Disabled || actor.Role != auth.RoleOwner {
		return auth.ErrForbidden
	}
	if err := m.create(user); err != nil {
		return err
	}
	m.events = append(m.events, event)
	return nil
}

func (m *authTestUsers) create(user auth.User) error {
	for _, current := range m.users {
		if current.Username == user.Username {
			return auth.ErrUsernameExists
		}
	}
	m.users[user.ID] = user
	return nil
}

func (m *authTestUsers) ByUsername(_ context.Context, username string) (auth.User, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, user := range m.users {
		if user.Username == username {
			return user, true, nil
		}
	}
	return auth.User{}, false, nil
}

func (m *authTestUsers) ByID(_ context.Context, id string) (auth.User, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	user, ok := m.users[id]
	return user, ok, nil
}

func (m *authTestUsers) ListForOwner(_ context.Context, actorID string) ([]auth.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	actor, ok := m.users[actorID]
	if !ok || actor.Disabled || actor.Role != auth.RoleOwner {
		return nil, auth.ErrForbidden
	}
	users := make([]auth.User, 0, len(m.users))
	for _, user := range m.users {
		users = append(users, user)
	}
	return users, nil
}

func (m *authTestUsers) UpdateAccountState(_ context.Context, actorID, id string, role auth.Role, disabled bool, event audit.Event) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	actor, actorExists := m.users[actorID]
	if !actorExists || actor.Disabled || actor.Role != auth.RoleOwner {
		return auth.ErrForbidden
	}
	user, ok := m.users[id]
	if !ok {
		return auth.ErrAccountNotFound
	}
	user.Role, user.Disabled = role, disabled
	activeOwners := 0
	for currentID, current := range m.users {
		if currentID == id {
			current = user
		}
		if current.Role == auth.RoleOwner && !current.Disabled {
			activeOwners++
		}
	}
	if activeOwners == 0 {
		return auth.ErrLastOwner
	}
	m.users[id] = user
	m.events = append(m.events, event)
	return nil
}

type authTestSessions struct {
	mu       sync.Mutex
	sessions map[[sha256.Size]byte]auth.Session
}

func newAuthTestSessions() *authTestSessions {
	return &authTestSessions{sessions: make(map[[sha256.Size]byte]auth.Session)}
}

func (m *authTestSessions) Create(_ context.Context, session auth.Session) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[session.TokenHash] = session
	return nil
}

func (m *authTestSessions) ByTokenHash(_ context.Context, hash [sha256.Size]byte) (auth.Session, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[hash]
	return session, ok, nil
}

func (m *authTestSessions) Revoke(_ context.Context, hash [sha256.Size]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, hash)
	return nil
}

func (m *authTestSessions) RevokeUser(_ context.Context, userID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for hash, session := range m.sessions {
		if session.UserID == userID {
			delete(m.sessions, hash)
		}
	}
	return nil
}

type allowLoginLimiter struct{ allowed bool }

func (l allowLoginLimiter) Allow(string) bool { return l.allowed }

type authTestFixture struct {
	handler  *AuthHandler
	service  *auth.Service
	users    *authTestUsers
	sessions *authTestSessions
}

func newAuthTestFixture(t *testing.T) authTestFixture {
	t.Helper()
	users, sessions := newAuthTestUsers(), newAuthTestSessions()
	service, err := auth.NewService(users, sessions, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.BootstrapOwner(context.Background(), "owner", "owner-password"); err != nil {
		t.Fatal(err)
	}
	limiter := auth.NewFixedWindowLimiter
	loginLimiter, err := limiter(64, 10, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewAuthHandler(service, loginLimiter, time.Hour, []string{testAuthOrigin})
	if err != nil {
		t.Fatal(err)
	}
	return authTestFixture{handler: handler, service: service, users: users, sessions: sessions}
}

func authRequest(method, target, body string) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.RemoteAddr = "192.0.2.10:43210"
	return request
}

func TestAuthHandlerSetsProtectedCookieAndRevokesSession(t *testing.T) {
	fixture := newAuthTestFixture(t)
	request := authRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"owner","password":"owner-password"}`)
	request.Header.Set("Origin", testAuthOrigin)
	response := httptest.NewRecorder()
	fixture.handler.Login(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "token") || strings.Contains(response.Body.String(), "owner-password") {
		t.Fatalf("login response exposed a secret: %s", response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != humanSessionCookie || cookies[0].Path != "/api/v1" || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("session cookie attributes = %+v", cookies)
	}

	sessionRequest := authRequest(http.MethodGet, "/api/v1/auth/session", "")
	sessionRequest.AddCookie(cookies[0])
	sessionResponse := httptest.NewRecorder()
	fixture.handler.Session(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusOK || !strings.Contains(sessionResponse.Body.String(), `"role":"owner"`) {
		t.Fatalf("session response status=%d body=%s", sessionResponse.Code, sessionResponse.Body.String())
	}

	logoutRequest := authRequest(http.MethodPost, "/api/v1/auth/logout", "")
	logoutRequest.Header.Set("Origin", testAuthOrigin)
	logoutRequest.AddCookie(cookies[0])
	logoutResponse := httptest.NewRecorder()
	fixture.handler.Logout(logoutResponse, logoutRequest)
	if logoutResponse.Code != http.StatusNoContent {
		t.Fatalf("logout status=%d body=%s", logoutResponse.Code, logoutResponse.Body.String())
	}
	cleared := logoutResponse.Result().Cookies()
	if len(cleared) != 1 || cleared[0].MaxAge != -1 || !cleared[0].Secure || !cleared[0].HttpOnly {
		t.Fatalf("cleared cookie attributes = %+v", cleared)
	}
	sessionResponse = httptest.NewRecorder()
	fixture.handler.Session(sessionResponse, sessionRequest)
	if sessionResponse.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status = %d", sessionResponse.Code)
	}
}

func TestAuthHandlerRejectsDuplicateCredentialKeys(t *testing.T) {
	fixture := newAuthTestFixture(t)
	request := authRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"owner","username":"attacker","password":"owner-password"}`)
	request.Header.Set("Origin", testAuthOrigin)
	response := httptest.NewRecorder()
	fixture.handler.Login(response, request)
	if response.Code != http.StatusBadRequest || len(fixture.sessions.sessions) != 0 {
		t.Fatalf("duplicate-key login status=%d sessions=%d body=%s", response.Code, len(fixture.sessions.sessions), response.Body.String())
	}
}

func TestAuthHandlerRejectsHTTPAndUntrustedOrigins(t *testing.T) {
	fixture := newAuthTestFixture(t)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"owner","password":"owner-password"}`)),
		authRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"owner","password":"owner-password"}`),
	} {
		if request.TLS != nil {
			request.Header.Set("Origin", "https://evil.example")
		}
		response := httptest.NewRecorder()
		fixture.handler.Login(response, request)
		if response.Code != http.StatusForbidden {
			t.Fatalf("rejected request status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if _, err := NewAuthHandler(fixture.service, allowLoginLimiter{allowed: true}, time.Hour, []string{"http://monitor.example"}); err == nil {
		t.Fatal("non-HTTPS configured Origin was accepted")
	}
}

func TestAuthHandlerLimitAndOwnerOnlyUserCreation(t *testing.T) {
	fixture := newAuthTestFixture(t)
	limited, err := NewAuthHandler(fixture.service, allowLoginLimiter{}, time.Hour, []string{testAuthOrigin})
	if err != nil {
		t.Fatal(err)
	}
	request := authRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"owner","password":"owner-password"}`)
	request.Header.Set("Origin", testAuthOrigin)
	response := httptest.NewRecorder()
	limited.Login(response, request)
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("limited login status=%d body=%s", response.Code, response.Body.String())
	}

	ownerToken, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	viewerPrincipal, err := fixture.service.ResolveSession(context.Background(), ownerToken)
	if err != nil {
		t.Fatal(err)
	}
	if err := fixture.service.CreateUser(context.Background(), viewerPrincipal, "viewer", "viewer-password", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	viewerToken, _, err := fixture.service.Login(context.Background(), "viewer", "viewer-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	viewerRequest := authRequest(http.MethodPost, "/api/v1/auth/users", `{"username":"another","password":"another-password","role":"viewer"}`)
	viewerRequest.Header.Set("Origin", testAuthOrigin)
	viewerRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	viewerResponse := httptest.NewRecorder()
	fixture.handler.CreateUser(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("Viewer account creation status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}
	createRequest := authRequest(http.MethodPost, "/api/v1/auth/users", `{"username":"created","password":"created-password","role":"viewer"}`)
	createRequest.Header.Set("Origin", testAuthOrigin)
	createRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	createResponse := httptest.NewRecorder()
	fixture.handler.CreateUser(createResponse, createRequest)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("Owner user creation status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	ownerRequest := authRequest(http.MethodPost, "/api/v1/auth/users", `{"username":"viewer","password":"duplicate-password","role":"viewer"}`)
	ownerRequest.Header.Set("Origin", testAuthOrigin)
	ownerRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	ownerResponse := httptest.NewRecorder()
	fixture.handler.CreateUser(ownerResponse, ownerRequest)
	if ownerResponse.Code != http.StatusConflict {
		t.Fatalf("duplicate username status=%d body=%s", ownerResponse.Code, ownerResponse.Body.String())
	}
}

func TestAuthHandlerListsAccountsOnlyForOwnerWithoutPasswordVerifiers(t *testing.T) {
	fixture := newAuthTestFixture(t)
	owner, _, err := fixture.users.ByUsername(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerPrincipal := auth.Principal{UserID: owner.ID, Username: owner.Username, Role: auth.RoleOwner}
	if err := fixture.service.CreateUser(context.Background(), ownerPrincipal, "viewer", "viewer-password", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	ownerToken, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, _, err := fixture.service.Login(context.Background(), "viewer", "viewer-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ownerRequest := authRequest(http.MethodGet, "/api/v1/auth/users", "")
	ownerRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	ownerResponse := httptest.NewRecorder()
	fixture.handler.Users(ownerResponse, ownerRequest)
	if ownerResponse.Code != http.StatusOK || !strings.Contains(ownerResponse.Body.String(), `"username":"viewer"`) {
		t.Fatalf("Owner account list status=%d body=%s", ownerResponse.Code, ownerResponse.Body.String())
	}
	if strings.Contains(ownerResponse.Body.String(), "password_hash") || strings.Contains(ownerResponse.Body.String(), "$argon2id$") {
		t.Fatalf("account list exposed a password verifier: %s", ownerResponse.Body.String())
	}
	viewerRequest := authRequest(http.MethodGet, "/api/v1/auth/users", "")
	viewerRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	viewerResponse := httptest.NewRecorder()
	fixture.handler.Users(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("Viewer account list status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}
}

func TestAuthHandlerAccountUpdateRequiresOwnerAndPreservesLastOwner(t *testing.T) {
	fixture := newAuthTestFixture(t)
	owner, _, err := fixture.users.ByUsername(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	ownerPrincipal := auth.Principal{UserID: owner.ID, Username: owner.Username, Role: auth.RoleOwner}
	ownerToken, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	update := func(token, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := authRequest(http.MethodPut, "/api/v1/auth/users", body)
		request.Header.Set("Origin", testAuthOrigin)
		request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
		response := httptest.NewRecorder()
		fixture.handler.Users(response, request)
		return response
	}
	lastOwner := update(ownerToken, `{"user_id":"`+owner.ID+`","role":"viewer","disabled":false}`)
	if lastOwner.Code != http.StatusConflict || !strings.Contains(lastOwner.Body.String(), "LAST_OWNER_REQUIRED") {
		t.Fatalf("last Owner demotion status=%d body=%s", lastOwner.Code, lastOwner.Body.String())
	}
	if err := fixture.service.CreateUser(context.Background(), ownerPrincipal, "owner-b", "owner-b-password", auth.RoleOwner); err != nil {
		t.Fatal(err)
	}
	accepted := update(ownerToken, `{"user_id":"`+owner.ID+`","role":"viewer","disabled":false}`)
	if accepted.Code != http.StatusNoContent {
		t.Fatalf("Owner demotion with backup Owner status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	if user, _, _ := fixture.users.ByID(context.Background(), owner.ID); user.Role != auth.RoleViewer {
		t.Fatalf("account role after authorized update=%q", user.Role)
	}
}

func TestRouterRequiresHumanSessionAndOwnerForDiskRefresh(t *testing.T) {
	fixture := newAuthTestFixture(t)
	owner, _, _ := fixture.users.ByUsername(context.Background(), "owner")
	if err := fixture.service.CreateUser(context.Background(), auth.Principal{UserID: owner.ID, Username: owner.Username, Role: auth.RoleOwner}, "viewer", "viewer-password", auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	viewerToken, _, err := fixture.service.Login(context.Background(), "viewer", "viewer-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore()
	store.Update(testEnvelope())
	api := NewHandler(store, NewBroker())
	router, err := NewRouter(RouterOptions{API: api, Auth: fixture.handler})
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := httptest.NewRecorder()
	router.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/api/v1/devices", nil))
	if unauthorized.Code != http.StatusForbidden {
		t.Fatalf("anonymous devices status = %d", unauthorized.Code)
	}
	viewerRequest := authRequest(http.MethodPost, "/api/v1/devices/node-a/refresh", `{"collectors":["disk"],"allow_wake":true}`)
	viewerRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: viewerToken})
	viewerResponse := httptest.NewRecorder()
	router.ServeHTTP(viewerResponse, viewerRequest)
	if viewerResponse.Code != http.StatusForbidden {
		t.Fatalf("Viewer disk refresh status=%d body=%s", viewerResponse.Code, viewerResponse.Body.String())
	}
	ownerToken, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ownerRequest := authRequest(http.MethodPost, "/api/v1/devices/n1/refresh", `{"collectors":["disk"],"allow_wake":true}`)
	ownerRequest.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: ownerToken})
	ownerResponse := httptest.NewRecorder()
	router.ServeHTTP(ownerResponse, ownerRequest)
	if ownerResponse.Code != http.StatusAccepted {
		t.Fatalf("Owner disk refresh status=%d body=%s", ownerResponse.Code, ownerResponse.Body.String())
	}
	health := httptest.NewRecorder()
	router.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("public health status = %d", health.Code)
	}
}

func TestAuthHandlerBootstrapIsFirstOwnerOnly(t *testing.T) {
	users, sessions := newAuthTestUsers(), newAuthTestSessions()
	service, err := auth.NewService(users, sessions, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	limiter, err := auth.NewFixedWindowLimiter(10, 3, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewAuthHandler(service, limiter, time.Hour, []string{testAuthOrigin})
	if err != nil {
		t.Fatal(err)
	}
	request := authRequest(http.MethodPost, "/api/v1/auth/bootstrap", `{"username":"owner","password":"owner-password"}`)
	request.Header.Set("Origin", testAuthOrigin)
	response := httptest.NewRecorder()
	handler.Bootstrap(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("bootstrap status=%d body=%s", response.Code, response.Body.String())
	}
	request = authRequest(http.MethodPost, "/api/v1/auth/bootstrap", `{"username":"second","password":"second-password"}`)
	request.Header.Set("Origin", testAuthOrigin)
	response = httptest.NewRecorder()
	handler.Bootstrap(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("second bootstrap status=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Error.Code != "OWNER_EXISTS" {
		t.Fatalf("second bootstrap error code = %q", body.Error.Code)
	}
}
