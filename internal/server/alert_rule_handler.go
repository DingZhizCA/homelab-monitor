/*
Package server exposes Owner-managed alert rules over authenticated HTTP.

The handler forwards declarative rules and explicit CAS revisions. It does not
choose thresholds, evaluate incidents, send notifications, or select storage.
*/
package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

// AlertRuleHandler serves the optional Owner rule-management boundary.
type AlertRuleHandler struct {
	service     *alerts.RuleService
	auth        *AuthHandler
	maxBodySize int64
}

// NewAlertRuleHandler requires the rule service, matching auth handler, and a request limit.
func NewAlertRuleHandler(service *alerts.RuleService, authHandler *AuthHandler, maxBodySize int64) (*AlertRuleHandler, error) {
	if service == nil || authHandler == nil || maxBodySize <= 0 {
		return nil, errors.New("alert-rule service, auth handler, and positive body limit are required")
	}
	return &AlertRuleHandler{service: service, auth: authHandler, maxBodySize: maxBodySize}, nil
}

// Collection lists current device rules or saves one explicitly versioned revision.
func (h *AlertRuleHandler) Collection(w http.ResponseWriter, r *http.Request) {
	principal, ok := authenticatedPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid Owner session is required")
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

// Revisions lists immutable history or republishes a historical body as a new revision.
func (h *AlertRuleHandler) Revisions(w http.ResponseWriter, r *http.Request) {
	principal, ok := authenticatedPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid Owner session is required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		query := r.URL.Query()
		deviceIDs, hasDevice := query["device_id"]
		ruleIDs, hasRule := query["rule_id"]
		if !hasDevice || !hasRule || len(deviceIDs) != 1 || len(ruleIDs) != 1 || len(query) != 2 {
			writeError(w, http.StatusBadRequest, "INVALID_ALERT_RULE", "one device_id and rule_id are required")
			return
		}
		revisions, err := h.service.ListRevisions(r.Context(), principal, deviceIDs[0], ruleIDs[0])
		if err != nil {
			h.writeServiceError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, struct {
			Revisions []alerts.Rule `json:"revisions"`
		}{revisions})
	case http.MethodPost:
		if !h.auth.requireMutationOrigin(w, r) {
			return
		}
		var input struct {
			DeviceID        string `json:"device_id"`
			RuleID          string `json:"rule_id"`
			TargetRevision  uint64 `json:"target_revision"`
			NewRevision     uint64 `json:"new_revision"`
			ExpectedCurrent uint64 `json:"expected_current"`
		}
		if !decodeSingleJSONBody(w, r, h.maxBodySize, &input, "INVALID_JSON", "request body must be one unambiguous alert-rule rollback object", "") {
			return
		}
		if err := h.service.RollbackRevision(r.Context(), principal, input.DeviceID, input.RuleID, input.TargetRevision, input.NewRevision, input.ExpectedCurrent); err != nil {
			h.writeServiceError(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	}
}

func (h *AlertRuleHandler) list(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	query := r.URL.Query()
	deviceIDs, exists := query["device_id"]
	if !exists || len(deviceIDs) != 1 || len(query) != 1 || strings.TrimSpace(deviceIDs[0]) == "" || strings.TrimSpace(deviceIDs[0]) != deviceIDs[0] {
		writeError(w, http.StatusBadRequest, "INVALID_ALERT_RULE", "device_id must be non-empty and trimmed")
		return
	}
	deviceID := deviceIDs[0]
	rules, err := h.service.ListCurrent(r.Context(), principal, deviceID)
	if err != nil {
		h.writeServiceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Rules []alerts.Rule `json:"rules"`
	}{rules})
}

func (h *AlertRuleHandler) save(w http.ResponseWriter, r *http.Request, principal auth.Principal) {
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "INVALID_ALERT_RULE", "alert-rule writes do not accept query parameters")
		return
	}
	var input struct {
		ExpectedCurrent uint64      `json:"expected_current"`
		Rule            alerts.Rule `json:"rule"`
	}
	if !decodeSingleJSONBody(w, r, h.maxBodySize, &input, "INVALID_JSON", "request body must be one unambiguous alert-rule object", "") {
		return
	}
	if err := h.service.SaveRevision(r.Context(), principal, input.Rule, input.ExpectedCurrent); err != nil {
		h.writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *AlertRuleHandler) writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, auth.ErrForbidden):
		writeError(w, http.StatusForbidden, "OWNER_REQUIRED", "Owner role is required")
	case errors.Is(err, alerts.ErrInvalidRuleRequest):
		writeError(w, http.StatusBadRequest, "INVALID_ALERT_RULE", "alert rule is invalid")
	case errors.Is(err, alerts.ErrRuleVersionConflict):
		writeError(w, http.StatusConflict, "ALERT_RULE_REVISION_CONFLICT", "alert rule changed; reload before saving")
	case errors.Is(err, alerts.ErrRuleNotFound):
		writeError(w, http.StatusNotFound, "ALERT_RULE_NOT_FOUND", "alert rule revision was not found")
	default:
		writeError(w, http.StatusInternalServerError, "ALERT_RULE_UNAVAILABLE", "alert rule operation could not be completed")
	}
}
