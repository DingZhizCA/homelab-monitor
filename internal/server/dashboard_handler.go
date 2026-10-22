/*
Package server exposes the versioned Dashboard service over authenticated HTTP.

The handler accepts declarative widget documents only; repository and query
authorization remain separate injected boundaries.
*/
package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

type DashboardHandler struct {
	service     *dashboard.Service
	auth        *AuthHandler
	maxBodySize int64
}

func NewDashboardHandler(service *dashboard.Service, authHandler *AuthHandler, maxBodySize int64) (*DashboardHandler, error) {
	if service == nil || authHandler == nil || maxBodySize <= 0 {
		return nil, errors.New("dashboard service, auth handler, and positive body limit are required")
	}
	return &DashboardHandler{service: service, auth: authHandler, maxBodySize: maxBodySize}, nil
}

func (h *DashboardHandler) Collection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !requireHTTPS(w, r) {
			return
		}
		principal, ok := h.requestPrincipal(w, r)
		if !ok {
			return
		}
		items, err := h.service.ListVisible(r.Context(), principal)
		if err != nil {
			h.writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Dashboards []dashboard.Dashboard `json:"dashboards"`
		}{items})
	case http.MethodPost:
		if !h.auth.requireMutationOrigin(w, r) {
			return
		}
		principal, ok := h.requestPrincipal(w, r)
		if !ok {
			return
		}
		var input struct {
			Name     string             `json:"name"`
			Scope    dashboard.Scope    `json:"scope"`
			Document dashboard.Document `json:"document"`
		}
		if !h.decodeBody(w, r, &input) {
			return
		}
		var result dashboard.Dashboard
		var err error
		switch input.Scope {
		case dashboard.ScopePersonal:
			result, err = h.service.CreatePersonal(r.Context(), principal, input.Name, input.Document)
		case dashboard.ScopeShared:
			result, err = h.service.CreateSharedDraft(r.Context(), principal, input.Name, input.Document)
		default:
			writeError(w, http.StatusBadRequest, "INVALID_DASHBOARD", "dashboard scope is invalid")
			return
		}
		if err != nil {
			h.writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, result)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *DashboardHandler) Resource(w http.ResponseWriter, r *http.Request) {
	if !requireHTTPS(w, r) {
		return
	}
	principal, ok := h.requestPrincipal(w, r)
	if !ok {
		return
	}
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/v1/dashboards/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 1 {
		if !method(w, r, http.MethodGet) {
			return
		}
		result, err := h.service.GetVisible(r.Context(), principal, parts[0])
		if err != nil {
			h.writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, result)
		return
	}
	if len(parts) != 2 {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "dashboard route does not exist")
		return
	}
	if parts[1] == "revisions" {
		if !method(w, r, http.MethodGet) {
			return
		}
		revisions, err := h.service.ListPublishedRevisions(r.Context(), principal, parts[0])
		if err != nil {
			h.writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Revisions []dashboard.Revision `json:"revisions"`
		}{revisions})
		return
	}
	expectedMethod := map[string]string{
		"personal": http.MethodPut,
		"draft":    http.MethodPut,
		"publish":  http.MethodPost,
		"rollback": http.MethodPost,
		"copy":     http.MethodPost,
	}[parts[1]]
	if expectedMethod == "" {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "dashboard operation does not exist")
		return
	}
	if !method(w, r, expectedMethod) || !h.auth.requireMutationOrigin(w, r) {
		return
	}
	var result dashboard.Dashboard
	var err error
	switch parts[1] {
	case "personal":
		var input struct {
			ExpectedRevision uint64             `json:"expected_revision"`
			Document         dashboard.Document `json:"document"`
		}
		if !h.decodeBody(w, r, &input) {
			return
		}
		result, err = h.service.SavePersonal(r.Context(), principal, parts[0], input.ExpectedRevision, input.Document)
	case "draft":
		var input struct {
			ExpectedRevision uint64             `json:"expected_revision"`
			Document         dashboard.Document `json:"document"`
		}
		if !h.decodeBody(w, r, &input) {
			return
		}
		result, err = h.service.SaveSharedDraft(r.Context(), principal, parts[0], input.ExpectedRevision, input.Document)
	case "publish":
		var input struct {
			ExpectedDraftRevision uint64 `json:"expected_draft_revision"`
		}
		if !h.decodeBody(w, r, &input) {
			return
		}
		result, err = h.service.PublishShared(r.Context(), principal, parts[0], input.ExpectedDraftRevision)
	case "rollback":
		var input struct {
			TargetPublishedRevision   uint64 `json:"target_published_revision"`
			ExpectedPublishedRevision uint64 `json:"expected_published_revision"`
		}
		if !h.decodeBody(w, r, &input) {
			return
		}
		result, err = h.service.RollbackShared(r.Context(), principal, parts[0], input.TargetPublishedRevision, input.ExpectedPublishedRevision)
	case "copy":
		var input struct {
			Name string `json:"name"`
		}
		if !h.decodeBody(w, r, &input) {
			return
		}
		result, err = h.service.CopyVisibleToPersonal(r.Context(), principal, parts[0], input.Name)
	default:
		writeError(w, http.StatusNotFound, "NOT_FOUND", "dashboard operation does not exist")
		return
	}
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *DashboardHandler) requestPrincipal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, err := h.auth.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return auth.Principal{}, false
	}
	return principal, true
}

func (h *DashboardHandler) decodeBody(w http.ResponseWriter, r *http.Request, target any) bool {
	return decodeSingleJSONBody(w, r, h.maxBodySize, target, "INVALID_JSON", "request body must be one unambiguous Dashboard JSON object", "")
}

func (h *DashboardHandler) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, dashboard.ErrForbidden), errors.Is(err, auth.ErrForbidden):
		writeError(w, http.StatusForbidden, "DASHBOARD_FORBIDDEN", "Dashboard operation is not permitted")
	case errors.Is(err, dashboard.ErrDashboardNotFound):
		writeError(w, http.StatusNotFound, "DASHBOARD_NOT_FOUND", "Dashboard does not exist or is not visible")
	case errors.Is(err, dashboard.ErrVersionConflict):
		writeError(w, http.StatusConflict, "DASHBOARD_REVISION_CONFLICT", "Dashboard changed; reload before saving")
	case errors.Is(err, dashboard.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, "INVALID_DASHBOARD", "Dashboard request is invalid")
	default:
		writeError(w, http.StatusInternalServerError, "DASHBOARD_UNAVAILABLE", "Dashboard operation could not be completed")
	}
}
