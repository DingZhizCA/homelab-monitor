package server

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

func testHandler() (*Handler, *Store) { s := NewStore(); return NewHandler(s, NewBroker()), s }

func testEnvelope() metrics.MetricEnvelope {
	return metrics.MetricEnvelope{SchemaVersion: 1, SamplingMode: "idle", SamplingIntervalSeconds: 60, Device: metrics.DeviceInfo{ID: "n1", DisplayName: "N1", Hostname: "armbian"}, Timestamp: time.Now(), CPU: metrics.CPUMetrics{LogicalCores: 4}, Memory: metrics.MemoryMetrics{TotalBytes: 100, AvailableBytes: 100}}
}

func TestMetricsAndDevice(t *testing.T) {
	h, _ := testHandler()
	body, _ := json.Marshal(testEnvelope())
	post := httptest.NewRecorder()
	h.Metrics(post, httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body)))
	if post.Code != http.StatusAccepted {
		t.Fatalf("POST status = %d, body=%s", post.Code, post.Body.String())
	}
	get := httptest.NewRecorder()
	h.Device(get, httptest.NewRequest(http.MethodGet, "/api/v1/devices/n1", nil))
	if get.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body=%s", get.Code, get.Body.String())
	}
	var state DeviceState
	if err := json.NewDecoder(get.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if state.Snapshot.Device.ID != "n1" || state.ReceivedAt.IsZero() || state.Status != "online" {
		t.Fatalf("unexpected state: %+v", state)
	}
}

func TestMetricsRejectsInvalidInput(t *testing.T) {
	h, store := testHandler()
	m := testEnvelope()
	m.CPU.UsagePercent = 101
	body, _ := json.Marshal(m)
	w := httptest.NewRecorder()
	h.Metrics(w, httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", w.Code)
	}
	if len(store.List()) != 0 {
		t.Fatal("invalid snapshot was stored")
	}
	if got := w.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
}

func TestMetricsDeviceAuthorizationBindsTokenToDevice(t *testing.T) {
	credentials := NewDeviceCredentials()
	if err := credentials.Set("n1", "secret-for-n1"); err != nil {
		t.Fatal(err)
	}
	h := NewHandlerWithDeviceAuthorizer(NewStore(), NewBroker(), credentials)
	body, err := json.Marshal(testEnvelope())
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer secret-for-n1")
	w := httptest.NewRecorder()
	h.Metrics(w, request)
	if w.Code != http.StatusForbidden {
		t.Fatalf("plain HTTP metrics status = %d", w.Code)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer secret-for-n1")
	w = httptest.NewRecorder()
	h.Metrics(w, request)
	if w.Code != http.StatusAccepted {
		t.Fatalf("authorized metrics status = %d, body=%s", w.Code, w.Body.String())
	}

	for _, authorization := range []string{"", "Bearer wrong-token"} {
		request = httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body))
		request.TLS = &tls.ConnectionState{}
		request.Header.Set("Authorization", authorization)
		w = httptest.NewRecorder()
		h.Metrics(w, request)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("authorization %q status = %d", authorization, w.Code)
		}
	}
	otherDevice := testEnvelope()
	otherDevice.Device.ID = "n2"
	body, err = json.Marshal(otherDevice)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body))
	request.TLS = &tls.ConnectionState{}
	request.Header.Set("Authorization", "Bearer secret-for-n1")
	w = httptest.NewRecorder()
	h.Metrics(w, request)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("cross-device token status = %d", w.Code)
	}
}

func TestDeviceNotFound(t *testing.T) {
	h, _ := testHandler()
	w := httptest.NewRecorder()
	h.Device(w, httptest.NewRequest(http.MethodGet, "/api/v1/devices/missing", nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
}

func TestExplicitDiskRefresh(t *testing.T) {
	h, _ := testHandler()
	body, _ := json.Marshal(testEnvelope())
	h.Metrics(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body)))

	refresh := httptest.NewRecorder()
	h.Device(refresh, httptest.NewRequest(http.MethodPost, "/api/v1/devices/n1/refresh", bytes.NewBufferString(`{"collectors":["disk"],"allow_wake":true}`)))
	if refresh.Code != http.StatusAccepted {
		t.Fatalf("refresh status = %d, body=%s", refresh.Code, refresh.Body.String())
	}
	var job RefreshJob
	if err := json.NewDecoder(refresh.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	if job.ID == "" || job.Status != "queued" {
		t.Fatalf("unexpected job: %+v", job)
	}

	post := httptest.NewRecorder()
	h.Metrics(post, httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body)))
	var response struct {
		RefreshDisk      bool   `json:"refresh_disk"`
		RefreshRequestID string `json:"refresh_request_id"`
	}
	if err := json.NewDecoder(post.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if !response.RefreshDisk {
		t.Fatal("queued refresh was not delivered to agent")
	}
	if response.RefreshRequestID != job.ID {
		t.Fatalf("request ID = %q, want %q", response.RefreshRequestID, job.ID)
	}

	post = httptest.NewRecorder()
	h.Metrics(post, httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body)))
	if err := json.NewDecoder(post.Body).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.RefreshDisk {
		t.Fatal("one-shot refresh was delivered more than once")
	}

	active := testEnvelope()
	active.RefreshRequestID = job.ID
	active.Disks = []metrics.DiskMetrics{{MountPoint: "/data", TotalBytes: 100, AvailableBytes: 100, CollectedAt: time.Now(), CollectionMode: "active"}}
	body, _ = json.Marshal(active)
	h.Metrics(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/v1/metrics", bytes.NewReader(body)))
	status := httptest.NewRecorder()
	h.RefreshStatus(status, httptest.NewRequest(http.MethodGet, "/api/v1/refresh/"+job.ID, nil))
	if status.Code != http.StatusOK {
		t.Fatalf("refresh status code = %d", status.Code)
	}
	if err := json.NewDecoder(status.Body).Decode(&job); err != nil {
		t.Fatal(err)
	}
	if job.Status != "completed" {
		t.Fatalf("job status = %q", job.Status)
	}
}

func TestDiskRefreshRequiresExplicitWakeConsent(t *testing.T) {
	h, _ := testHandler()
	w := httptest.NewRecorder()
	h.Device(w, httptest.NewRequest(http.MethodPost, "/api/v1/devices/n1/refresh", bytes.NewBufferString(`{"collectors":["disk"],"allow_wake":false}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	h.Device(w, httptest.NewRequest(http.MethodPost, "/api/v1/devices/n1/refresh", bytes.NewBufferString(`{"collectors":["disk"],"allow_wake":false,"allow_wake":true}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("duplicate consent status = %d, body=%s", w.Code, w.Body.String())
	}
}
