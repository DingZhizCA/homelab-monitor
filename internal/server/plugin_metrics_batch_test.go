/*
Package server tests authenticated plugin observation batch ingestion.
*/
package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type pluginMetricBatchWriterFake struct {
	seen map[string]metrics.PluginBatch
}

func (f *pluginMetricBatchWriterFake) AppendPluginBatch(_ context.Context, batch metrics.PluginBatch) (bool, error) {
	if f.seen == nil {
		f.seen = make(map[string]metrics.PluginBatch)
	}
	if _, exists := f.seen[batch.ID]; exists {
		return false, nil
	}
	f.seen[batch.ID] = batch
	return true, nil
}

func TestPluginMetricsBatchHandlerAuthenticatesAndDeduplicates(t *testing.T) {
	credentials := NewDeviceCredentials()
	if err := credentials.Set("node-a", "token"); err != nil {
		t.Fatal(err)
	}
	api := NewHandlerWithDeviceAuthorizer(NewStore(), NewBroker(), credentials)
	handler, err := NewPluginMetricsBatchHandler(api, &pluginMetricBatchWriterFake{}, 1, 1, 4096)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: api, PluginMetricsBatch: handler})
	if err != nil {
		t.Fatal(err)
	}
	batch := serverPluginBatch()
	body, _ := json.Marshal(batch)
	for attempt := 0; attempt < 2; attempt++ {
		request := httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/plugin-metrics/batch", bytes.NewReader(body))
		request.TLS = &tls.ConnectionState{}
		request.Header.Set("Authorization", "Bearer token")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("attempt=%d status=%d body=%s", attempt, response.Code, response.Body.String())
		}
		var result struct {
			Duplicate bool `json:"duplicate"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil || result.Duplicate != (attempt == 1) {
			t.Fatalf("attempt=%d result=%+v err=%v", attempt, result, err)
		}
	}
	request := httptest.NewRequest(http.MethodPost, "http://monitor.example/api/v1/plugin-metrics/batch", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("insecure status=%d", response.Code)
	}
}

func serverPluginBatch() metrics.PluginBatch {
	return metrics.PluginBatch{ID: "plugin-batch-a", DeviceID: "node-a", Observations: []metrics.PluginObservation{{
		SchemaVersion: 1, DeviceID: "node-a", PluginID: "smart", PluginVersion: "1.0.0", CollectorID: "temperature", ObservedAt: time.Now().UTC(),
		Samples: []metrics.PluginMetricObservation{{MetricID: "temperature", Type: "gauge", Unit: "celsius", Value: json.RawMessage(`42`), Quality: "valid"}},
	}}}
}
