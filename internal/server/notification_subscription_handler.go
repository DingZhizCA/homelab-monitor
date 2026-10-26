/*
Package server exposes authenticated self-service notification subscriptions.
The request shape omits user identity so the application service always binds
reads and writes to the current Session principal.
*/
package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/notifications"
)

type NotificationSubscriptionHandler struct {
	service     *notifications.SubscriptionService
	auth        *AuthHandler
	maxBodySize int64
}

func NewNotificationSubscriptionHandler(service *notifications.SubscriptionService, authHandler *AuthHandler, maxBodySize int64) (*NotificationSubscriptionHandler, error) {
	if service == nil || authHandler == nil || maxBodySize < 1 {
		return nil, errors.New("notification subscription service, auth handler, and positive body limit are required")
	}
	return &NotificationSubscriptionHandler{service: service, auth: authHandler, maxBodySize: maxBodySize}, nil
}

func (h *NotificationSubscriptionHandler) Collection(w http.ResponseWriter, r *http.Request) {
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
		h.save(w, r, principal)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPut)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *NotificationSubscriptionHandler) list(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	query := r.URL.Query()
	limits, exists := query["limit"]
	if !exists || len(limits) != 1 || len(query) != 1 {
		writeError(w, http.StatusBadRequest, "INVALID_NOTIFICATION_SUBSCRIPTION", "one bounded limit is required")
		return
	}
	limit, err := strconv.Atoi(limits[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_NOTIFICATION_SUBSCRIPTION", "limit must be a positive bounded integer")
		return
	}
	subscriptions, err := h.service.List(r.Context(), principal, limit)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Subscriptions []notifications.Subscription `json:"subscriptions"`
	}{subscriptions})
}

func (h *NotificationSubscriptionHandler) save(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "INVALID_NOTIFICATION_SUBSCRIPTION", "notification subscription writes do not accept query parameters")
		return
	}
	var input struct {
		ExpectedRevision uint64 `json:"expected_revision"`
		Subscription     struct {
			DeviceID string                 `json:"device_id"`
			RuleID   string                 `json:"rule_id"`
			Severity alerts.Severity        `json:"severity"`
			Revision uint64                 `json:"revision"`
			Enabled  bool                   `json:"enabled"`
			Channels notifications.Channels `json:"channels"`
		} `json:"subscription"`
	}
	if !decodeSingleJSONBody(w, r, h.maxBodySize, &input, "INVALID_JSON", "request body must be one unambiguous notification subscription object", "") {
		return
	}
	subscription := notifications.Subscription{
		DeviceID: input.Subscription.DeviceID, RuleID: input.Subscription.RuleID,
		Severity: input.Subscription.Severity, Revision: input.Subscription.Revision,
		Enabled: input.Subscription.Enabled, Channels: input.Subscription.Channels,
	}
	if err := h.service.Save(r.Context(), principal, subscription, input.ExpectedRevision); err != nil {
		h.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *NotificationSubscriptionHandler) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		writeError(w, http.StatusForbidden, "USER_REQUIRED", "an authenticated Owner or Viewer is required")
	case errors.Is(err, notifications.ErrInvalidSubscription):
		writeError(w, http.StatusBadRequest, "INVALID_NOTIFICATION_SUBSCRIPTION", "notification subscription is invalid")
	case errors.Is(err, notifications.ErrSubscriptionRuleNotFound):
		writeError(w, http.StatusNotFound, "ALERT_RULE_NOT_FOUND", "the subscribed alert rule was not found")
	case errors.Is(err, notifications.ErrSubscriptionConflict):
		writeError(w, http.StatusConflict, "NOTIFICATION_SUBSCRIPTION_CONFLICT", "notification subscription changed; reload before saving")
	case errors.Is(err, notifications.ErrSubscriptionCapacity):
		writeError(w, http.StatusInsufficientStorage, "NOTIFICATION_SUBSCRIPTION_CAPACITY", "notification subscription capacity has been reached")
	default:
		writeError(w, http.StatusInternalServerError, "NOTIFICATION_SUBSCRIPTION_UNAVAILABLE", "notification subscription operation could not be completed")
	}
}
