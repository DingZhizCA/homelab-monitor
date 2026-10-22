/*
Package main tests optional Server runtime assembly without starting a listener
or touching any Homelab device, network, or production database.
*/
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/server"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

type allowTestDevice struct{}

func (allowTestDevice) Authorize(string, string) bool { return true }

func TestOptionalRuntimesRequireCompleteExplicitConfiguration(t *testing.T) {
	if databases, err := openServerStorage(context.Background(), ""); err != nil || databases != nil {
		t.Fatalf("disabled storage=%v err=%v", databases, err)
	}
	if handler, err := buildMetricBatchRuntime(nil, nil, metricBatchRuntimeConfig{}); err != nil || handler != nil {
		t.Fatalf("disabled metric handler=%v err=%v", handler, err)
	}
	if runtime, err := buildAuthRuntime(nil, nil, authRuntimeConfig{}); err != nil || runtime != nil {
		t.Fatalf("disabled auth runtime=%v err=%v", runtime, err)
	}
	for _, config := range []metricBatchRuntimeConfig{{maxSnapshots: 1}, {maxBodyBytes: 1024}} {
		if handler, err := buildMetricBatchRuntime(nil, nil, config); err == nil || handler != nil {
			t.Fatalf("partial metric config %+v handler=%v err=%v", config, handler, err)
		}
	}
	partialAuth := authRuntimeConfig{origins: []string{"https://monitor.example"}}
	if runtime, err := buildAuthRuntime(nil, nil, partialAuth); err == nil || runtime != nil {
		t.Fatalf("partial auth runtime=%v err=%v", runtime, err)
	}
}

func TestBuildMetricBatchRuntimePersistsAuthenticatedRetries(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := openServerStorage(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	for _, name := range []string{"core.db", "metrics.db"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	api := server.NewHandlerWithDeviceAuthorizer(server.NewStore(), server.NewBroker(), allowTestDevice{})
	handler, err := buildMetricBatchRuntime(databases, api, metricBatchRuntimeConfig{maxSnapshots: 8, maxBodyBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	router, err := server.NewRouter(server.RouterOptions{API: api, MetricsBatch: handler})
	if err != nil {
		t.Fatal(err)
	}
	body := metricBatchFixture()
	for attempt := range 2 {
		request := httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/metrics/batch", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer test-token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("attempt %d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
		wantDuplicate := attempt == 1
		if strings.Contains(response.Body.String(), `"duplicate":true`) != wantDuplicate {
			t.Fatalf("attempt %d response=%s", attempt, response.Body.String())
		}
	}
	var batches, snapshots int
	if err := databases.Metrics.QueryRow(`SELECT COUNT(*) FROM metric_batches`).Scan(&batches); err != nil {
		t.Fatal(err)
	}
	if err := databases.Metrics.QueryRow(`SELECT COUNT(*) FROM metric_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if batches != 1 || snapshots != 1 {
		t.Fatalf("batches=%d snapshots=%d", batches, snapshots)
	}
	restoredStore := server.NewStore()
	restored, err := restoreMetricCurrentState(context.Background(), databases, restoredStore)
	if err != nil || restored != 1 {
		t.Fatalf("restored=%d err=%v", restored, err)
	}
	state, ok := restoredStore.Get("node-a")
	if !ok || state.Snapshot.CPU.UsagePercent != 10 || state.ReceivedAt.IsZero() {
		t.Fatalf("restored state=%+v found=%t", state, ok)
	}
}

func TestBuildAuthRuntimeConnectsBootstrapSessionCurrentAndHistoryRoutes(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	databases, err := openServerStorage(context.Background(), directory)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = databases.Close() })
	store := server.NewStore()
	api := server.NewHandlerWithDeviceAuthorizer(store, server.NewBroker(), allowTestDevice{})
	metricsBatch, err := buildMetricBatchRuntime(databases, api, metricBatchRuntimeConfig{maxSnapshots: 8, maxBodyBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	humanAuth, err := buildAuthRuntime(databases, store, authRuntimeConfig{
		origins: []string{"https://monitor.example"}, sessionTTL: time.Hour,
		loginMaxKeys: 32, loginMaxAttempts: 10, loginWindow: time.Minute,
		historyMaxRange: time.Hour, historyMaxPoints: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	router, err := server.NewRouter(server.RouterOptions{
		API: api, MetricsBatch: metricsBatch, Auth: humanAuth.handler,
		MetricQueries: humanAuth.queries, MetricHistory: humanAuth.history,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/metrics/batch", strings.NewReader(metricBatchFixture()))
	request.Header.Set("Authorization", "Bearer test-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("metric batch status=%d body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "https://monitor.example/api/v1/devices", nil)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous devices status=%d body=%s", response.Code, response.Body.String())
	}

	request = authenticatedMutationRequest(http.MethodPost, "/api/v1/auth/bootstrap", `{"username":"owner","password":"owner-password"}`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("bootstrap status=%d body=%s", response.Code, response.Body.String())
	}
	request = authenticatedMutationRequest(http.MethodPost, "/api/v1/auth/login", `{"username":"owner","password":"owner-password"}`)
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login cookies=%d", len(cookies))
	}

	for _, path := range []string{
		"/api/v1/dashboard/metrics/latest?descriptor_id=cpu.usage_percent&device_id=node-a",
		"/api/v1/dashboard/metrics/history?descriptor_id=cpu.usage_percent&device_id=node-a&from=2026-10-22T01:00:00Z&to=2026-10-22T02:00:00Z&limit=10",
	} {
		request = httptest.NewRequest(http.MethodGet, "https://monitor.example"+path, nil)
		request.AddCookie(cookies[0])
		response = httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"cpu.usage_percent"`) {
			t.Fatalf("path=%s status=%d body=%s", path, response.Code, response.Body.String())
		}
	}
}

func authenticatedMutationRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, "https://monitor.example"+path, strings.NewReader(body))
	request.Header.Set("Origin", "https://monitor.example")
	request.Header.Set("Content-Type", "application/json")
	return request
}

func metricBatchFixture() string {
	return `{"batch_id":"batch-a","device_id":"node-a","snapshots":[{"schema_version":1,"timestamp":"2026-10-22T01:02:03Z","sampling_mode":"idle","sampling_interval_seconds":30,"device":{"id":"node-a","display_name":"Node A","hostname":"node-a"},"cpu":{"usage_percent":10,"logical_cores":4},"memory":{"total_bytes":1024,"used_bytes":512,"available_bytes":512,"usage_percent":50}}]}`
}
