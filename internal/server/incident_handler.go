/*
Package server exposes current Incident states and explicit revision activation
over authenticated HTTP.

The read boundary accepts a single device scope. The Owner mutation boundary
archives an older current state before a new Rule Revision can evaluate.
*/
package server

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

// IncidentHandler serves the authenticated current-state query boundary.
type IncidentHandler struct {
	service     *alerts.IncidentService
	activation  *alerts.IncidentActivationService
	archives    *alerts.IncidentArchiveService
	transitions *alerts.IncidentTransitionService
	auth        *AuthHandler
	maxBodySize int64
}

// NewIncidentHandler requires query and activation services, matching auth, and a request limit.
func NewIncidentHandler(service *alerts.IncidentService, activation *alerts.IncidentActivationService, archives *alerts.IncidentArchiveService, transitions *alerts.IncidentTransitionService, authHandler *AuthHandler, maxBodySize int64) (*IncidentHandler, error) {
	if service == nil || activation == nil || archives == nil || authHandler == nil || maxBodySize < 1 {
		return nil, errors.New("Incident query, activation, and archive services, auth handler, and positive body limit are required")
	}
	return &IncidentHandler{service: service, activation: activation, archives: archives, transitions: transitions, auth: authHandler, maxBodySize: maxBodySize}, nil
}

// HasTransitionHistory reports whether the optional query boundary is enabled.
func (h *IncidentHandler) HasTransitionHistory() bool { return h != nil && h.transitions != nil }

// Current lists current Incident transition states for one device.
func (h *IncidentHandler) Current(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	principal, ok := authenticatedPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	query := r.URL.Query()
	deviceIDs, exists := query["device_id"]
	if !exists || len(deviceIDs) != 1 || len(query) != 1 {
		writeError(w, http.StatusBadRequest, "INVALID_INCIDENT_QUERY", "one device_id is required")
		return
	}
	states, err := h.service.ListCurrent(r.Context(), principal, deviceIDs[0])
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden):
			writeError(w, http.StatusForbidden, "USER_REQUIRED", "an authenticated Owner or Viewer is required")
		case errors.Is(err, alerts.ErrInvalidIncidentRequest):
			writeError(w, http.StatusBadRequest, "INVALID_INCIDENT_QUERY", "device_id must be non-empty and trimmed")
		default:
			writeError(w, http.StatusInternalServerError, "INCIDENT_QUERY_UNAVAILABLE", "Incident states could not be loaded")
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Incidents []alerts.IncidentState `json:"incidents"`
	}{states})
}

// Archives lists bounded superseded revisions for one rule.
func (h *IncidentHandler) Archives(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	principal, ok := authenticatedPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	query := r.URL.Query()
	deviceIDs, hasDevice := query["device_id"]
	ruleIDs, hasRule := query["rule_id"]
	limits, hasLimit := query["limit"]
	if !hasDevice || !hasRule || !hasLimit || len(deviceIDs) != 1 || len(ruleIDs) != 1 || len(limits) != 1 || len(query) != 3 {
		writeError(w, http.StatusBadRequest, "INVALID_INCIDENT_ARCHIVE_QUERY", "one device_id, rule_id, and limit are required")
		return
	}
	limit, err := strconv.Atoi(limits[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_INCIDENT_ARCHIVE_QUERY", "limit must be a positive bounded integer")
		return
	}
	archives, err := h.archives.List(r.Context(), principal, deviceIDs[0], ruleIDs[0], limit)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden):
			writeError(w, http.StatusForbidden, "USER_REQUIRED", "an authenticated Owner or Viewer is required")
		case errors.Is(err, alerts.ErrInvalidIncidentRequest):
			writeError(w, http.StatusBadRequest, "INVALID_INCIDENT_ARCHIVE_QUERY", "valid device, rule, and bounded limit are required")
		default:
			writeError(w, http.StatusInternalServerError, "INCIDENT_ARCHIVE_QUERY_UNAVAILABLE", "Incident archives could not be loaded")
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Archives []alerts.IncidentArchive `json:"archives"`
	}{archives})
}

// Transitions lists bounded status changes for one rule.
func (h *IncidentHandler) Transitions(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	principal, ok := authenticatedPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	deviceID, ruleID, limit, ok := incidentHistoryQuery(w, r, "INVALID_INCIDENT_TRANSITION_QUERY")
	if !ok {
		return
	}
	transitions, err := h.transitions.List(r.Context(), principal, deviceID, ruleID, limit)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden):
			writeError(w, http.StatusForbidden, "USER_REQUIRED", "an authenticated Owner or Viewer is required")
		case errors.Is(err, alerts.ErrInvalidIncidentRequest):
			writeError(w, http.StatusBadRequest, "INVALID_INCIDENT_TRANSITION_QUERY", "valid device, rule, and bounded limit are required")
		default:
			writeError(w, http.StatusInternalServerError, "INCIDENT_TRANSITION_QUERY_UNAVAILABLE", "Incident transitions could not be loaded")
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Transitions []alerts.IncidentTransition `json:"transitions"`
	}{transitions})
}

func incidentHistoryQuery(w http.ResponseWriter, r *http.Request, code string) (string, string, int, bool) {
	query := r.URL.Query()
	deviceIDs, hasDevice := query["device_id"]
	ruleIDs, hasRule := query["rule_id"]
	limits, hasLimit := query["limit"]
	if !hasDevice || !hasRule || !hasLimit || len(deviceIDs) != 1 || len(ruleIDs) != 1 || len(limits) != 1 || len(query) != 3 {
		writeError(w, http.StatusBadRequest, code, "one device_id, rule_id, and limit are required")
		return "", "", 0, false
	}
	limit, err := strconv.Atoi(limits[0])
	if err != nil {
		writeError(w, http.StatusBadRequest, code, "limit must be a positive bounded integer")
		return "", "", 0, false
	}
	return deviceIDs[0], ruleIDs[0], limit, true
}

// Activate explicitly archives an older current state for a new rule revision.
func (h *IncidentHandler) Activate(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	principal, ok := authenticatedPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid Owner session is required")
		return
	}
	if r.URL.RawQuery != "" {
		writeError(w, http.StatusBadRequest, "INVALID_INCIDENT_ACTIVATION", "Incident activation does not accept query parameters")
		return
	}
	var input struct {
		DeviceID       string `json:"device_id"`
		RuleID         string `json:"rule_id"`
		TargetRevision uint64 `json:"target_revision"`
	}
	if !decodeSingleJSONBody(w, r, h.maxBodySize, &input, "INVALID_JSON", "request body must be one unambiguous Incident activation object", "") {
		return
	}
	result, err := h.activation.ActivateRevision(r.Context(), principal, input.DeviceID, input.RuleID, input.TargetRevision)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden):
			writeError(w, http.StatusForbidden, "OWNER_REQUIRED", "Owner role is required")
		case errors.Is(err, alerts.ErrInvalidIncidentRequest):
			writeError(w, http.StatusBadRequest, "INVALID_INCIDENT_ACTIVATION", "valid device, rule, and target revision are required")
		case errors.Is(err, alerts.ErrIncidentRuleNotFound):
			writeError(w, http.StatusNotFound, "ALERT_RULE_NOT_FOUND", "the current alert rule was not found")
		case errors.Is(err, alerts.ErrIncidentActivationConflict):
			writeError(w, http.StatusConflict, "INCIDENT_ACTIVATION_CONFLICT", "the rule or Incident state changed; reload before activating")
		case errors.Is(err, alerts.ErrIncidentArchiveCapacity):
			writeError(w, http.StatusInsufficientStorage, "INCIDENT_ARCHIVE_CAPACITY", "Incident archive capacity has been reached")
		default:
			writeError(w, http.StatusInternalServerError, "INCIDENT_ACTIVATION_UNAVAILABLE", "Incident revision could not be activated")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}
