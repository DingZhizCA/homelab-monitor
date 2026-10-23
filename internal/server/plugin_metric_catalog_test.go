/* Package server tests the authenticated observed plugin descriptor route. */
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/pluginmetrics"
)

type pluginDescriptorSourceFake struct{ items []pluginmetrics.Descriptor }

func (f pluginDescriptorSourceFake) ListPluginMetricDescriptors(context.Context, string, int) ([]pluginmetrics.Descriptor, error) {
	return f.items, nil
}

func TestPluginMetricCatalogRouteRequiresSession(t *testing.T) {
	fixture := newAuthTestFixture(t)
	now := time.Now().UTC()
	service, _ := pluginmetrics.NewCatalogService(pluginDescriptorSourceFake{[]pluginmetrics.Descriptor{{DeviceID: "node-a", PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature", MetricID: "temperature", Type: "gauge", Unit: "celsius", LabelKeys: []string{"disk"}, FirstObserved: now, LastObserved: now}}}, 10)
	handler, _ := NewPluginMetricCatalogHandler(service, fixture.handler)
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: fixture.handler, PluginMetricCatalog: handler})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/dashboard/plugin-metrics/descriptors?device_id=node-a"
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
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"plugin_version":"1.0.0"`) || !strings.Contains(response.Body.String(), `"label_keys":["disk"]`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
}
