/*
Package main tests optional Server storage assembly without starting a listener
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

	"github.com/DingZhizCA/homelab-monitor/internal/server"
	"github.com/DingZhizCA/homelab-monitor/internal/testutil"
)

type allowTestDevice struct{}

func (allowTestDevice) Authorize(string, string) bool { return true }

func TestOpenMetricStorageIsDisabledOnlyWhenAllOptionsAreEmpty(t *testing.T) {
	databases, handler, err := openMetricStorage(context.Background(), metricStorageConfig{}, nil)
	if err != nil || databases != nil || handler != nil {
		t.Fatalf("disabled databases=%v handler=%v err=%v", databases, handler, err)
	}
	invalid := []metricStorageConfig{
		{directory: "somewhere"},
		{maxSnapshots: 1, maxBodyBytes: 1024},
		{directory: "somewhere", maxSnapshots: 1},
	}
	for _, config := range invalid {
		if databases, handler, err := openMetricStorage(context.Background(), config, nil); err == nil || databases != nil || handler != nil {
			t.Fatalf("partial config %+v returned databases=%v handler=%v err=%v", config, databases, handler, err)
		}
	}
}

func TestOpenMetricStorageBuildsAuthenticatedPersistentBatchHandler(t *testing.T) {
	directory := filepath.Join(testutil.TempDir(t), "storage")
	api := server.NewHandlerWithDeviceAuthorizer(server.NewStore(), server.NewBroker(), allowTestDevice{})
	databases, handler, err := openMetricStorage(context.Background(), metricStorageConfig{
		directory:    directory,
		maxSnapshots: 8,
		maxBodyBytes: 1 << 20,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	if databases == nil || handler == nil {
		t.Fatal("storage runtime was not constructed")
	}
	t.Cleanup(func() { _ = databases.Close() })
	for _, name := range []string{"core.db", "metrics.db"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	router, err := server.NewRouter(server.RouterOptions{API: api, MetricsBatch: handler})
	if err != nil {
		t.Fatal(err)
	}
	body := `{"batch_id":"batch-a","device_id":"node-a","snapshots":[{"schema_version":1,"timestamp":"2026-10-22T01:02:03Z","sampling_mode":"idle","sampling_interval_seconds":30,"device":{"id":"node-a","display_name":"Node A","hostname":"node-a"},"cpu":{"usage_percent":10,"logical_cores":4},"memory":{"total_bytes":1024,"used_bytes":512,"available_bytes":512,"usage_percent":50}}]}`
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
}
