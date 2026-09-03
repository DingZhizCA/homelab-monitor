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

// Sender delivers snapshots to one configured Server endpoint.
// Sender 将指标快照发送到配置的 Server，并复用带超时的 HTTP Client。
type Sender struct {
	endpoint, token string
	client          *http.Client
}

// SendResult contains sampling and one-shot refresh instructions from Server.
// SendResult 表示 Server 随上报响应返回的采样模式和一次性刷新指令。
type SendResult struct {
	RefreshDisk      bool       `json:"refresh_disk"`
	RefreshRequestID string     `json:"refresh_request_id"`
	SamplingMode     string     `json:"sampling_mode"`
	ActiveUntil      *time.Time `json:"active_until"`
}

// NewSender creates a Sender for the metrics endpoint derived from serverURL.
// NewSender 根据 Server 基础地址创建指标发送器。
func NewSender(serverURL, token string) *Sender {
	return &Sender{endpoint: strings.TrimRight(serverURL, "/") + "/api/v1/metrics", token: token, client: &http.Client{Timeout: 4 * time.Second}}
}

// Send posts one snapshot and decodes any control instruction in the response.
// Send 发送一次快照；网络或协议错误由调用方处理，不在此处无限重试。
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
