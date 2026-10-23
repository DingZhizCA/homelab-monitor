/*
Package server exposes device-scoped reads and Owner-authored writes for
collector policies without coupling them to plugin package revisions.
*/
package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

type PluginCollectionPolicyStore interface {
	CollectionPoliciesForDeviceContext(context.Context, string) ([]plugins.CollectionPolicy, error)
	DesiredForDeviceContext(context.Context, string) ([]plugins.DesiredState, error)
	SetCollectionPolicyWithAuditContext(context.Context, plugins.CollectionPolicy, audit.Event) error
}

type PluginCollectionPolicyHandler struct {
	store     PluginCollectionPolicyStore
	artifacts plugins.ArtifactRepository
	devices   DeviceAuthorizer
}

func NewPluginCollectionPolicyHandler(store PluginCollectionPolicyStore, artifacts plugins.ArtifactRepository, devices DeviceAuthorizer) (*PluginCollectionPolicyHandler, error) {
	if store == nil || artifacts == nil || devices == nil {
		return nil, errors.New("collection policy store, artifact repository, and device authorizer are required")
	}
	return &PluginCollectionPolicyHandler{store: store, artifacts: artifacts, devices: devices}, nil
}

// Desired returns every collection policy for one authenticated Agent.
func (h *PluginCollectionPolicyHandler) Desired(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodGet) {
		return
	}
	if r.TLS == nil {
		writeError(w, http.StatusForbidden, "HTTPS_REQUIRED", "device credentials require HTTPS")
		return
	}
	deviceIDs := r.URL.Query()["device_id"]
	if len(deviceIDs) != 1 || strings.TrimSpace(deviceIDs[0]) == "" || strings.TrimSpace(deviceIDs[0]) != deviceIDs[0] {
		writeError(w, http.StatusBadRequest, "INVALID_DEVICE", "exactly one trimmed device_id is required")
		return
	}
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok || !h.devices.Authorize(deviceIDs[0], token) {
		writeError(w, http.StatusUnauthorized, "DEVICE_UNAUTHORIZED", "device credentials are invalid")
		return
	}
	policies, err := h.store.CollectionPoliciesForDeviceContext(r.Context(), deviceIDs[0])
	if err != nil {
		writeError(w, http.StatusInternalServerError, "COLLECTION_POLICY_UNAVAILABLE", "plugin collection policies are unavailable")
		return
	}
	if policies == nil {
		policies = []plugins.CollectionPolicy{}
	}
	writeJSON(w, http.StatusOK, struct {
		DeviceID string                     `json:"device_id"`
		Policies []plugins.CollectionPolicy `json:"policies"`
	}{deviceIDs[0], policies})
}

// SetDesired publishes one independently revisioned policy after package validation.
func (h *PluginCollectionPolicyHandler) SetDesired(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPut) {
		return
	}
	principal, ok := authenticatedPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid Owner session is required")
		return
	}
	var policy plugins.CollectionPolicy
	if !decodeSingleJSONBody(w, r, maxMetricsBody, &policy, "INVALID_JSON", "request body must be one unambiguous collection policy", "") {
		return
	}
	if err := policy.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_COLLECTION_POLICY", err.Error())
		return
	}
	if policy.Enabled {
		desired, err := h.store.DesiredForDeviceContext(r.Context(), policy.DeviceID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "PLUGIN_STATE_UNAVAILABLE", "plugin desired state is unavailable")
			return
		}
		state, found := desiredPlugin(desired, policy.PluginID)
		if !found || !state.Enabled || state.Revision != policy.PluginRevision || !artifactDeclaresCollector(h.artifacts, state, policy.CollectorID) {
			writeError(w, http.StatusConflict, "COLLECTOR_UNAVAILABLE", "enabled policy must reference a collector in the enabled desired plugin package")
			return
		}
	}
	event, err := audit.NewEvent(time.Now(), principal.UserID, plugins.CollectionPolicyAuditAction, plugins.CollectionPolicyAuditResourceType, plugins.CollectionPolicyAuditResourceID(policy))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT_UNAVAILABLE", "collection policy could not be audited")
		return
	}
	if err := h.store.SetCollectionPolicyWithAuditContext(r.Context(), policy, event); err != nil {
		switch {
		case errors.Is(err, auth.ErrForbidden):
			writeError(w, http.StatusForbidden, "FORBIDDEN", "the current account is no longer an active Owner")
		case errors.Is(err, plugins.ErrStateConflict):
			writeError(w, http.StatusConflict, "COLLECTION_POLICY_CONFLICT", err.Error())
		case errors.Is(err, plugins.ErrCollectionTargetUnavailable):
			writeError(w, http.StatusConflict, "COLLECTOR_UNAVAILABLE", "the bound plugin revision is no longer enabled")
		case errors.Is(err, plugins.ErrCollectionPolicyCapacity):
			writeError(w, http.StatusConflict, "COLLECTION_POLICY_CAPACITY", "the device collection policy capacity is exhausted")
		default:
			writeError(w, http.StatusInternalServerError, "COLLECTION_POLICY_UNAVAILABLE", "collection policy could not be stored")
		}
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func desiredPlugin(states []plugins.DesiredState, pluginID string) (plugins.DesiredState, bool) {
	for _, state := range states {
		if state.PluginID == pluginID {
			return state, true
		}
	}
	return plugins.DesiredState{}, false
}

func artifactDeclaresCollector(repository plugins.ArtifactRepository, state plugins.DesiredState, collectorID string) bool {
	bundle, err := repository.Get(state.Artifact.Key(state.PluginID, state.Version))
	if err != nil {
		return false
	}
	manifest, err := plugins.DecodeManifest(bundle.ManifestJSON)
	if err != nil {
		return false
	}
	for _, collector := range manifest.Collectors {
		if collector.ID == collectorID {
			return true
		}
	}
	return false
}
