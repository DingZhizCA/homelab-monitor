/*
Package server tests bounded batch ingestion, authorization, and replay behavior.

The fake historical writer models atomic ID-based de-duplication without choosing storage.
*/
package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type metricBatchWriterFake struct {
	accepted map[string]metrics.Batch
	calls    int
}

func (f *metricBatchWriterFake) AppendBatch(_ context.Context, batch metrics.Batch) (bool, error) {
	f.calls++
	if f.accepted == nil {
		f.accepted = make(map[string]metrics.Batch)
	}
	if existing, ok := f.accepted[batch.ID]; ok {
		if !reflect.DeepEqual(existing, batch) {
			return false, metrics.ErrBatchConflict
		}
		return false, nil
	}
	f.accepted[batch.ID] = batch
	return true, nil
}

func TestMetricsBatchHandlerValidatesAuthorizesAndDeduplicates(t *testing.T) {
	credentials := NewDeviceCredentials()
	if err := credentials.Set("node-a", "batch-device-token"); err != nil {
		t.Fatal(err)
	}
	api := NewHandlerWithDeviceAuthorizer(NewStore(), NewBroker(), credentials)
	writer := &metricBatchWriterFake{}
	handler, err := NewMetricsBatchHandler(api, writer, 2, 8192)
	if err != nil {
		t.Fatal(err)
	}
	router, err := NewRouter(RouterOptions{API: api, MetricsBatch: handler})
	if err != nil {
		t.Fatal(err)
	}
	first := serverBatchEnvelope(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC), 10)
	second := serverBatchEnvelope(first.Timestamp.Add(time.Minute), 30)
	batch := metrics.Batch{ID: "batch-01", DeviceID: "node-a", Snapshots: []metrics.MetricEnvelope{first, second}}
	body, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/metrics/batch", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer batch-device-token")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || writer.calls != 1 {
		t.Fatalf("batch status=%d calls=%d body=%s", response.Code, writer.calls, response.Body.String())
	}
	var result struct {
		AcceptedSnapshots int  `json:"accepted_snapshots"`
		Duplicate         bool `json:"duplicate"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.AcceptedSnapshots != 2 || result.Duplicate {
		t.Fatalf("batch result=%+v", result)
	}
	latest, ok := api.store.Get("node-a")
	if !ok || latest.Snapshot.Timestamp != second.Timestamp || latest.Snapshot.CPU.UsagePercent != 30 {
		t.Fatalf("current snapshot did not advance to batch tail: %+v", latest)
	}

	request = httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/metrics/batch", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer batch-device-token")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusAccepted || writer.calls != 2 {
		t.Fatalf("batch retry status=%d calls=%d body=%s", response.Code, writer.calls, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Duplicate || result.AcceptedSnapshots != 2 {
		t.Fatalf("batch retry result=%+v", result)
	}

	batch.Snapshots[1].Timestamp = batch.Snapshots[0].Timestamp
	body, _ = json.Marshal(batch)
	request = httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/metrics/batch", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer batch-device-token")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || writer.calls != 2 {
		t.Fatalf("invalid batch status=%d calls=%d body=%s", response.Code, writer.calls, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/metrics/batch", strings.NewReader(`{"batch_id":"batch-a","batch_id":"batch-b","device_id":"node-a","snapshots":[]}`))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer batch-device-token")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || writer.calls != 2 {
		t.Fatalf("duplicate-key batch status=%d calls=%d body=%s", response.Code, writer.calls, response.Body.String())
	}
	large := serverBatchEnvelope(time.Now(), 10)
	large.Device.DisplayName = strings.Repeat("n", 9000)
	oversized, _ := json.Marshal(metrics.Batch{ID: "batch-large", DeviceID: "node-a", Snapshots: []metrics.MetricEnvelope{large}})
	request = httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/metrics/batch", bytes.NewReader(oversized))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer batch-device-token")
	response = httptest.NewRecorder()
	router.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || writer.calls != 2 {
		t.Fatalf("oversized batch status=%d calls=%d body=%s", response.Code, writer.calls, response.Body.String())
	}
}

func TestMetricsBatchHandlerRequiresHTTPSAndDeviceAuthorization(t *testing.T) {
	credentials := NewDeviceCredentials()
	if err := credentials.Set("node-a", "batch-device-token"); err != nil {
		t.Fatal(err)
	}
	api := NewHandlerWithDeviceAuthorizer(NewStore(), NewBroker(), credentials)
	handler, err := NewMetricsBatchHandler(api, &metricBatchWriterFake{}, 2, 8192)
	if err != nil {
		t.Fatal(err)
	}
	batch := metrics.Batch{ID: "batch-01", DeviceID: "node-a", Snapshots: []metrics.MetricEnvelope{serverBatchEnvelope(time.Now(), 10)}}
	body, _ := json.Marshal(batch)
	for _, test := range []struct {
		name       string
		secure     bool
		token      string
		wantStatus int
	}{
		{name: "http", wantStatus: http.StatusForbidden},
		{name: "invalid token", secure: true, token: "wrong", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := "http://monitor.example/api/v1/metrics/batch"
			if test.secure {
				target = "https://monitor.example/api/v1/metrics/batch"
			}
			request := httptest.NewRequest(http.MethodPost, target, bytes.NewReader(body))
			if test.secure {
				request.TLS = &tls.ConnectionState{}
			}
			request.Header.Set("Authorization", "Bearer "+test.token)
			response := httptest.NewRecorder()
			handler.Ingest(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
		})
	}
}

func TestMetricsBatchHandlerDoesNotRegressCurrentStateForOutOfOrderBatch(t *testing.T) {
	credentials := NewDeviceCredentials()
	if err := credentials.Set("node-a", "batch-device-token"); err != nil {
		t.Fatal(err)
	}
	broker := NewBroker()
	api := NewHandlerWithDeviceAuthorizer(NewStore(), broker, credentials)
	handler, err := NewMetricsBatchHandler(api, &metricBatchWriterFake{}, 2, 8192)
	if err != nil {
		t.Fatal(err)
	}
	events, unsubscribe := broker.Subscribe()
	defer unsubscribe()
	newer := serverBatchEnvelope(time.Date(2026, 10, 18, 2, 0, 0, 0, time.UTC), 80)
	older := serverBatchEnvelope(newer.Timestamp.Add(-time.Hour), 20)
	for _, batch := range []metrics.Batch{
		{ID: "newer-batch", DeviceID: "node-a", Snapshots: []metrics.MetricEnvelope{newer}},
		{ID: "older-batch", DeviceID: "node-a", Snapshots: []metrics.MetricEnvelope{older}},
	} {
		body, err := json.Marshal(batch)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "https://monitor.example/api/v1/metrics/batch", bytes.NewReader(body))
		request.TLS = &tls.ConnectionState{}
		request.Header.Set("Authorization", "Bearer batch-device-token")
		response := httptest.NewRecorder()
		handler.Ingest(response, request)
		if response.Code != http.StatusAccepted {
			t.Fatalf("batch %q status=%d body=%s", batch.ID, response.Code, response.Body.String())
		}
	}
	latest, ok := api.store.Get("node-a")
	if !ok || latest.Snapshot.Timestamp != newer.Timestamp || latest.Snapshot.CPU.UsagePercent != 80 {
		t.Fatalf("out-of-order batch regressed current state: %+v", latest)
	}
	select {
	case event := <-events:
		if event.Snapshot.Timestamp != newer.Timestamp {
			t.Fatalf("published stale event timestamp=%s, want %s", event.Snapshot.Timestamp, newer.Timestamp)
		}
	default:
		t.Fatal("newer batch did not publish current state")
	}
	select {
	case event := <-events:
		t.Fatalf("out-of-order batch published stale event: %+v", event)
	default:
	}
}

func serverBatchEnvelope(timestamp time.Time, cpuUsage float64) metrics.MetricEnvelope {
	return metrics.MetricEnvelope{
		SchemaVersion: 1, SamplingMode: "idle", SamplingIntervalSeconds: 60,
		Device: metrics.DeviceInfo{ID: "node-a", DisplayName: "Node A", Hostname: "node-a"}, Timestamp: timestamp,
		CPU:    metrics.CPUMetrics{UsagePercent: cpuUsage, LogicalCores: 1},
		Memory: metrics.MemoryMetrics{TotalBytes: 100, AvailableBytes: 100},
	}
}
