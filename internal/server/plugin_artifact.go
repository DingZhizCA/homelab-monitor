/*
Package server serves signed plugin packages to authorized Agents.

The route verifies that a device's current desired state references the exact
artifact requested. It does not trust the publisher signature; the Agent checks
that signature against its local trust root before staging or execution.
*/
package server

import (
	"errors"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

// PluginArtifactHandler serves immutable package bundles to device-scoped Agents.
type PluginArtifactHandler struct {
	store     PluginStateStore
	artifacts plugins.ArtifactRepository
	devices   DeviceAuthorizer
}

// NewPluginArtifactHandler requires desired-state storage, immutable artifacts, and device credentials.
func NewPluginArtifactHandler(store PluginStateStore, artifacts plugins.ArtifactRepository, devices DeviceAuthorizer) (*PluginArtifactHandler, error) {
	if store == nil || artifacts == nil || devices == nil {
		return nil, errors.New("plugin state store, artifact repository, and device authorizer are required")
	}
	return &PluginArtifactHandler{store: store, artifacts: artifacts, devices: devices}, nil
}

// Download returns a multipart package only when it matches the device's enabled desired state.
func (h *PluginArtifactHandler) Download(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	if r.TLS == nil {
		writeError(w, http.StatusForbidden, "HTTPS_REQUIRED", "device credentials require HTTPS")
		return
	}
	deviceID, key, err := artifactRequestKey(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ARTIFACT", err.Error())
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !h.devices.Authorize(deviceID, token) {
		writeError(w, http.StatusUnauthorized, "DEVICE_UNAUTHORIZED", "device credentials are invalid")
		return
	}
	states, err := h.store.DesiredForDeviceContext(r.Context(), deviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "PLUGIN_STATE_UNAVAILABLE", "plugin desired state is unavailable")
		return
	}
	allowed := false
	for _, state := range states {
		if state.Enabled && state.PluginID == key.PluginID && state.Version == key.Version && state.Artifact.Key(state.PluginID, state.Version) == key {
			allowed = true
			break
		}
	}
	if !allowed {
		writeError(w, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "artifact is not part of the device's desired state")
		return
	}
	bundle, err := h.artifacts.Get(key)
	if err != nil {
		writeError(w, http.StatusNotFound, "ARTIFACT_NOT_FOUND", "plugin artifact is unavailable")
		return
	}
	multipartWriter := multipart.NewWriter(w)
	w.Header().Set("Content-Type", multipartWriter.FormDataContentType())
	w.Header().Set("Cache-Control", "no-store")
	for field, contents := range map[string][]byte{
		"manifest.json": bundle.ManifestJSON,
		"manifest.sig":  bundle.Signature,
		"artifact":      bundle.Artifact,
	} {
		part, err := multipartWriter.CreateFormField(field)
		if err != nil {
			return
		}
		if _, err := part.Write(contents); err != nil {
			return
		}
	}
	_ = multipartWriter.Close()
}

func artifactRequestKey(query url.Values) (string, plugins.ArtifactKey, error) {
	value := func(name string) (string, error) {
		values := query[name]
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" || strings.TrimSpace(values[0]) != values[0] {
			return "", fmt.Errorf("exactly one %s query parameter is required", name)
		}
		return values[0], nil
	}
	deviceID, err := value("device_id")
	if err != nil {
		return "", plugins.ArtifactKey{}, err
	}
	pluginID, err := value("plugin_id")
	if err != nil {
		return "", plugins.ArtifactKey{}, err
	}
	version, err := value("version")
	if err != nil {
		return "", plugins.ArtifactKey{}, err
	}
	osName, err := value("os")
	if err != nil {
		return "", plugins.ArtifactKey{}, err
	}
	arch, err := value("arch")
	if err != nil {
		return "", plugins.ArtifactKey{}, err
	}
	digest, err := value("sha256")
	if err != nil {
		return "", plugins.ArtifactKey{}, err
	}
	key := plugins.ArtifactKey{PluginID: pluginID, Version: version, Platform: plugins.Platform{OS: osName, Arch: arch}, SHA256: digest}
	if err := key.Validate(); err != nil {
		return "", plugins.ArtifactKey{}, err
	}
	return deviceID, key, nil
}
