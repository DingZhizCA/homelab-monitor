/*
Package server accepts explicit, bounded batches of historical Agent samples.

The historical writer must append each batch atomically and deduplicate its
caller-supplied ID; the existing single-snapshot endpoint remains unchanged.
*/
package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type MetricBatchWriter interface {
	AppendBatch(context.Context, metrics.Batch) (inserted bool, err error)
}

type MetricsBatchHandler struct {
	api          *Handler
	writer       MetricBatchWriter
	maxSnapshots int
	maxBodyBytes int64
}

func NewMetricsBatchHandler(api *Handler, writer MetricBatchWriter, maxSnapshots int, maxBodyBytes int64) (*MetricsBatchHandler, error) {
	if api == nil || api.devices == nil || api.store == nil || api.broker == nil || api.presence == nil || writer == nil || maxSnapshots < 1 || maxBodyBytes < 1 {
		return nil, errors.New("authenticated Agent API, atomic batch writer, and positive batch limits are required")
	}
	return &MetricsBatchHandler{api: api, writer: writer, maxSnapshots: maxSnapshots, maxBodyBytes: maxBodyBytes}, nil
}

func (h *MetricsBatchHandler) Ingest(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPost) {
		return
	}
	var batch metrics.Batch
	if !decodeSingleJSONBody(w, r, h.maxBodyBytes, &batch, "INVALID_METRIC_BATCH", "request body must be one unambiguous metrics batch", "METRIC_BATCH_TOO_LARGE") {
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
	if err := batch.Validate(h.maxSnapshots); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_METRIC_BATCH", err.Error())
		return
	}
	inserted, err := h.writer.AppendBatch(r.Context(), batch)
	if err != nil {
		if errors.Is(err, metrics.ErrBatchConflict) {
			writeError(w, http.StatusConflict, "METRIC_BATCH_CONFLICT", "batch ID was already used for different sample contents")
			return
		}
		writeError(w, http.StatusInternalServerError, "METRIC_BATCH_UNAVAILABLE", "metric batch could not be stored")
		return
	}
	if inserted {
		for _, snapshot := range batch.Snapshots {
			if state, updated := h.api.store.UpdateIfNewer(snapshot); updated {
				h.api.broker.Publish(state)
			}
		}
	}
	latest := batch.Snapshots[len(batch.Snapshots)-1]
	refreshRequestID := ""
	if inserted && !containsActiveDisk(latest) {
		refreshRequestID = h.api.store.TakeDiskRefresh(batch.DeviceID)
	}
	mode := "idle"
	activeUntil, active := h.api.presence.Mode()
	if active {
		mode = "active"
	}
	writeJSON(w, http.StatusAccepted, struct {
		AcceptedSnapshots int        `json:"accepted_snapshots"`
		Duplicate         bool       `json:"duplicate"`
		RefreshDisk       bool       `json:"refresh_disk"`
		RefreshRequestID  string     `json:"refresh_request_id,omitempty"`
		SamplingMode      string     `json:"sampling_mode"`
		ActiveUntil       *time.Time `json:"active_until,omitempty"`
	}{len(batch.Snapshots), !inserted, refreshRequestID != "", refreshRequestID, mode, activeTime(activeUntil, active)})
}
