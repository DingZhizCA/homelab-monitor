/*
Package server exposes the descriptor registry and latest-snapshot query API.

Every read requires a human HTTPS session, and the injected metric policy runs
before snapshot access so this endpoint cannot bypass field-level authorization.
*/
package server

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

type MetricQueryHandler struct {
	service  *dashboard.QueryService
	registry *dashboard.MetricRegistry
	auth     *AuthHandler
}

func NewMetricQueryHandler(service *dashboard.QueryService, registry *dashboard.MetricRegistry, authHandler *AuthHandler) (*MetricQueryHandler, error) {
	if service == nil || registry == nil || authHandler == nil {
		return nil, errors.New("metric query service, registry, and auth handler are required")
	}
	return &MetricQueryHandler{service: service, registry: registry, auth: authHandler}, nil
}

func (h *MetricQueryHandler) Descriptors(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) || !requireHTTPS(w, r) {
		return
	}
	if _, ok := h.principal(w, r); !ok {
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Descriptors []dashboard.MetricDescriptor `json:"descriptors"`
	}{h.registry.List()})
}

func (h *MetricQueryHandler) Latest(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) || !requireHTTPS(w, r) {
		return
	}
	principal, ok := h.principal(w, r)
	if !ok {
		return
	}
	query, err := parseMetricQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_METRIC_QUERY", "exactly one descriptor_id and device_id are required")
		return
	}
	result, err := h.service.QueryLatest(r.Context(), principal, query.descriptorID, query.deviceID)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden), errors.Is(err, dashboard.ErrForbidden):
			writeError(w, http.StatusForbidden, "METRIC_FORBIDDEN", "metric access is not permitted")
		case errors.Is(err, dashboard.ErrUnsupportedMetric), errors.Is(err, dashboard.ErrSnapshotUnavailable):
			writeError(w, http.StatusNotFound, "METRIC_UNAVAILABLE", "metric or current snapshot is unavailable")
		case errors.Is(err, dashboard.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, "INVALID_METRIC_QUERY", "metric query parameters are invalid")
		default:
			writeError(w, http.StatusInternalServerError, "METRIC_QUERY_UNAVAILABLE", "metric query could not be completed")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *MetricQueryHandler) principal(w http.ResponseWriter, r *http.Request) (auth.Principal, bool) {
	principal, err := h.auth.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return auth.Principal{}, false
	}
	return principal, true
}

type metricQuery struct{ descriptorID, deviceID string }

func parseMetricQuery(values url.Values) (metricQuery, error) {
	for key := range values {
		if key != "descriptor_id" && key != "device_id" {
			return metricQuery{}, errors.New("unknown metric query parameter")
		}
	}
	descriptors, devices := values["descriptor_id"], values["device_id"]
	if len(descriptors) != 1 || descriptors[0] == "" || len(devices) != 1 || strings.TrimSpace(devices[0]) == "" || strings.TrimSpace(devices[0]) != devices[0] {
		return metricQuery{}, errors.New("descriptor_id and device_id must occur exactly once")
	}
	return metricQuery{descriptorID: descriptors[0], deviceID: devices[0]}, nil
}
