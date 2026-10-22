/*
Package agent tests bounded batch retention, retry identity, and acknowledgment.
*/
package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
	"github.com/DingZhizCA/homelab-monitor/internal/server"
)

func TestBatchQueueKeepsPendingBatchStableUntilAcknowledged(t *testing.T) {
	queue, err := NewBatchQueue("node-a", 4, 16384, 2, 8192)
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	for index := range 3 {
		if err := queue.Append(senderTestEnvelope(base.Add(time.Duration(index) * time.Minute))); err != nil {
			t.Fatal(err)
		}
	}

	first, ok, err := queue.PendingBatch()
	if err != nil || !ok {
		t.Fatalf("first pending batch=%+v present=%t err=%v", first, ok, err)
	}
	if len(first.Snapshots) != 2 || first.DeviceID != "node-a" {
		t.Fatalf("unexpected first batch: %+v", first)
	}
	first.Snapshots[0].Device.DisplayName = "mutated by caller"
	retry, ok, err := queue.PendingBatch()
	if err != nil || !ok {
		t.Fatalf("retry batch=%+v present=%t err=%v", retry, ok, err)
	}
	if retry.ID != first.ID || retry.Snapshots[0].Device.DisplayName != "Node A" {
		t.Fatalf("retry changed identity or queue contents: %+v", retry)
	}
	if err := queue.Acknowledge("wrong-id"); err == nil {
		t.Fatal("mismatched acknowledgment was accepted")
	}
	if err := queue.Acknowledge(retry.ID); err != nil {
		t.Fatal(err)
	}
	if queue.Len() != 1 {
		t.Fatalf("queue length=%d, want 1 after acknowledgment", queue.Len())
	}
	next, ok, err := queue.PendingBatch()
	if err != nil || !ok || len(next.Snapshots) != 1 || next.ID == retry.ID {
		t.Fatalf("next batch=%+v present=%t err=%v", next, ok, err)
	}
	if err := queue.Acknowledge(next.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := queue.PendingBatch(); err != nil || ok {
		t.Fatalf("empty queue pending present=%t err=%v", ok, err)
	}
}

func TestBatchQueueRequiresExplicitLimitsAndRejectsOverflow(t *testing.T) {
	if _, err := NewBatchQueue(" node-a ", 1, 1024, 1, 1024); err == nil {
		t.Fatal("queue accepted a device identity with surrounding whitespace")
	}
	if _, err := NewBatchQueue("node-a", 0, 1024, 1, 1024); err == nil {
		t.Fatal("zero queue capacity was accepted")
	}
	if _, err := NewBatchQueue("node-a", 1, 0, 1, 1024); err == nil {
		t.Fatal("zero queue byte capacity was accepted")
	}
	if _, err := NewBatchQueue("node-a", 1, 1024, 2, 1024); err == nil {
		t.Fatal("batch limit greater than capacity was accepted")
	}
	queue, err := NewBatchQueue("node-a", 1, 8192, 1, 8192)
	if err != nil {
		t.Fatal(err)
	}
	timestamp := time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC)
	if err := queue.Append(senderTestEnvelope(timestamp)); err != nil {
		t.Fatal(err)
	}
	if err := queue.Append(senderTestEnvelope(timestamp.Add(time.Minute))); !errors.Is(err, ErrBatchQueueFull) {
		t.Fatalf("overflow error=%v, want ErrBatchQueueFull", err)
	}
	wrongDevice := senderTestEnvelope(timestamp.Add(time.Minute))
	wrongDevice.Device.ID = "node-b"
	if err := queue.Append(wrongDevice); err == nil {
		t.Fatal("snapshot from another device was accepted")
	}
	if err := queue.Append(senderTestEnvelope(timestamp)); err == nil {
		t.Fatal("non-increasing timestamp was accepted")
	}
}

func TestBatchQueueEnforcesByteCapacityAndReleasesItOnAcknowledgment(t *testing.T) {
	first := senderTestEnvelope(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC))
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	queue, err := NewBatchQueue("node-a", 3, int64(len(encoded)), 1, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Append(first); err != nil {
		t.Fatal(err)
	}
	if count, bytes := queue.Usage(); count != 1 || bytes != int64(len(encoded)) {
		t.Fatalf("queue usage=(%d, %d), want (1, %d)", count, bytes, len(encoded))
	}
	second := senderTestEnvelope(first.Timestamp.Add(time.Minute))
	if err := queue.Append(second); !errors.Is(err, ErrBatchQueueFull) {
		t.Fatalf("byte overflow error=%v, want ErrBatchQueueFull", err)
	}
	batch, ok, err := queue.PendingBatch()
	if err != nil || !ok {
		t.Fatalf("pending batch present=%t err=%v", ok, err)
	}
	if err := queue.Acknowledge(batch.ID); err != nil {
		t.Fatal(err)
	}
	if count, bytes := queue.Usage(); count != 0 || bytes != 0 {
		t.Fatalf("usage after acknowledgment=(%d, %d), want (0, 0)", count, bytes)
	}
	if err := queue.Append(second); err != nil {
		t.Fatalf("append after acknowledged bytes were released: %v", err)
	}
}

func TestBatchQueueRejectsSnapshotAboveBodyLimitWithoutLosingIt(t *testing.T) {
	queue, err := NewBatchQueue("node-a", 1, 8192, 1, 128)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := senderTestEnvelope(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC))
	snapshot.Device.DisplayName = strings.Repeat("n", 512)
	if err := queue.Append(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := queue.PendingBatch(); err == nil || ok {
		t.Fatalf("oversized snapshot present=%t err=%v", ok, err)
	}
	if queue.Len() != 1 {
		t.Fatal("oversized snapshot was removed from the queue")
	}
}

func TestBatchQueueSendPendingRetainsBatchAcrossFailure(t *testing.T) {
	attempts := 0
	batchIDs := make([]string, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var batch struct {
			ID string `json:"batch_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
			t.Error(err)
		}
		batchIDs = append(batchIDs, batch.ID)
		attempts++
		if attempts == 1 {
			http.Error(w, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"accepted_snapshots":1}`))
	}))
	defer server.Close()
	queue, err := NewBatchQueue("node-a", 2, 8192, 1, 8192)
	if err != nil {
		t.Fatal(err)
	}
	if err := queue.Append(senderTestEnvelope(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC))); err != nil {
		t.Fatal(err)
	}
	sender := NewSender(server.URL, "")
	if _, sent, err := queue.SendPending(context.Background(), sender); err == nil || !sent {
		t.Fatalf("first send sent=%t err=%v, want sent with error", sent, err)
	}
	if queue.Len() != 1 {
		t.Fatal("failed send removed pending snapshot")
	}
	result, sent, err := queue.SendPending(context.Background(), sender)
	if err != nil || !sent || result.AcceptedSnapshots != 1 {
		t.Fatalf("retry result=%+v sent=%t err=%v", result, sent, err)
	}
	if attempts != 2 || len(batchIDs) != 2 || batchIDs[0] == "" || batchIDs[0] != batchIDs[1] {
		t.Fatalf("attempts=%d batch IDs=%v, want two requests with one stable ID", attempts, batchIDs)
	}
	if queue.Len() != 0 {
		t.Fatalf("queue length=%d after successful retry, want 0", queue.Len())
	}
}

func TestBatchQueueRetainsBatchForMalformedOrOversizedAcceptanceResponse(t *testing.T) {
	responses := []struct {
		name string
		body string
	}{
		{name: "trailing JSON", body: `{"accepted_snapshots":1} {}`},
		{name: "oversized response", body: `{"accepted_snapshots":1}` + strings.Repeat(" ", maxMetricsResponseBytes)},
	}
	for _, test := range responses {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			queue, err := NewBatchQueue("node-a", 2, 8192, 1, 8192)
			if err != nil {
				t.Fatal(err)
			}
			if err := queue.Append(senderTestEnvelope(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC))); err != nil {
				t.Fatal(err)
			}
			if _, sent, err := queue.SendPending(context.Background(), NewSender(server.URL, "")); err == nil || !sent {
				t.Fatalf("SendPending() sent=%t err=%v, want malformed-response error", sent, err)
			}
			if queue.Len() != 1 {
				t.Fatalf("queue length=%d after invalid acceptance response, want 1", queue.Len())
			}
		})
	}
}

func TestBatchQueueHTTPSRetryIsIdempotentAfterServerCommit(t *testing.T) {
	credentials := server.NewDeviceCredentials()
	if err := credentials.Set("node-a", "batch-device-token"); err != nil {
		t.Fatal(err)
	}
	api := server.NewHandlerWithDeviceAuthorizer(server.NewStore(), server.NewBroker(), credentials, server.NewPresence())
	writer := &integrationBatchWriter{accepted: make(map[string]metrics.Batch)}
	batchHandler, err := server.NewMetricsBatchHandler(api, writer, 4, 64*1024)
	if err != nil {
		t.Fatal(err)
	}
	router, err := server.NewRouter(server.RouterOptions{API: api, MetricsBatch: batchHandler})
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	httpsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/metrics/batch" {
			attempts++
			if attempts == 1 {
				committedResponse := httptest.NewRecorder()
				router.ServeHTTP(committedResponse, r)
				if committedResponse.Code != http.StatusAccepted {
					t.Errorf("first batch request did not commit: status=%d body=%s", committedResponse.Code, committedResponse.Body.String())
					w.WriteHeader(http.StatusInternalServerError)
					return
				}
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
		}
		router.ServeHTTP(w, r)
	}))
	defer httpsServer.Close()

	queue, err := NewBatchQueue("node-a", 4, 64*1024, 2, 32*1024)
	if err != nil {
		t.Fatal(err)
	}
	first := senderTestEnvelope(time.Date(2026, 10, 18, 1, 0, 0, 0, time.UTC))
	second := senderTestEnvelope(first.Timestamp.Add(time.Minute))
	for _, snapshot := range []metrics.MetricEnvelope{first, second} {
		if err := queue.Append(snapshot); err != nil {
			t.Fatal(err)
		}
	}
	sender := NewSender(httpsServer.URL, "batch-device-token")
	sender.client = httpsServer.Client()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, sent, err := queue.SendPending(ctx, sender); err == nil || !sent {
		t.Fatalf("first send sent=%t err=%v, want committed request with lost acknowledgment", sent, err)
	}
	if queue.Len() != 2 || len(writer.accepted) != 1 {
		t.Fatalf("after lost response queue=%d committed batches=%d, want 2 and 1", queue.Len(), len(writer.accepted))
	}
	result, sent, err := queue.SendPending(ctx, sender)
	if err != nil || !sent || result.AcceptedSnapshots != 2 {
		t.Fatalf("retry result=%+v sent=%t err=%v", result, sent, err)
	}
	if attempts != 2 || writer.appendCalls != 2 || len(writer.accepted) != 1 || writer.insertedSnapshots != 2 {
		t.Fatalf("attempts=%d writer calls=%d unique batches=%d inserted snapshots=%d", attempts, writer.appendCalls, len(writer.accepted), writer.insertedSnapshots)
	}
	if queue.Len() != 0 {
		t.Fatalf("queue retained %d snapshots after accepted retry", queue.Len())
	}
	response, err := httpsServer.Client().Get(httpsServer.URL + "/api/v1/devices/node-a")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var latest server.DeviceState
	if err := json.NewDecoder(response.Body).Decode(&latest); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || latest.Snapshot.Timestamp != second.Timestamp {
		t.Fatalf("server current snapshot=%+v status=%d, want batch tail", latest, response.StatusCode)
	}
}

type integrationBatchWriter struct {
	accepted          map[string]metrics.Batch
	appendCalls       int
	insertedSnapshots int
}

func (w *integrationBatchWriter) AppendBatch(_ context.Context, batch metrics.Batch) (bool, error) {
	w.appendCalls++
	if previous, ok := w.accepted[batch.ID]; ok {
		if !reflect.DeepEqual(previous, batch) {
			return false, metrics.ErrBatchConflict
		}
		return false, nil
	}
	w.accepted[batch.ID] = batch
	w.insertedSnapshots += len(batch.Snapshots)
	return true, nil
}
