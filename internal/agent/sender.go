/*
Package agent implements metrics delivery from homelab-agent to homelab-server.

本文件负责 JSON 编码、HTTP 超时、可选 Bearer token 和 Server 控制响应解析。
本文件不采集指标、不重试调度，也不执行 Server 返回的磁盘刷新操作。

数据链路：MetricEnvelope -> HTTP POST -> SendResult -> Agent loop
*/
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

const maxMetricsResponseBytes = 4096

// Sender delivers snapshots to one configured Server endpoint.
// Sender 将指标快照发送到配置的 Server，并复用带超时的 HTTP Client。
type Sender struct {
	endpoint, batchEndpoint, token string
	client                         *http.Client
}

// SendResult contains sampling and one-shot refresh instructions from Server.
// SendResult 表示 Server 随上报响应返回的采样模式和一次性刷新指令。
type SendResult struct {
	RefreshDisk       bool       `json:"refresh_disk"`
	RefreshRequestID  string     `json:"refresh_request_id"`
	SamplingMode      string     `json:"sampling_mode"`
	ActiveUntil       *time.Time `json:"active_until"`
	AcceptedSnapshots int        `json:"accepted_snapshots,omitempty"`
}

// NewSender creates a Sender for the metrics endpoint derived from serverURL.
// NewSender 根据 Server 基础地址创建指标发送器。
func NewSender(serverURL, token string) *Sender {
	baseURL := strings.TrimRight(serverURL, "/")
	return &Sender{endpoint: baseURL + "/api/v1/metrics", batchEndpoint: baseURL + "/api/v1/metrics/batch", token: token, client: newCredentialHTTPClient()}
}

// Send posts one snapshot and decodes any control instruction in the response.
// Send 发送一次快照；网络或协议错误由调用方处理，不在此处无限重试。
func (s *Sender) Send(ctx context.Context, snapshot metrics.MetricEnvelope) (SendResult, error) {
	body, err := json.Marshal(snapshot)
	if err != nil {
		return SendResult{}, fmt.Errorf("encode metrics: %w", err)
	}
	return s.sendJSON(ctx, s.endpoint, body)
}

// SendBatch uploads an explicitly bounded, ordered sample batch with a caller-selected idempotency key.
func (s *Sender) SendBatch(ctx context.Context, batch metrics.Batch, maxSnapshots int, maxBodyBytes int64) (SendResult, error) {
	if err := batch.Validate(maxSnapshots); err != nil {
		return SendResult{}, fmt.Errorf("validate metrics batch: %w", err)
	}
	if maxBodyBytes < 1 {
		return SendResult{}, fmt.Errorf("metrics batch body limit must be positive")
	}
	body, err := json.Marshal(batch)
	if err != nil {
		return SendResult{}, fmt.Errorf("encode metrics batch: %w", err)
	}
	if int64(len(body)) > maxBodyBytes {
		return SendResult{}, fmt.Errorf("metrics batch body exceeds configured %d-byte limit", maxBodyBytes)
	}
	result, err := s.sendJSON(ctx, s.batchEndpoint, body)
	if err != nil {
		return SendResult{}, err
	}
	if result.AcceptedSnapshots != len(batch.Snapshots) {
		return SendResult{}, fmt.Errorf("server accepted %d of %d batch snapshots", result.AcceptedSnapshots, len(batch.Snapshots))
	}
	return result, nil
}

func (s *Sender) sendJSON(ctx context.Context, endpoint string, body []byte) (SendResult, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
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
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxMetricsResponseBytes+1))
	if err != nil {
		return SendResult{}, fmt.Errorf("read metrics response: %w", err)
	}
	if len(responseBody) > maxMetricsResponseBytes {
		return SendResult{}, fmt.Errorf("metrics response exceeds %d-byte limit", maxMetricsResponseBytes)
	}
	if len(bytes.TrimSpace(responseBody)) == 0 {
		return SendResult{}, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	var result SendResult
	if err := decoder.Decode(&result); err != nil {
		return SendResult{}, fmt.Errorf("decode server response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return SendResult{}, fmt.Errorf("decode server response: expected exactly one JSON object")
	}
	return result, nil
}
