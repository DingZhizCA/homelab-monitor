/*
Package server accepts authenticated, bounded plugin observation batches.

The endpoint is intentionally independent from built-in metric current state,
SSE publication, and disk refresh control.
*/
package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

// PluginMetricBatchWriter persists one complete retryable plugin batch.
type PluginMetricBatchWriter interface {
	AppendPluginBatch(context.Context, metrics.PluginBatch) (inserted bool, err error)
}

// PluginMetricsBatchHandler owns the plugin observation ingestion boundary.
type PluginMetricsBatchHandler struct {
	api             *Handler
	writer          PluginMetricBatchWriter
	maxObservations int
	maxSamples      int
	maxBodyBytes    int64
}

// NewPluginMetricsBatchHandler requires explicit request and work limits.
func NewPluginMetricsBatchHandler(api *Handler, writer PluginMetricBatchWriter, maxObservations, maxSamples int, maxBodyBytes int64) (*PluginMetricsBatchHandler, error) {
	if api == nil || api.devices == nil || writer == nil || maxObservations < 1 || maxSamples < 1 || maxBodyBytes < 1 {
		return nil, errors.New("device-authenticated API, plugin batch writer, and positive limits are required")
	}
	return &PluginMetricsBatchHandler{api: api, writer: writer, maxObservations: maxObservations, maxSamples: maxSamples, maxBodyBytes: maxBodyBytes}, nil
}

// Ingest validates device identity before committing a plugin batch.
func (h *PluginMetricsBatchHandler) Ingest(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var batch metrics.PluginBatch
	if !decodeSingleJSONBody(w, r, h.maxBodyBytes, &batch, "INVALID_PLUGIN_METRIC_BATCH", "request body must be one unambiguous plugin metric batch", "PLUGIN_METRIC_BATCH_TOO_LARGE") {
		return
	}
	if r.TLS == nil {
		writeError(w, http.StatusForbidden, "HTTPS_REQUIRED", "device credentials require HTTPS")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !h.api.devices.Authorize(batch.DeviceID, token) {
		writeError(w, http.StatusUnauthorized, "DEVICE_UNAUTHORIZED", "device credentials are invalid")
		return
	}
	if err := batch.Validate(h.maxObservations, h.maxSamples); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PLUGIN_METRIC_BATCH", err.Error())
		return
	}
	inserted, err := h.writer.AppendPluginBatch(r.Context(), batch)
	if err != nil {
		switch {
		case errors.Is(err, metrics.ErrBatchConflict):
			writeError(w, http.StatusConflict, "PLUGIN_METRIC_BATCH_CONFLICT", "batch ID was already used for different plugin observations")
		case errors.Is(err, metrics.ErrMetricStorageCapacity):
			writeError(w, http.StatusInsufficientStorage, "METRIC_STORAGE_FULL", "metric storage capacity could not be recovered")
		default:
			writeError(w, http.StatusInternalServerError, "PLUGIN_METRIC_BATCH_UNAVAILABLE", "plugin metric batch could not be stored")
		}
		return
	}
	writeJSON(w, http.StatusAccepted, struct {
		AcceptedObservations int  `json:"accepted_observations"`
		Duplicate            bool `json:"duplicate"`
	}{len(batch.Observations), !inserted})
}
