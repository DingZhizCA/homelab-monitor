/*
Package server exposes the human Owner boundary for plugin desired state.

The handler validates state revisions and verifies that enabled targets point
to an immutable package already present in the local artifact repository.
*/
package server

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/plugins"
)

// PluginDesiredStateWriter persists a Server-authored plugin target.
type PluginDesiredStateWriter interface {
	SetDesiredWithAuditContext(context.Context, plugins.DesiredState, audit.Event) error
}

// PluginAdminHandler accepts one Owner-authored desired-state revision.
type PluginAdminHandler struct {
	store     PluginDesiredStateWriter
	artifacts plugins.ArtifactRepository
	mutation  sync.Locker
}

// NewPluginAdminHandler requires desired-state storage and the artifact repository.
func NewPluginAdminHandler(store PluginDesiredStateWriter, artifacts plugins.ArtifactRepository) (*PluginAdminHandler, error) {
	return NewPluginAdminHandlerWithMutationLock(store, artifacts, nil)
}

// NewPluginAdminHandlerWithMutationLock coordinates publication with artifact cleanup.
func NewPluginAdminHandlerWithMutationLock(store PluginDesiredStateWriter, artifacts plugins.ArtifactRepository, mutation sync.Locker) (*PluginAdminHandler, error) {
	if store == nil || artifacts == nil {
		return nil, errors.New("plugin desired-state writer and artifact repository are required")
	}
	return &PluginAdminHandler{store: store, artifacts: artifacts, mutation: mutation}, nil
}

// SetDesired publishes one validated desired state after checking package availability.
func (h *PluginAdminHandler) SetDesired(w http.ResponseWriter, r *http.Request) {
	if !method(w, r, http.MethodPut) {
		return
	}
	principal, ok := authenticatedPrincipal(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "UNAUTHENTICATED", "a valid Owner session is required")
		return
	}
	var state plugins.DesiredState
	if !decodeSingleJSONBody(w, r, maxMetricsBody, &state, "INVALID_JSON", "request body must be one unambiguous desired-state object", "") {
		return
	}
	if err := state.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_PLUGIN_STATE", err.Error())
		return
	}
	if h.mutation != nil {
		h.mutation.Lock()
		defer h.mutation.Unlock()
	}
	if state.Enabled {
		if _, err := h.artifacts.Get(state.Artifact.Key(state.PluginID, state.Version)); err != nil {
			writeError(w, http.StatusConflict, "ARTIFACT_UNAVAILABLE", "enabled desired state must reference an available immutable artifact")
			return
		}
	}
	event, err := audit.NewEvent(time.Now(), principal.UserID, plugins.DesiredStateAuditAction, plugins.DesiredStateAuditResourceType, plugins.DesiredStateAuditResourceID(state))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUDIT_UNAVAILABLE", "plugin desired state could not be audited")
		return
	}
	if err := h.store.SetDesiredWithAuditContext(r.Context(), state, event); err != nil {
		if errors.Is(err, auth.ErrForbidden) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "the current account is no longer an active Owner")
		} else if errors.Is(err, plugins.ErrStateConflict) {
			writeError(w, http.StatusConflict, "DESIRED_STATE_CONFLICT", err.Error())
		} else {
			writeError(w, http.StatusInternalServerError, "PLUGIN_STATE_UNAVAILABLE", "plugin desired state could not be stored")
		}
		return
	}
	w.WriteHeader(http.StatusAccepted)
}
