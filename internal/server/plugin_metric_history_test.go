/*
Package server tests the authenticated structured plugin history route.
*/
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
)

type pluginHistorySourceFake struct {
	points []pluginmetrics.HistoricalPoint
}

func (f pluginHistorySourceFake) QueryPluginHistory(context.Context, pluginmetrics.HistoryRequest) ([]pluginmetrics.HistoricalPoint, error) {
	return f.points, nil
}

func TestPluginMetricHistoryRouteRequiresSession(t *testing.T) {
	fixture := newAuthTestFixture(t)
	from := time.Date(2026, 10, 23, 1, 0, 0, 0, time.UTC)
	service, err := pluginmetrics.NewHistoryService(pluginHistorySourceFake{[]pluginmetrics.HistoricalPoint{{ObservedAt: from.Add(time.Minute), Type: "gauge", Unit: "celsius", Value: json.RawMessage(`42`), Quality: "valid", AggregationKind: "raw"}}}, pluginmetrics.HistoryBudget{MaxRange: time.Hour, MaxPoints: 10})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewPluginMetricHistoryHandler(service, fixture.handler)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: fixture.handler, PluginMetricHistory: handler})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/dashboard/plugin-metrics/history?device_id=node-a&plugin_id=smart&plugin_version=1.0.0&collector_id=temperature&metric_id=temperature&from=2026-10-23T01:00:00Z&to=2026-10-23T02:00:00Z&limit=10"
	response := httptest.NewRecorder()
	router.ServeHTTP(response, authRequest(http.MethodGet, "https://monitor.example"+path, ""))
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d", response.Code)
	}
	token, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request := authRequest(http.MethodGet, "https://monitor.example"+path, "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"plugin_version":"1.0.0"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
