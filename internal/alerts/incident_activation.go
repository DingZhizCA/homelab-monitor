/*
Package alerts defines explicit Owner activation of a new rule revision.

Activation archives an older current state before clearing it. The next engine
evaluation creates state for the target revision without inheriting old timing.
*/
package alerts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

const (
	IncidentSupersedeAuditAction = "alert.incident.supersede"
	IncidentAuditResourceType    = "alert_incident"
	IncidentArchiveSuperseded    = "superseded"
)

var (
	ErrIncidentActivationConflict = errors.New("Incident activation conflict")
	ErrIncidentArchiveCapacity    = errors.New("Incident archive capacity reached")
	ErrIncidentRuleNotFound       = errors.New("Incident rule not found")
)

// IncidentArchive preserves the last state of one superseded rule revision.
type IncidentArchive struct {
	State                IncidentState `json:"state"`
	Disposition          string        `json:"disposition"`
	SupersededByRevision uint64        `json:"superseded_by_revision"`
	SupersededAt         time.Time     `json:"superseded_at"`
}

// IncidentActivationResult reports whether an old current state was archived.
type IncidentActivationResult struct {
	Ready    bool             `json:"ready"`
	Archived *IncidentArchive `json:"archived,omitempty"`
}

// IncidentActivationRepository owns the atomic archive, clear, and audit write.
type IncidentActivationRepository interface {
	ActivateRuleRevisionWithAudit(context.Context, string, string, string, uint64, audit.Event) (IncidentActivationResult, error)
}

// IncidentArchiveSource reads bounded superseded revision history.
type IncidentArchiveSource interface {
	ListArchives(context.Context, string, string, int) ([]IncidentArchive, error)
}

// IncidentArchiveService authorizes bounded archive reads.
type IncidentArchiveService struct {
	source   IncidentArchiveSource
	maxLimit int
}

// NewIncidentArchiveService requires a source and explicit per-query limit.
func NewIncidentArchiveService(source IncidentArchiveSource, maxLimit int) (*IncidentArchiveService, error) {
	if source == nil || maxLimit < 1 {
		return nil, errors.New("Incident archive source and positive query limit are required")
	}
	return &IncidentArchiveService{source: source, maxLimit: maxLimit}, nil
}

// List returns superseded revisions for one device-scoped rule.
func (s *IncidentArchiveService) List(ctx context.Context, actor auth.Principal, deviceID, ruleID string, limit int) ([]IncidentArchive, error) {
	if strings.TrimSpace(actor.UserID) == "" || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleViewer) {
		return nil, auth.ErrForbidden
	}
	if ctx == nil {
		return nil, errors.New("Incident archive query context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateIncidentIdentity(deviceID, ruleID); err != nil || limit < 1 || limit > s.maxLimit {
		return nil, fmt.Errorf("%w: valid device, rule, and bounded limit are required", ErrInvalidIncidentRequest)
	}
	archives, err := s.source.ListArchives(ctx, deviceID, ruleID, limit)
	if err != nil {
		return nil, err
	}
	if archives == nil {
		archives = make([]IncidentArchive, 0)
	}
	return archives, nil
}

// IncidentActivationService authorizes explicit revision activation.
type IncidentActivationService struct {
	repository IncidentActivationRepository
}

// NewIncidentActivationService requires an atomic activation repository.
func NewIncidentActivationService(repository IncidentActivationRepository) (*IncidentActivationService, error) {
	if repository == nil {
		return nil, errors.New("Incident activation repository is required")
	}
	return &IncidentActivationService{repository: repository}, nil
}

// ActivateRevision archives older current state so targetRevision can evaluate cleanly.
func (s *IncidentActivationService) ActivateRevision(ctx context.Context, actor auth.Principal, deviceID, ruleID string, targetRevision uint64) (IncidentActivationResult, error) {
	if strings.TrimSpace(actor.UserID) == "" || actor.Role != auth.RoleOwner {
		return IncidentActivationResult{}, auth.ErrForbidden
	}
	if ctx == nil {
		return IncidentActivationResult{}, errors.New("Incident activation context is required")
	}
	if err := ctx.Err(); err != nil {
		return IncidentActivationResult{}, err
	}
	if err := ValidateIncidentIdentity(deviceID, ruleID); err != nil || targetRevision == 0 {
		return IncidentActivationResult{}, fmt.Errorf("%w: valid device, rule, and target revision are required", ErrInvalidIncidentRequest)
	}
	event, err := audit.NewEvent(time.Now(), actor.UserID, IncidentSupersedeAuditAction, IncidentAuditResourceType, IncidentAuditResourceID(deviceID, ruleID))
	if err != nil {
		return IncidentActivationResult{}, fmt.Errorf("create Incident activation audit event: %w", err)
	}
	return s.repository.ActivateRuleRevisionWithAudit(ctx, actor.UserID, deviceID, ruleID, targetRevision, event)
}

// IncidentAuditResourceID identifies one device-scoped Incident state.
func IncidentAuditResourceID(deviceID, ruleID string) string { return deviceID + "/" + ruleID }
