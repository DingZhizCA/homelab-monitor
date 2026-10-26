/*
Package server assembles the HTTP routes owned by the modular monolith.

The router connects API handlers and the optional built Dashboard. It does not
construct storage, select authentication policy, or start the HTTP listener.
*/
package server

import (
	"errors"
	"net/http"
)

// RouterOptions contains already-constructed application handlers for routing.
type RouterOptions struct {
	API                 *Handler
	PluginControl       *PluginControlHandler
	PluginArtifacts     *PluginArtifactHandler
	PluginAdmin         *PluginAdminHandler
	CollectionPolicies  *PluginCollectionPolicyHandler
	Auth                *AuthHandler
	Dashboards          *DashboardHandler
	MetricQueries       *MetricQueryHandler
	MetricHistory       *MetricHistoryHandler
	PluginMetricHistory *PluginMetricHistoryHandler
	PluginMetricCatalog *PluginMetricCatalogHandler
	MetricsBatch        *MetricsBatchHandler
	PluginMetricsBatch  *PluginMetricsBatchHandler
	AlertRules          *AlertRuleHandler
	Incidents           *IncidentHandler
	WebDirectory        string
}

// NewRouter registers the stable HTTP API and an optional Dashboard handler.
func NewRouter(options RouterOptions) (http.Handler, error) {
	if options.API == nil {
		return nil, errors.New("server API handler is required")
	}
	if options.PluginAdmin != nil && options.Auth == nil {
		return nil, errors.New("plugin desired-state administration requires human authentication")
	}
	if options.CollectionPolicies != nil && options.Auth == nil {
		return nil, errors.New("plugin collection-policy administration requires human authentication")
	}
	if options.Dashboards != nil && (options.Auth == nil || options.Dashboards.auth != options.Auth) {
		return nil, errors.New("Dashboard routes require the matching human authentication handler")
	}
	if options.MetricQueries != nil && (options.Auth == nil || options.MetricQueries.auth != options.Auth) {
		return nil, errors.New("metric query routes require the matching human authentication handler")
	}
	if options.MetricHistory != nil && (options.Auth == nil || options.MetricHistory.auth != options.Auth) {
		return nil, errors.New("metric history routes require the matching human authentication handler")
	}
	if options.PluginMetricHistory != nil && (options.Auth == nil || options.PluginMetricHistory.auth != options.Auth) {
		return nil, errors.New("plugin metric history routes require the matching human authentication handler")
	}
	if options.PluginMetricCatalog != nil && (options.Auth == nil || options.PluginMetricCatalog.auth != options.Auth) {
		return nil, errors.New("plugin metric catalog route requires the matching human authentication handler")
	}
	if options.MetricsBatch != nil && (options.MetricsBatch.api != options.API || options.API.devices == nil) {
		return nil, errors.New("metric batch route requires the matching device-authenticated Agent API")
	}
	if options.PluginMetricsBatch != nil && (options.PluginMetricsBatch.api != options.API || options.API.devices == nil) {
		return nil, errors.New("plugin metric batch route requires the matching device-authenticated Agent API")
	}
	if options.AlertRules != nil && (options.Auth == nil || options.AlertRules.auth != options.Auth) {
		return nil, errors.New("alert-rule routes require the matching human authentication handler")
	}
	if options.Incidents != nil && (options.Auth == nil || options.Incidents.auth != options.Auth) {
		return nil, errors.New("Incident routes require the matching human authentication handler")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/health", options.API.Health)
	mux.HandleFunc("/api/v1/metrics", options.API.Metrics)
	if options.MetricsBatch != nil {
		mux.HandleFunc("/api/v1/metrics/batch", options.MetricsBatch.Ingest)
	}
	if options.PluginMetricsBatch != nil {
		mux.HandleFunc("/api/v1/plugin-metrics/batch", options.PluginMetricsBatch.Ingest)
	}
	if options.Auth == nil {
		mux.Handle("/api/v1/auth/", http.NotFoundHandler())
		mux.HandleFunc("/api/v1/devices", options.API.Devices)
		mux.HandleFunc("/api/v1/devices/", options.API.Device)
		mux.HandleFunc("/api/v1/events", options.API.Events)
		mux.HandleFunc("/api/v1/presence", options.API.Presence)
		mux.HandleFunc("/api/v1/refresh/", options.API.RefreshStatus)
	} else {
		options.Auth.Register(mux)
		mux.Handle("/api/v1/auth/", http.NotFoundHandler())
		mux.Handle("/api/v1/devices", options.Auth.RequireUser(http.HandlerFunc(options.API.Devices)))
		mux.Handle("/api/v1/devices/", options.Auth.RequireDeviceViewer(http.HandlerFunc(options.API.Device)))
		mux.Handle("/api/v1/events", options.Auth.RequireUser(http.HandlerFunc(options.API.Events)))
		mux.Handle("/api/v1/presence", options.Auth.RequireUser(http.HandlerFunc(options.API.Presence)))
		mux.Handle("/api/v1/refresh/", options.Auth.RequireUser(http.HandlerFunc(options.API.RefreshStatus)))
	}
	if options.PluginControl != nil {
		mux.HandleFunc("/api/v1/agent/plugins/desired", options.PluginControl.Desired)
		mux.HandleFunc("/api/v1/agent/plugins/reported", options.PluginControl.Reported)
	}
	if options.CollectionPolicies != nil {
		mux.HandleFunc("/api/v1/agent/plugins/collection-policies", options.CollectionPolicies.Desired)
	}
	if options.PluginArtifacts != nil {
		mux.HandleFunc("/api/v1/agent/plugins/artifact", options.PluginArtifacts.Download)
	}
	if options.PluginAdmin != nil && options.Auth != nil {
		mux.Handle("/api/v1/owner/plugins/desired", options.Auth.RequireOwnerMutation(http.HandlerFunc(options.PluginAdmin.SetDesired)))
	}
	if options.CollectionPolicies != nil && options.Auth != nil {
		mux.Handle("/api/v1/owner/plugins/collection-policies", options.Auth.RequireOwnerMutation(http.HandlerFunc(options.CollectionPolicies.SetDesired)))
	}
	if options.Dashboards != nil {
		mux.HandleFunc("/api/v1/dashboards", options.Dashboards.Collection)
		mux.HandleFunc("/api/v1/dashboards/", options.Dashboards.Resource)
	}
	if options.MetricQueries != nil {
		mux.HandleFunc("/api/v1/dashboard/metrics/descriptors", options.MetricQueries.Descriptors)
		mux.HandleFunc("/api/v1/dashboard/metrics/latest", options.MetricQueries.Latest)
	}
	if options.MetricHistory != nil {
		mux.HandleFunc("/api/v1/dashboard/metrics/history", options.MetricHistory.History)
	}
	if options.PluginMetricHistory != nil {
		mux.HandleFunc("/api/v1/dashboard/plugin-metrics/history", options.PluginMetricHistory.History)
	}
	if options.PluginMetricCatalog != nil {
		mux.HandleFunc("/api/v1/dashboard/plugin-metrics/descriptors", options.PluginMetricCatalog.Descriptors)
	}
	if options.AlertRules != nil {
		mux.Handle("/api/v1/owner/alert-rules", options.Auth.RequireOwner(http.HandlerFunc(options.AlertRules.Collection)))
		mux.Handle("/api/v1/owner/alert-rules/revisions", options.Auth.RequireOwner(http.HandlerFunc(options.AlertRules.Revisions)))
	}
	if options.Incidents != nil {
		mux.Handle("/api/v1/alerts/incidents", options.Auth.RequireUser(http.HandlerFunc(options.Incidents.Current)))
		mux.Handle("/api/v1/alerts/incidents/history", options.Auth.RequireUser(http.HandlerFunc(options.Incidents.Archives)))
		mux.Handle("/api/v1/owner/alerts/incidents/activate", options.Auth.RequireOwnerMutation(http.HandlerFunc(options.Incidents.Activate)))
	}
	if options.WebDirectory != "" {
		mux.Handle("/", NewSPAHandler(options.WebDirectory))
	}
	return mux, nil
}
