/*
Package server exposes the authenticated observed plugin metric descriptor
catalog used to construct exact versioned history selectors.
*/
package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
)

type PluginMetricCatalogHandler struct {
	service *pluginmetrics.CatalogService
	auth    *AuthHandler
}

func NewPluginMetricCatalogHandler(service *pluginmetrics.CatalogService, authHandler *AuthHandler) (*PluginMetricCatalogHandler, error) {
	if service == nil || authHandler == nil {
		return nil, errors.New("plugin metric catalog service and auth handler are required")
	}
	return &PluginMetricCatalogHandler{service: service, auth: authHandler}, nil
}

func (h *PluginMetricCatalogHandler) Descriptors(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) || !requireHTTPS(w, r) {
		return
	}
	principal, err := h.auth.principal(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid session is required")
		return
	}
	if len(r.URL.Query()) != 1 || len(r.URL.Query()["device_id"]) != 1 || strings.TrimSpace(r.URL.Query().Get("device_id")) == "" || strings.TrimSpace(r.URL.Query().Get("device_id")) != r.URL.Query().Get("device_id") {
		writeError(w, http.StatusBadRequest, "INVALID_PLUGIN_METRIC_CATALOG_QUERY", "device_id is required exactly once")
		return
	}
	items, err := h.service.List(r.Context(), principal, r.URL.Query().Get("device_id"))
	if err != nil {
		switch {
		case errors.Is(err, pluginmetrics.ErrForbidden):
			writeError(w, http.StatusForbidden, "PLUGIN_METRIC_FORBIDDEN", "plugin metric access is not permitted")
		case errors.Is(err, pluginmetrics.ErrInvalidRequest):
			writeError(w, http.StatusBadRequest, "INVALID_PLUGIN_METRIC_CATALOG_QUERY", "device_id is invalid")
		case errors.Is(err, pluginmetrics.ErrDescriptorLimit):
			writeError(w, http.StatusUnprocessableEntity, "PLUGIN_METRIC_DESCRIPTOR_LIMIT", "descriptor catalog exceeds its configured limit")
		case errors.Is(err, pluginmetrics.ErrInvalidPoint):
			writeError(w, http.StatusBadGateway, "PLUGIN_METRIC_CATALOG_INVALID", "plugin metric catalog source violated its contract")
		default:
			writeError(w, http.StatusInternalServerError, "PLUGIN_METRIC_CATALOG_UNAVAILABLE", "plugin metric catalog could not be read")
		}
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Descriptors []pluginmetrics.Descriptor `json:"descriptors"`
	}{items})
}
