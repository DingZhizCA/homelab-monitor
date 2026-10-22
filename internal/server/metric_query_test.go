/*
Package server tests authenticated metric descriptor and latest-snapshot routes.

The query reads an in-memory snapshot and the test policy never triggers collection.
*/
package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type metricQueryPolicy struct{ err error }

func (p metricQueryPolicy) AuthorizeMetric(auth.Principal, string, string) error { return p.err }

func TestMetricQueryRoutesRequireSessionAndExplicitMetricAuthorization(t *testing.T) {
	fixture := newAuthTestFixture(t)
	registry := dashboard.NewMetricRegistry()
	store := NewStore()
	store.Update(metrics.MetricEnvelope{
		SchemaVersion: 1, SamplingMode: "idle", SamplingIntervalSeconds: 60,
		Device: metrics.DeviceInfo{ID: "node-a"}, Timestamp: time.Now().UTC(),
		CPU: metrics.CPUMetrics{UsagePercent: 23.5},
	})
	service, err := dashboard.NewQueryService(store, metricQueryPolicy{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	queryHandler, err := NewMetricQueryHandler(service, registry, fixture.handler)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: NewHandler(store, NewBroker()), Auth: fixture.handler, MetricQueries: queryHandler})
	if err != nil {
		t.Fatal(err)
	}

	request := authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboard/metrics/descriptors", "")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous descriptor status=%d body=%s", response.Code, response.Body.String())
	}

	token, _, err := fixture.service.Login(context.Background(), "owner", "owner-password", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	request = authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboard/metrics/latest?descriptor_id=cpu.usage_percent&device_id=node-a", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"value":23.5`) || !strings.Contains(response.Body.String(), `"quality":"fresh"`) {
		t.Fatalf("latest metric status=%d body=%s", response.Code, response.Body.String())
	}
	request = authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboard/metrics/latest?descriptor_id=cpu.usage_percent&descriptor_id=memory.usage_percent&device_id=node-a", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("duplicate descriptor query status=%d body=%s", response.Code, response.Body.String())
	}
	request = authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboard/metrics/latest?descriptor_id=cpu.usage_percent&device_id=%20node-a%20", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("untrimmed device query status=%d body=%s", response.Code, response.Body.String())
	}

	deniedService, err := dashboard.NewQueryService(store, metricQueryPolicy{err: auth.ErrForbidden}, registry)
	if err != nil {
		t.Fatal(err)
	}
	deniedHandler, err := NewMetricQueryHandler(deniedService, registry, fixture.handler)
	if err != nil {
		t.Fatal(err)
	}
	deniedRouter, err := NewRouter(RouterOptions{API: NewHandler(store, NewBroker()), Auth: fixture.handler, MetricQueries: deniedHandler})
	if err != nil {
		t.Fatal(err)
	}
	request = authRequest(http.MethodGet, "https://monitor.example/api/v1/dashboard/metrics/latest?descriptor_id=cpu.usage_percent&device_id=node-a", "")
	request.AddCookie(&http.Cookie{Name: humanSessionCookie, Value: token})
	response = httptest.NewRecorder()
	deniedRouter.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("denied metric route status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestMetricQueryRouterRequiresMatchingAuthenticationHandler(t *testing.T) {
	fixture := newAuthTestFixture(t)
	registry := dashboard.NewMetricRegistry()
	service, err := dashboard.NewQueryService(NewStore(), metricQueryPolicy{}, registry)
	if err != nil {
		t.Fatal(err)
	}
	handler, err := NewMetricQueryHandler(service, registry, fixture.handler)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewRouter(RouterOptions{API: NewHandler(NewStore(), NewBroker()), MetricQueries: handler}); err == nil {
		t.Fatal("metric query routes mounted without their auth handler")
	}
}
