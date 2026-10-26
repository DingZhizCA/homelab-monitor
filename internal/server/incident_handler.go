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

	"github.com/DingZhizCA/homelab-monitor/internal/alerts"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

// IncidentHandler serves the authenticated current-state query boundary.
type IncidentHandler struct {
	service     *alerts.IncidentService
	activation  *alerts.IncidentActivationService
	auth        *AuthHandler
	maxBodySize int64
}

// NewIncidentHandler requires query and activation services, matching auth, and a request limit.
func NewIncidentHandler(service *alerts.IncidentService, activation *alerts.IncidentActivationService, authHandler *AuthHandler, maxBodySize int64) (*IncidentHandler, error) {
	if service == nil || activation == nil || authHandler == nil || maxBodySize < 1 {
		return nil, errors.New("Incident query and activation services, auth handler, and positive body limit are required")
	}
	return &IncidentHandler{service: service, activation: activation, auth: authHandler, maxBodySize: maxBodySize}, nil
}

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
