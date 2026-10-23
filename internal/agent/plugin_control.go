/*
Package agent provides the HTTPS client for plugin desired and reported state.

The client only exchanges control-plane state. It does not download, validate,
start, stop, or roll back plugin processes.
*/
package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

const maxPluginControlResponse = 1 << 20

// PluginControlClient exchanges plugin reconciliation state with one Server.
type PluginControlClient struct {
	baseURL string
	token   string
	client  *http.Client
}

// NewPluginControlClient requires an HTTPS endpoint for device credentials.
func NewPluginControlClient(serverURL, token string) (*PluginControlClient, error) {
	return newPluginControlClient(serverURL, token, 4*time.Second)
}

func newPluginControlClient(serverURL, token string, operationTimeout time.Duration) (*PluginControlClient, error) {
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("plugin control server URL must be an absolute HTTPS URL")
	}
	if strings.TrimSpace(token) == "" || operationTimeout <= 0 {
		return nil, fmt.Errorf("plugin control device token and positive operation timeout are required")
	}
	return &PluginControlClient{
		baseURL: strings.TrimRight(serverURL, "/"),
		token:   token,
		client:  newCredentialHTTPClientWithTimeout(operationTimeout),
	}, nil
}

// Desired fetches the target plugin state for one configured device.
func (c *PluginControlClient) Desired(ctx context.Context, deviceID string) ([]plugins.DesiredState, error) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return nil, fmt.Errorf("plugin desired-state device ID is required and must be trimmed")
	}
	endpoint := c.baseURL + "/api/v1/agent/plugins/desired?device_id=" + url.QueryEscape(deviceID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create plugin desired-state request: %w", err)
	}
	c.authorize(request)
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch plugin desired state: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, controlResponseError("fetch plugin desired state", response)
	}
	var result struct {
		DeviceID string                 `json:"device_id"`
		States   []plugins.DesiredState `json:"states"`
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxPluginControlResponse+1))
	if err != nil {
		return nil, fmt.Errorf("read plugin desired state: %w", err)
	}
	if len(responseBody) > maxPluginControlResponse {
		return nil, fmt.Errorf("plugin desired-state response exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode plugin desired state: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("decode plugin desired state: response must contain one JSON object")
	}
	if result.DeviceID != deviceID {
		return nil, fmt.Errorf("plugin desired-state response device mismatch")
	}
	for _, state := range result.States {
		if state.DeviceID != deviceID {
			return nil, fmt.Errorf("plugin desired-state response contains another device")
		}
		if err := state.Validate(); err != nil {
			return nil, fmt.Errorf("plugin desired-state response contains invalid state: %w", err)
		}
	}
	if result.States == nil {
		result.States = []plugins.DesiredState{}
	}
	return result.States, nil
}

// CollectionPolicies fetches independently revisioned collector schedules.
func (c *PluginControlClient) CollectionPolicies(ctx context.Context, deviceID string) ([]plugins.CollectionPolicy, error) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return nil, fmt.Errorf("plugin collection-policy device ID is required and must be trimmed")
	}
	endpoint := c.baseURL + "/api/v1/agent/plugins/collection-policies?device_id=" + url.QueryEscape(deviceID)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create plugin collection-policy request: %w", err)
	}
	c.authorize(request)
	response, err := c.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("fetch plugin collection policies: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, controlResponseError("fetch plugin collection policies", response)
	}
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxPluginControlResponse+1))
	if err != nil || len(responseBody) > maxPluginControlResponse {
		return nil, fmt.Errorf("plugin collection-policy response exceeds size limit or could not be read")
	}
	var result struct {
		DeviceID string                     `json:"device_id"`
		Policies []plugins.CollectionPolicy `json:"policies"`
	}
	decoder := json.NewDecoder(bytes.NewReader(responseBody))
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode plugin collection policies: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF || result.DeviceID != deviceID {
		return nil, fmt.Errorf("plugin collection-policy response must contain one matching device object")
	}
	for _, policy := range result.Policies {
		if policy.DeviceID != deviceID {
			return nil, fmt.Errorf("plugin collection-policy response contains another device")
		}
		if err := policy.Validate(); err != nil {
			return nil, fmt.Errorf("plugin collection-policy response contains invalid policy: %w", err)
		}
	}
	if result.Policies == nil {
		result.Policies = []plugins.CollectionPolicy{}
	}
	return result.Policies, nil
}

// Reported submits a batch of inspected and applied plugin states.
func (c *PluginControlClient) Reported(ctx context.Context, deviceID string, states []plugins.ReportedState) error {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID {
		return fmt.Errorf("plugin report device ID is required and must be trimmed")
	}
	for _, state := range states {
		if state.DeviceID != deviceID {
			return fmt.Errorf("reported plugin state belongs to another device")
		}
	}
	body, err := json.Marshal(struct {
		DeviceID string                  `json:"device_id"`
		States   []plugins.ReportedState `json:"states"`
	}{deviceID, states})
	if err != nil {
		return fmt.Errorf("encode plugin report: %w", err)
	}
	endpoint := c.baseURL + "/api/v1/agent/plugins/reported"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create plugin report request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	c.authorize(request)
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("report plugin state: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusAccepted {
		return controlResponseError("report plugin state", response)
	}
	return nil
}

// DownloadArtifact fetches one desired package under an explicit artifact size limit.
// Signature trust is checked later by the Agent's configured Stager trust root.
func (c *PluginControlClient) DownloadArtifact(ctx context.Context, deviceID string, state plugins.DesiredState, maxArtifactBytes int64) (plugins.PackageArtifact, error) {
	if strings.TrimSpace(deviceID) == "" || strings.TrimSpace(deviceID) != deviceID || state.DeviceID != deviceID {
		return plugins.PackageArtifact{}, fmt.Errorf("plugin artifact request requires a matching device ID")
	}
	if err := state.Validate(); err != nil {
		return plugins.PackageArtifact{}, fmt.Errorf("invalid desired plugin state: %w", err)
	}
	if !state.Enabled {
		return plugins.PackageArtifact{}, fmt.Errorf("disabled plugin state has no artifact to download")
	}
	if maxArtifactBytes < 1 {
		return plugins.PackageArtifact{}, fmt.Errorf("plugin artifact size limit must be positive")
	}
	const envelopeOverhead int64 = maxPluginControlResponse + 64 + (1 << 20)
	maxInt64 := int64(^uint64(0) >> 1)
	if maxArtifactBytes > maxInt64-envelopeOverhead-1 {
		return plugins.PackageArtifact{}, fmt.Errorf("plugin artifact size limit is too large")
	}
	key := state.Artifact.Key(state.PluginID, state.Version)
	if key.Platform.OS != runtime.GOOS || key.Platform.Arch != runtime.GOARCH {
		return plugins.PackageArtifact{}, fmt.Errorf("desired plugin artifact platform does not match this Agent")
	}
	query := url.Values{
		"device_id": {deviceID}, "plugin_id": {key.PluginID}, "version": {key.Version},
		"os": {key.Platform.OS}, "arch": {key.Platform.Arch}, "sha256": {key.SHA256},
	}
	endpoint := c.baseURL + "/api/v1/agent/plugins/artifact?" + query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return plugins.PackageArtifact{}, fmt.Errorf("create plugin artifact request: %w", err)
	}
	c.authorize(request)
	response, err := c.client.Do(request)
	if err != nil {
		return plugins.PackageArtifact{}, fmt.Errorf("download plugin artifact: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return plugins.PackageArtifact{}, controlResponseError("download plugin artifact", response)
	}
	mediaType, params, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" || params["boundary"] == "" {
		return plugins.PackageArtifact{}, fmt.Errorf("plugin artifact response has an invalid multipart content type")
	}
	maxResponse := maxArtifactBytes + envelopeOverhead
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxResponse+1))
	if err != nil {
		return plugins.PackageArtifact{}, fmt.Errorf("read plugin artifact response: %w", err)
	}
	if int64(len(responseBody)) > maxResponse {
		return plugins.PackageArtifact{}, fmt.Errorf("plugin artifact response exceeds size limit")
	}
	bundle, err := decodeArtifactMultipart(responseBody, params["boundary"], maxArtifactBytes)
	if err != nil {
		return plugins.PackageArtifact{}, err
	}
	manifest, err := plugins.DecodeManifest(bundle.ManifestJSON)
	if err != nil {
		return plugins.PackageArtifact{}, fmt.Errorf("validate downloaded plugin manifest: %w", err)
	}
	digest := sha256.Sum256(bundle.Artifact)
	if manifest.PluginID != key.PluginID || manifest.Version != key.Version || manifest.Platform != key.Platform ||
		manifest.ArtifactSHA256 != key.SHA256 || hex.EncodeToString(digest[:]) != key.SHA256 || len(bundle.Signature) != ed25519.SignatureSize {
		return plugins.PackageArtifact{}, fmt.Errorf("downloaded plugin package does not match the desired artifact identity")
	}
	return bundle, nil
}

func decodeArtifactMultipart(body []byte, boundary string, maxArtifactBytes int64) (plugins.PackageArtifact, error) {
	reader := multipart.NewReader(bytes.NewReader(body), boundary)
	limits := map[string]int64{"manifest.json": maxPluginControlResponse, "manifest.sig": ed25519.SignatureSize, "artifact": maxArtifactBytes}
	parts := make(map[string][]byte, len(limits))
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return plugins.PackageArtifact{}, fmt.Errorf("decode plugin artifact multipart: %w", err)
		}
		name := part.FormName()
		limit, ok := limits[name]
		if !ok {
			_ = part.Close()
			return plugins.PackageArtifact{}, fmt.Errorf("plugin artifact response contains an unknown part")
		}
		if _, exists := parts[name]; exists {
			_ = part.Close()
			return plugins.PackageArtifact{}, fmt.Errorf("plugin artifact response contains a duplicate part")
		}
		contents, readErr := io.ReadAll(io.LimitReader(part, limit+1))
		closeErr := part.Close()
		if readErr != nil || closeErr != nil || int64(len(contents)) > limit {
			return plugins.PackageArtifact{}, fmt.Errorf("plugin artifact part %q is invalid or exceeds its size limit", name)
		}
		parts[name] = contents
	}
	if len(parts) != len(limits) {
		return plugins.PackageArtifact{}, fmt.Errorf("plugin artifact response is missing required parts")
	}
	return plugins.PackageArtifact{ManifestJSON: parts["manifest.json"], Signature: parts["manifest.sig"], Artifact: parts["artifact"]}, nil
}

func (c *PluginControlClient) authorize(request *http.Request) {
	request.Header.Set("Authorization", "Bearer "+c.token)
}

func controlResponseError(operation string, response *http.Response) error {
	detail, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
	return fmt.Errorf("%s: server returned %s: %s", operation, response.Status, strings.TrimSpace(string(detail)))
}
