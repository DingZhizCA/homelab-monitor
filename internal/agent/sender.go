package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/metrics"
)

type Sender struct {
	endpoint, token string
	client          *http.Client
}

type SendResult struct {
	RefreshDisk      bool       `json:"refresh_disk"`
	RefreshRequestID string     `json:"refresh_request_id"`
	SamplingMode     string     `json:"sampling_mode"`
	ActiveUntil      *time.Time `json:"active_until"`
}

func NewSender(serverURL, token string) *Sender {
	return &Sender{endpoint: strings.TrimRight(serverURL, "/") + "/api/v1/metrics", token: token, client: &http.Client{Timeout: 4 * time.Second}}
}

func (s *Sender) Send(ctx context.Context, snapshot metrics.MetricEnvelope) (SendResult, error) {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return SendResult{}, fmt.Errorf("encode metrics: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.endpoint, bytes.NewReader(body))
	if err != nil {
		return SendResult{}, fmt.Errorf("create request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	if s.token != "" {
		request.Header.Set("Authorization", "Bearer "+s.token)
	}
	response, err := s.client.Do(request)
	if err != nil {
		return SendResult{}, fmt.Errorf("send metrics: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return SendResult{}, fmt.Errorf("server returned %s: %s", response.Status, strings.TrimSpace(string(detail)))
	}
	var result SendResult
	if err := json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&result); err != nil && err != io.EOF {
		return SendResult{}, fmt.Errorf("decode server response: %w", err)
	}
	return result, nil
}
