package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func TestSenderReceivesControlResponse(t *testing.T) {
	expires := time.Now().Add(time.Minute).UTC().Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/metrics" || r.Method != http.MethodPost {
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"sampling_mode":"active","active_until":"` + expires.Format(time.RFC3339) + `"}`))
	}))
	defer server.Close()
	result, err := NewSender(server.URL, "").Send(context.Background(), metrics.MetricEnvelope{})
	if err != nil {
		t.Fatal(err)
	}
	if result.SamplingMode != "active" || result.ActiveUntil == nil || !result.ActiveUntil.Equal(expires) {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSenderSendsBoundedMetricBatchWithStableID(t *testing.T) {
	first := senderTestEnvelope(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC))
	second := senderTestEnvelope(first.Timestamp.Add(time.Minute))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/metrics/batch" || r.Method != http.MethodPost {
			t.Errorf("unexpected batch request: %s %s", r.Method, r.URL.Path)
		}
		var batch metrics.Batch
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Error(err)
		}
		if batch.ID != "retry-key-01" || batch.DeviceID != "node-a" || len(batch.Snapshots) != 2 {
			t.Errorf("unexpected batch payload: %+v", batch)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted_snapshots":2}`))
	}))
	defer server.Close()
	result, err := NewSender(server.URL, "").SendBatch(context.Background(), metrics.Batch{ID: "retry-key-01", DeviceID: "node-a", Snapshots: []metrics.MetricEnvelope{first, second}}, 2, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if result.AcceptedSnapshots != 2 {
		t.Fatalf("accepted snapshots=%d, want 2", result.AcceptedSnapshots)
	}
}

func TestSenderRejectsInvalidBatchBeforeNetworkRequest(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	_, err := NewSender(server.URL, "").SendBatch(context.Background(), metrics.Batch{ID: "batch-1", DeviceID: "node-a", Snapshots: []metrics.MetricEnvelope{senderTestEnvelope(time.Now())}}, 0, 8192)
	if err == nil || called {
		t.Fatalf("invalid batch error=%v, network called=%t", err, called)
	}
	large := senderTestEnvelope(time.Now())
	large.Device.DisplayName = strings.Repeat("n", 512)
	_, err = NewSender(server.URL, "").SendBatch(context.Background(), metrics.Batch{ID: "batch-2", DeviceID: "node-a", Snapshots: []metrics.MetricEnvelope{large}}, 1, 128)
	if err == nil || called {
		t.Fatalf("oversized batch error=%v, network called=%t", err, called)
	}
}

func senderTestEnvelope(timestamp time.Time) metrics.MetricEnvelope {
	return metrics.MetricEnvelope{
		SchemaVersion: 1, SamplingMode: "idle", SamplingIntervalSeconds: 60,
		Device: metrics.DeviceInfo{ID: "node-a", DisplayName: "Node A", Hostname: "node-a"}, Timestamp: timestamp,
		CPU:    metrics.CPUMetrics{LogicalCores: 1},
		Memory: metrics.MemoryMetrics{TotalBytes: 100, AvailableBytes: 100},
	}
}

func TestSenderUsesConfiguredBearerTokenOverTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
			t.Errorf("Authorization = %q", got)
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()
	sender := NewSender(server.URL, "device-token")
	sender.client = server.Client()
	if _, err := sender.Send(context.Background(), metrics.MetricEnvelope{}); err != nil {
		t.Fatal(err)
	}
}
