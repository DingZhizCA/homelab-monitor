package server

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

const maxMetricsBody = 1 << 20

type Handler struct {
	store    *Store
	broker   *Broker
	presence *Presence
}

func NewHandler(store *Store, broker *Broker, presence ...*Presence) *Handler {
	p := NewPresence()
	if len(presence) > 0 && presence[0] != nil {
		p = presence[0]
	}
	return &Handler{store: store, broker: broker, presence: p}
}

func (h *Handler) Health(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Status string `json:"status"`
	}{"ok"})
}

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxMetricsBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var snapshot metrics.MetricEnvelope
	if err := decoder.Decode(&snapshot); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body must be one valid metrics JSON object")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "INVALID_JSON", "request body must contain exactly one JSON object")
		return
	}
	if err := snapshot.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_METRICS", err.Error())
		return
	}
	state := h.store.Update(snapshot)
	h.broker.Publish(state)
	refreshRequestID := ""
	if !containsActiveDisk(snapshot) {
		refreshRequestID = h.store.TakeDiskRefresh(snapshot.Device.ID)
	}
	mode := "idle"
	activeUntil, active := h.presence.Mode()
	if active {
		mode = "active"
	}
	writeJSON(w, http.StatusAccepted, struct {
		RefreshDisk      bool       `json:"refresh_disk"`
		RefreshRequestID string     `json:"refresh_request_id,omitempty"`
		SamplingMode     string     `json:"sampling_mode"`
		ActiveUntil      *time.Time `json:"active_until,omitempty"`
	}{refreshRequestID != "", refreshRequestID, mode, activeTime(activeUntil, active)})
}

func activeTime(value time.Time, active bool) *time.Time {
	if !active {
		return nil
	}
	return &value
}

// Presence records that at least one visible Dashboard is actively viewing
// monitoring data. The active mode remains valid for 30 minutes.
func (h *Handler) Presence(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	expiresAt := h.presence.Touch()
	writeJSON(w, http.StatusOK, struct {
		SamplingMode string    `json:"sampling_mode"`
		ExpiresAt    time.Time `json:"expires_at"`
	}{"active", expiresAt})
}

func (h *Handler) Devices(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, http.StatusOK, h.store.List())
}

func (h *Handler) Device(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/refresh") {
		h.Refresh(w, r)
		return
	}
	if !method(w, r, http.MethodGet) {
		return
	}
	rawID := strings.TrimPrefix(r.URL.Path, "/api/v1/devices/")
	deviceID, err := url.PathUnescape(rawID)
	if err != nil || deviceID == "" || strings.Contains(deviceID, "/") {
		writeError(w, http.StatusNotFound, "DEVICE_NOT_FOUND", "device not found")
		return
	}
	state, ok := h.store.Get(deviceID)
	if !ok {
		writeError(w, http.StatusNotFound, "DEVICE_NOT_FOUND", "device not found")
		return
	}
	writeJSON(w, http.StatusOK, state)
}

func (h *Handler) Refresh(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var request struct {
		Collectors []string `json:"collectors"`
		AllowWake  bool     `json:"allow_wake"`
	}
	if err := decoder.Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_REFRESH", "request body must be valid JSON")
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "INVALID_REFRESH", "request body must contain exactly one JSON object")
		return
	}
	if !request.AllowWake || len(request.Collectors) != 1 || request.Collectors[0] != "disk" {
		writeError(w, http.StatusBadRequest, "INVALID_REFRESH", "disk refresh requires collectors [\"disk\"] and allow_wake true")
		return
	}
	rawID := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/devices/"), "/refresh")
	deviceID, err := url.PathUnescape(rawID)
	if err != nil || deviceID == "" || strings.Contains(deviceID, "/") {
		writeError(w, http.StatusNotFound, "DEVICE_NOT_FOUND", "device not found")
		return
	}
	job, ok, err := h.store.RequestDiskRefresh(deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "REFRESH_FAILED", "could not create refresh request")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "DEVICE_NOT_FOUND", "device not found")
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		RefreshJob
		Warning string `json:"warning"`
	}{job, "this operation may wake sleeping disks"})
}

func (h *Handler) RefreshStatus(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	requestID := strings.TrimPrefix(r.URL.Path, "/api/v1/refresh/")
	job, ok := h.store.GetRefresh(requestID)
	if !ok {
		writeError(w, http.StatusNotFound, "REFRESH_NOT_FOUND", "refresh request not found")
		return
	}
	writeJSON(w, http.StatusOK, job)
}

func containsActiveDisk(snapshot metrics.MetricEnvelope) bool {
	for _, disk := range snapshot.Disks {
		if disk.CollectionMode == "active" {
			return true
		}
	}
	return false
}

func (h *Handler) Events(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE_UNSUPPORTED", "streaming is unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	updates, unsubscribe := h.broker.Subscribe()
	defer unsubscribe()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()
	for {
		select {
		case state := <-updates:
			data, err := json.Marshal(state)
			if err != nil {
				log.Printf("component=server level=ERROR msg=%q", err)
				continue
			}
			fmt.Fprintf(w, "event: metrics\ndata: %s\n\n", data)
			flusher.Flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": heartbeat\n\n")
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func method(w http.ResponseWriter, r *http.Request, allowed string) bool {
	if r.Method == allowed {
		return true
	}
	w.Header().Set("Allow", allowed)
	writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed")
	return false
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("component=server level=ERROR msg=%q", err)
	}
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}{Error: struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	}{code, message}})
}
