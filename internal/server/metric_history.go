/*
Package server exposes the authenticated HTTP boundary for historical metrics.

Parsing is intentionally strict; the dashboard service owns authorization and
query budgets, while an injected source owns persistence and aggregation.
*/
package server

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

type MetricHistoryHandler struct {
	service *dashboard.HistoryService
	auth    *AuthHandler
}

// NewMetricHistoryHandler requires an explicitly bounded history service and human authentication.
func NewMetricHistoryHandler(service *dashboard.HistoryService, authHandler *AuthHandler) (*MetricHistoryHandler, error) {
	if service == nil || authHandler == nil {
		return nil, errors.New("metric history service and auth handler are required")
	}
	return &MetricHistoryHandler{service: service, auth: authHandler}, nil
}

// History serves one authenticated, bounded historical metric query.
func (h *MetricHistoryHandler) History(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) || !requireHTTPS(w, r) {
		return
	}
	principal, err := h.auth.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	query, err := parseMetricHistoryQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_METRIC_QUERY", "descriptor_id, device_id, from, to, and limit are required exactly once")
		return
	}
	result, err := h.service.Query(r.Context(), principal, query)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden), errors.Is(err, dashboard.ErrForbidden):
			writeError(w, http.StatusForbidden, "METRIC_FORBIDDEN", "metric access is not permitted")
		case errors.Is(err, dashboard.ErrUnsupportedMetric):
			writeError(w, http.StatusNotFound, "METRIC_UNAVAILABLE", "metric descriptor is unavailable")
		case errors.Is(err, dashboard.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, "INVALID_METRIC_QUERY", "metric query parameters exceed valid bounds")
		case errors.Is(err, dashboard.ErrInvalidHistoryPoint), errors.Is(err, dashboard.ErrHistorySourceLimit):
			writeError(w, http.StatusBadGateway, "METRIC_HISTORY_INVALID", "history source returned data outside the query contract")
		default:
			writeError(w, http.StatusInternalServerError, "METRIC_QUERY_UNAVAILABLE", "metric history could not be read")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func parseMetricHistoryQuery(values url.Values) (dashboard.HistoryRequest, error) {
	for key := range values {
		switch key {
		case "descriptor_id", "device_id", "from", "to", "limit":
		default:
			return dashboard.HistoryRequest{}, errors.New("unknown history query parameter")
		}
	}
	getOne := func(key string) (string, error) {
		entries := values[key]
		if len(entries) != 1 || entries[0] == "" {
			return "", errors.New("query parameter must occur exactly once")
		}
		return entries[0], nil
	}
	descriptorID, err := getOne("descriptor_id")
	if err != nil {
		return dashboard.HistoryRequest{}, err
	}
	deviceID, err := getOne("device_id")
	if err != nil {
		return dashboard.HistoryRequest{}, err
	}
	if strings.TrimSpace(deviceID) != deviceID {
		return dashboard.HistoryRequest{}, errors.New("device_id must not have surrounding whitespace")
	}
	fromText, err := getOne("from")
	if err != nil {
		return dashboard.HistoryRequest{}, err
	}
	toText, err := getOne("to")
	if err != nil {
		return dashboard.HistoryRequest{}, err
	}
	limitText, err := getOne("limit")
	if err != nil {
		return dashboard.HistoryRequest{}, err
	}
	from, err := time.Parse(time.RFC3339Nano, fromText)
	if err != nil {
		return dashboard.HistoryRequest{}, errors.New("from must be RFC3339")
	}
	to, err := time.Parse(time.RFC3339Nano, toText)
	if err != nil {
		return dashboard.HistoryRequest{}, errors.New("to must be RFC3339")
	}
	limit, err := strconv.Atoi(limitText)
	if err != nil {
		return dashboard.HistoryRequest{}, errors.New("limit must be an integer")
	}
	return dashboard.HistoryRequest{DescriptorID: descriptorID, DeviceID: deviceID, From: from, To: to, Limit: limit}, nil
}
