package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
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
