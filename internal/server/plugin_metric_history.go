/*
Package server exposes authenticated, bounded raw plugin metric history.

The request keeps plugin identity fields separate and parses one exact value
for every selector so URL ambiguity cannot change the selected series.
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
	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
)

// PluginMetricHistoryHandler serves the plugin-specific history contract.
type PluginMetricHistoryHandler struct {
	service *pluginmetrics.HistoryService
	auth    *AuthHandler
}

// NewPluginMetricHistoryHandler requires matching human authentication.
func NewPluginMetricHistoryHandler(service *pluginmetrics.HistoryService, authHandler *AuthHandler) (*PluginMetricHistoryHandler, error) {
	if service == nil || authHandler == nil {
		return nil, errors.New("plugin metric history service and auth handler are required")
	}
	return &PluginMetricHistoryHandler{service: service, auth: authHandler}, nil
}

// History serves one exact plugin metric source and version.
func (h *PluginMetricHistoryHandler) History(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) || !requireHTTPS(w, r) {
		return
	}
	principal, err := h.auth.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	query, err := parsePluginMetricHistoryQuery(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PLUGIN_METRIC_QUERY", "exact plugin identity, device, range, and limit are required")
		return
	}
	result, err := h.service.Query(r.Context(), principal, query)
	if err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden), errors.Is(err, pluginmetrics.ErrForbidden):
			writeError(w, http.StatusForbidden, "PLUGIN_METRIC_FORBIDDEN", "plugin metric access is not permitted")
		case errors.Is(err, pluginmetrics.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, "INVALID_PLUGIN_METRIC_QUERY", "plugin metric query exceeds valid bounds")
		case errors.Is(err, pluginmetrics.ErrScanLimit):
			writeError(w, http.StatusUnprocessableEntity, "PLUGIN_METRIC_SCAN_LIMIT", "query is too sparse for the configured observation scan budget")
		case errors.Is(err, pluginmetrics.ErrInvalidPoint), errors.Is(err, pluginmetrics.ErrSourceLimit):
			writeError(w, http.StatusBadGateway, "PLUGIN_METRIC_HISTORY_INVALID", "plugin metric history source violated the query contract")
		default:
			writeError(w, http.StatusInternalServerError, "PLUGIN_METRIC_QUERY_UNAVAILABLE", "plugin metric history could not be read")
		}
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func parsePluginMetricHistoryQuery(values url.Values) (pluginmetrics.HistoryRequest, error) {
	allowed := map[string]struct{}{"device_id": {}, "plugin_id": {}, "plugin_version": {}, "collector_id": {}, "metric_id": {}, "from": {}, "to": {}, "limit": {}}
	for key := range values {
		if _, ok := allowed[key]; !ok {
			return pluginmetrics.HistoryRequest{}, errors.New("unknown plugin history parameter")
		}
	}
	one := func(key string) (string, error) {
		entries := values[key]
		if len(entries) != 1 || entries[0] == "" || strings.TrimSpace(entries[0]) != entries[0] {
			return "", errors.New("plugin history parameter must occur exactly once")
		}
		return entries[0], nil
	}
	deviceID, err := one("device_id")
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	pluginID, err := one("plugin_id")
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	version, err := one("plugin_version")
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	collectorID, err := one("collector_id")
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	metricID, err := one("metric_id")
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	fromText, err := one("from")
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	toText, err := one("to")
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	limitText, err := one("limit")
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	from, err := time.Parse(time.RFC3339Nano, fromText)
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	to, err := time.Parse(time.RFC3339Nano, toText)
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	limit, err := strconv.Atoi(limitText)
	if err != nil {
		return pluginmetrics.HistoryRequest{}, err
	}
	return pluginmetrics.HistoryRequest{DeviceID: deviceID, PluginID: pluginID, PluginVersion: version, CollectorID: collectorID, MetricID: metricID, From: from, To: to, Limit: limit}, nil
}
