/*
Package server exposes current Incident states over authenticated HTTP.

The handler accepts a single device scope and delegates authorization and data
access to alerts.IncidentService. It does not expose transition mutations.
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
	service *alerts.IncidentService
	auth    *AuthHandler
}

// NewIncidentHandler requires the query service and matching auth handler.
func NewIncidentHandler(service *alerts.IncidentService, authHandler *AuthHandler) (*IncidentHandler, error) {
	if service == nil || authHandler == nil {
		return nil, errors.New("Incident service and auth handler are required")
	}
	return &IncidentHandler{service: service, auth: authHandler}, nil
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
