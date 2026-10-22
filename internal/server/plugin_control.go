/*
Package server exposes the authenticated Agent plugin reconciliation boundary.

These handlers exchange desired and reported state through a narrow storage
interface; they do not download artifacts, execute plugins, or provide
administrative state mutation routes.
*/
package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

// PluginStateStore is the persistence boundary used by the Agent control handlers.
type PluginStateStore interface {
	DesiredForDevice(deviceID string) []plugins.DesiredState
	SetReportedBatch(states []plugins.ReportedState) error
}

// PluginControlHandler serves device-scoped plugin state polling and reporting.
type PluginControlHandler struct {
	store   PluginStateStore
	devices DeviceAuthorizer
}

// NewPluginControlHandler requires both state storage and device authorization.
func NewPluginControlHandler(store PluginStateStore, devices DeviceAuthorizer) (*PluginControlHandler, error) {
	if store == nil || devices == nil {
		return nil, errors.New("plugin state store and device authorizer are required")
	}
	return &PluginControlHandler{store: store, devices: devices}, nil
}

// Desired returns the desired plugin state for the authenticated device.
// The route is intended for GET /api/v1/agent/plugins/desired?device_id=...
func (h *PluginControlHandler) Desired(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	if r.TLS == nil {
		writeError(w, http.StatusForbidden, "HTTPS_REQUIRED", "device credentials require HTTPS")
		return
	}
	deviceIDs := r.URL.Query()["device_id"]
	if len(deviceIDs) != 1 || strings.TrimSpace(deviceIDs[0]) == "" || strings.TrimSpace(deviceIDs[0]) != deviceIDs[0] {
		writeError(w, http.StatusBadRequest, "INVALID_DEVICE", "exactly one trimmed device_id is required")
		return
	}
	deviceID := deviceIDs[0]
	if !h.authorized(r, deviceID) {
		writeError(w, http.StatusUnauthorized, "DEVICE_UNAUTHORIZED", "device credentials are invalid")
		return
	}
	states := h.store.DesiredForDevice(deviceID)
	if states == nil {
		states = []plugins.DesiredState{}
	}
	writeJSON(w, http.StatusOK, struct {
		DeviceID string                 `json:"device_id"`
		States   []plugins.DesiredState `json:"states"`
	}{deviceID, states})
}

// Reported accepts one batch of device plugin observations atomically.
// The route is intended for POST /api/v1/agent/plugins/reported.
func (h *PluginControlHandler) Reported(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	if r.TLS == nil {
		writeError(w, http.StatusForbidden, "HTTPS_REQUIRED", "device credentials require HTTPS")
		return
	}
	var batch struct {
		DeviceID string                  `json:"device_id"`
		States   []plugins.ReportedState `json:"states"`
	}
	if !decodeSingleJSONBody(w, r, maxMetricsBody, &batch, "INVALID_JSON", "request body must be one unambiguous plugin report object", "") {
		return
	}
	if strings.TrimSpace(batch.DeviceID) == "" || strings.TrimSpace(batch.DeviceID) != batch.DeviceID {
		writeError(w, http.StatusBadRequest, "INVALID_DEVICE", "device_id is required and must be trimmed")
		return
	}
	if !h.authorized(r, batch.DeviceID) {
		writeError(w, http.StatusUnauthorized, "DEVICE_UNAUTHORIZED", "device credentials are invalid")
		return
	}
	for _, state := range batch.States {
		if state.DeviceID != batch.DeviceID {
			writeError(w, http.StatusForbidden, "DEVICE_SCOPE_MISMATCH", "reported state belongs to another device")
			return
		}
	}
	if err := h.store.SetReportedBatch(batch.States); err != nil {
		writeError(w, http.StatusConflict, "INVALID_PLUGIN_STATE", err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		Accepted int `json:"accepted"`
	}{len(batch.States)})
}

func (h *PluginControlHandler) authorized(r *http.Request, deviceID string) bool {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	return ok && h.devices.Authorize(deviceID, token)
}
