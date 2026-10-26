/*
Package server exposes bounded in-app notification reads and authenticated
read acknowledgement without accepting user identity from HTTP clients.
*/
package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

type InAppNotificationHandler struct {
	service     *notifications.InAppService
	auth        *AuthHandler
	maxBodySize int64
}

func NewInAppNotificationHandler(service *notifications.InAppService, authHandler *AuthHandler, maxBodySize int64) (*InAppNotificationHandler, error) {
	if service == nil || authHandler == nil || maxBodySize < 1 {
		return nil, errors.New("in-app notification service, auth handler, and body limit are required")
	}
	return &InAppNotificationHandler{service: service, auth: authHandler, maxBodySize: maxBodySize}, nil
}

func (h *InAppNotificationHandler) Collection(w http.ResponseWriter, r *http.Request) {
	principal, ok := authenticatedPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.list(w, r, principal)
	case http.MethodPut:
		if !h.auth.requireMutationOrigin(w, r) {
			return
		}
		h.markRead(w, r, principal)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *InAppNotificationHandler) list(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	values, ok := r.URL.Query()["limit"]
	if !ok || len(values) != 1 || len(r.URL.Query()) != 1 {
		writeError(w, http.StatusBadRequest, "INVALID_NOTIFICATION_QUERY", "one bounded limit is required")
		return
	}
	limit, err := strconv.Atoi(values[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_NOTIFICATION_QUERY", "limit must be an integer")
		return
	}
	items, err := h.service.List(r.Context(), principal, limit)
	if err != nil {
		h.writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Notifications []notifications.InAppNotification `json:"notifications"`
	}{items})
}

func (h *InAppNotificationHandler) markRead(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "INVALID_NOTIFICATION", "read updates do not accept query parameters")
		return
	}
	var input struct {
		NotificationID string `json:"notification_id"`
	}
	if !decodeSingleJSONBody(w, r, h.maxBodySize, &input, "INVALID_JSON", "request body must contain one notification ID", "") {
		return
	}
	if err := h.service.MarkRead(r.Context(), principal, input.NotificationID); err != nil {
		h.writeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *InAppNotificationHandler) writeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		writeError(w, http.StatusForbidden, "USER_REQUIRED", "an authenticated user is required")
	case errors.Is(err, notifications.ErrNotificationNotFound):
		writeError(w, http.StatusNotFound, "NOTIFICATION_NOT_FOUND", "notification was not found")
	default:
		writeError(w, http.StatusInternalServerError, "NOTIFICATIONS_UNAVAILABLE", "notification operation failed")
	}
}
