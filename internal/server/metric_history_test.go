/*
Package server verifies the authenticated HTTP contract for metric history.

Fixtures use in-memory points and an explicit short query budget.
*/
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

type metricHistorySourceFake struct{ points []dashboard.HistoricalPoint }

func (f metricHistorySourceFake) QueryHistory(_ context.Context, _ dashboard.HistoryRequest) ([]dashboard.HistoricalPoint, error) {
	return f.points, nil
}

func TestMetricHistoryRouteRequiresSessionAndReturnsBoundedResults(t *testing.T) {
	fixture := newAuthTestFixture(t)
	registry := dashboard.NewMetricRegistry()
	from := time.Date(2026, 10, 18, 10, 0, 0, 0, time.UTC)
	history, err := dashboard.NewHistoryService(metricHistorySourceFake{points: []dashboard.HistoricalPoint{{
		DeviceID: "node-a", DescriptorID: "network.rx_bytes_per_second", SampledAt: from.Add(time.Minute), Quality: "valid", Dimensions: map[string]string{"interface": "eth0"},
	}}}, metricQueryPolicy{}, registry, dashboard.HistoryBudget{MaxRange: time.Hour, MaxPoints: 100})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewMetricHistoryHandler(history, fixture.handler)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), Auth: fixture.handler, MetricHistory: handler})
	if err != nil {
		t.Fatal(err)
	}
	path := "/api/v1/dashboard/metrics/history?descriptor_id=network.rx_bytes_per_second&device_id=node-a&from=2026-10-18T10:00:00Z&to=2026-10-18T11:00:00Z&limit=20"
	request := authRequest(http.MethodGet, "https://monitor.example"+path, "")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous history status=%d body=%s", response.Code, response.Body.String())
	}
	token, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request = authRequest(http.MethodGet, "https://monitor.example"+path, "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"descriptor_id":"network.rx_bytes_per_second"`) || !strings.Contains(response.Body.String(), `"interface":"eth0"`) {
		t.Fatalf("authenticated history status=%d body=%s", response.Code, response.Body.String())
	}
	request = authRequest(http.MethodGet, "https://monitor.example"+path+"&limit=101", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("over-budget history status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMetricHistoryRouterRequiresMatchingAuthenticationHandler(t *testing.T) {
	fixture := newAuthTestFixture(t)
	history, err := dashboard.NewHistoryService(metricHistorySourceFake{}, metricQueryPolicy{}, dashboard.NewMetricRegistry(), dashboard.HistoryBudget{MaxRange: time.Hour, MaxPoints: 10})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewMetricHistoryHandler(history, fixture.handler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), MetricHistory: handler}); err == nil {
		t.Fatal("metric history route mounted without its auth handler")
	}
}

func TestParseMetricHistoryQueryRejectsDuplicatesUnknownsAndMalformedValues(t *testing.T) {
	valid := url.Values{
		"descriptor_id": {"cpu.usage_percent"},
		"device_id":     {"node-a"},
		"from":          {"2026-10-18T10:00:00Z"},
		"to":            {"2026-10-18T11:00:00Z"},
		"limit":         {"10"},
	}
	if _, err := parseMetricHistoryQuery(valid); err != nil {
		t.Fatalf("valid history query rejected: %v", err)
	}
	invalid := []struct {
		name   string
		values url.Values
	}{
		{"duplicate limit", cloneURLValues(valid)},
		{"unknown parameter", cloneURLValues(valid)},
		{"malformed timestamp", cloneURLValues(valid)},
		{"malformed limit", cloneURLValues(valid)},
	}
	invalid[0].values.Add("limit", "20")
	invalid[1].values.Set("offset", "1")
	invalid[2].values.Set("from", "yesterday")
	invalid[3].values.Set("limit", "ten")
	untrimmedDevice := cloneURLValues(valid)
	untrimmedDevice.Set("device_id", " node-a ")
	invalid = append(invalid, struct {
		name   string
		values url.Values
	}{"untrimmed device ID", untrimmedDevice})
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseMetricHistoryQuery(test.values); err == nil {
				t.Fatal("invalid history query was accepted")
			}
		})
	}
}

func cloneURLValues(values url.Values) url.Values {
	cloned := make(url.Values, len(values))
	for key, entries := range values {
		cloned[key] = append([]string(nil), entries...)
	}
	return cloned
}
