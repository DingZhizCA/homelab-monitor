/*
Package dashboard applies Dashboard permissions and structural validation.

The repository stays injectable so these rules do not choose a persistence
engine; implementations must enforce actor state and revision checks atomically.
*/
package dashboard

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
	DashboardAuditResourceType = "dashboard"
	DashboardAuditCreateShared = "dashboard.shared.create"
	DashboardAuditSaveDraft    = "dashboard.shared.draft.save"
	DashboardAuditPublish      = "dashboard.shared.publish"
	DashboardAuditRollback     = "dashboard.shared.rollback"
)

// AuditEventFactory creates an event after a repository allocates a resource ID.
// A repository must call it before committing and commit the returned event with the resource.
type AuditEventFactory func(resourceID string) (audit.Event, error)

type Repository interface {
	ListVisible(context.Context, string, auth.Role) ([]Dashboard, error)
	GetVisible(context.Context, string, auth.Role, string) (Dashboard, error)
	DraftForOwner(context.Context, string, string, uint64) (Document, error)
	CreatePersonal(context.Context, string, string, Document) (Dashboard, error)
	CreateSharedDraft(context.Context, string, string, Document, AuditEventFactory) (Dashboard, error)
	SavePersonal(context.Context, string, string, uint64, Document) (Dashboard, error)
	SaveSharedDraft(context.Context, string, string, uint64, Document, audit.Event) (Dashboard, error)
	PublishShared(context.Context, string, string, uint64, audit.Event) (Dashboard, error)
	ListPublishedRevisions(context.Context, string, string) ([]Revision, error)
	PublishHistoricalAsNew(context.Context, string, string, uint64, uint64, audit.Event) (Dashboard, error)
	CopyVisibleToPersonal(context.Context, string, auth.Role, string, string) (Dashboard, error)
}

type PublicationPolicy interface {
	ValidateViewerAccess(context.Context, Document) error
}

type Service struct {
	repository  Repository
	publication PublicationPolicy
}

func NewService(repository Repository, publication PublicationPolicy) (*Service, error) {
	if repository == nil || publication == nil {
		return nil, errors.New("dashboard repository and publication policy are required")
	}
	return &Service{repository: repository, publication: publication}, nil
}

func (s *Service) ListVisible(ctx context.Context, actor auth.Principal) ([]Dashboard, error) {
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	return s.repository.ListVisible(ctx, actor.UserID, actor.Role)
}

func (s *Service) GetVisible(ctx context.Context, actor auth.Principal, dashboardID string) (Dashboard, error) {
	if err := validateActor(actor); err != nil {
		return Dashboard{}, err
	}
	if err := validateDashboardID(dashboardID); err != nil {
		return Dashboard{}, err
	}
	return s.repository.GetVisible(ctx, actor.UserID, actor.Role, dashboardID)
}

func (s *Service) CreatePersonal(ctx context.Context, actor auth.Principal, name string, document Document) (Dashboard, error) {
	if err := validateActor(actor); err != nil {
		return Dashboard{}, err
	}
	if err := validateNameAndDocument(name, document); err != nil {
		return Dashboard{}, err
	}
	return s.repository.CreatePersonal(ctx, actor.UserID, name, document)
}

func (s *Service) CreateSharedDraft(ctx context.Context, actor auth.Principal, name string, document Document) (Dashboard, error) {
	if actor.Role != auth.RoleOwner {
		return Dashboard{}, ErrForbidden
	}
	if err := validateActor(actor); err != nil {
		return Dashboard{}, err
	}
	if err := validateNameAndDocument(name, document); err != nil {
		return Dashboard{}, err
	}
	eventFactory := func(resourceID string) (audit.Event, error) {
		return audit.NewEvent(time.Now(), actor.UserID, DashboardAuditCreateShared, DashboardAuditResourceType, resourceID)
	}
	return s.repository.CreateSharedDraft(ctx, actor.UserID, name, document, eventFactory)
}

func (s *Service) SavePersonal(ctx context.Context, actor auth.Principal, dashboardID string, expectedRevision uint64, document Document) (Dashboard, error) {
	if err := validateActor(actor); err != nil {
		return Dashboard{}, err
	}
	if err := validateDashboardID(dashboardID); err != nil {
		return Dashboard{}, err
	}
	if expectedRevision == 0 {
		return Dashboard{}, fmt.Errorf("%w: expected revision must be positive", ErrInvalidRequest)
	}
	if err := document.Validate(); err != nil {
		return Dashboard{}, fmt.Errorf("%w: invalid document: %v", ErrInvalidRequest, err)
	}
	return s.repository.SavePersonal(ctx, actor.UserID, dashboardID, expectedRevision, document)
}

func (s *Service) SaveSharedDraft(ctx context.Context, actor auth.Principal, dashboardID string, expectedRevision uint64, document Document) (Dashboard, error) {
	if actor.Role != auth.RoleOwner {
		return Dashboard{}, ErrForbidden
	}
	if err := validateActor(actor); err != nil {
		return Dashboard{}, err
	}
	if err := validateDashboardID(dashboardID); err != nil {
		return Dashboard{}, err
	}
	if expectedRevision == 0 {
		return Dashboard{}, fmt.Errorf("%w: expected revision must be positive", ErrInvalidRequest)
	}
	if err := document.Validate(); err != nil {
		return Dashboard{}, fmt.Errorf("%w: invalid document: %v", ErrInvalidRequest, err)
	}
	event, err := audit.NewEvent(time.Now(), actor.UserID, DashboardAuditSaveDraft, DashboardAuditResourceType, dashboardID)
	if err != nil {
		return Dashboard{}, err
	}
	return s.repository.SaveSharedDraft(ctx, actor.UserID, dashboardID, expectedRevision, document, event)
}

func (s *Service) PublishShared(ctx context.Context, actor auth.Principal, dashboardID string, expectedDraftRevision uint64) (Dashboard, error) {
	if actor.Role != auth.RoleOwner {
		return Dashboard{}, ErrForbidden
	}
	if err := validateActor(actor); err != nil {
		return Dashboard{}, err
	}
	if err := validateDashboardID(dashboardID); err != nil {
		return Dashboard{}, err
	}
	if expectedDraftRevision == 0 {
		return Dashboard{}, fmt.Errorf("%w: expected draft revision must be positive", ErrInvalidRequest)
	}
	document, err := s.repository.DraftForOwner(ctx, actor.UserID, dashboardID, expectedDraftRevision)
	if err != nil {
		return Dashboard{}, err
	}
	if err := document.Validate(); err != nil {
		return Dashboard{}, fmt.Errorf("%w: invalid draft document: %v", ErrInvalidRequest, err)
	}
	if err := s.publication.ValidateViewerAccess(ctx, document); err != nil {
		return Dashboard{}, fmt.Errorf("validate Viewer access before publication: %w", err)
	}
	event, err := audit.NewEvent(time.Now(), actor.UserID, DashboardAuditPublish, DashboardAuditResourceType, dashboardID)
	if err != nil {
		return Dashboard{}, err
	}
	return s.repository.PublishShared(ctx, actor.UserID, dashboardID, expectedDraftRevision, event)
}

func (s *Service) RollbackShared(ctx context.Context, actor auth.Principal, dashboardID string, targetPublishedRevision, expectedCurrentPublishedRevision uint64) (Dashboard, error) {
	if actor.Role != auth.RoleOwner {
		return Dashboard{}, ErrForbidden
	}
	if err := validateActor(actor); err != nil {
		return Dashboard{}, err
	}
	if err := validateDashboardID(dashboardID); err != nil {
		return Dashboard{}, err
	}
	if targetPublishedRevision == 0 || expectedCurrentPublishedRevision == 0 {
		return Dashboard{}, fmt.Errorf("%w: target and expected published revisions must be positive", ErrInvalidRequest)
	}
	if targetPublishedRevision == expectedCurrentPublishedRevision {
		return Dashboard{}, fmt.Errorf("%w: rollback target must differ from current published revision", ErrInvalidRequest)
	}
	event, err := audit.NewEvent(time.Now(), actor.UserID, DashboardAuditRollback, DashboardAuditResourceType, dashboardID)
	if err != nil {
		return Dashboard{}, err
	}
	return s.repository.PublishHistoricalAsNew(ctx, actor.UserID, dashboardID, targetPublishedRevision, expectedCurrentPublishedRevision, event)
}

// ListPublishedRevisions returns immutable shared publication history to its Owner.
func (s *Service) ListPublishedRevisions(ctx context.Context, actor auth.Principal, dashboardID string) ([]Revision, error) {
	if actor.Role != auth.RoleOwner {
		return nil, ErrForbidden
	}
	if err := validateActor(actor); err != nil {
		return nil, err
	}
	if err := validateDashboardID(dashboardID); err != nil {
		return nil, err
	}
	return s.repository.ListPublishedRevisions(ctx, actor.UserID, dashboardID)
}

func (s *Service) CopyVisibleToPersonal(ctx context.Context, actor auth.Principal, sourceID, name string) (Dashboard, error) {
	if err := validateActor(actor); err != nil {
		return Dashboard{}, err
	}
	if err := validateDashboardID(sourceID); err != nil {
		return Dashboard{}, err
	}
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return Dashboard{}, fmt.Errorf("%w: dashboard name must be non-empty and trimmed", ErrInvalidRequest)
	}
	return s.repository.CopyVisibleToPersonal(ctx, actor.UserID, actor.Role, sourceID, name)
}

func validateActor(actor auth.Principal) error {
	if strings.TrimSpace(actor.UserID) == "" || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleViewer) {
		return ErrForbidden
	}
	return nil
}

func validateDashboardID(id string) error {
	if !documentIDPattern.MatchString(id) {
		return fmt.Errorf("%w: dashboard ID is invalid", ErrInvalidRequest)
	}
	return nil
}

func validateNameAndDocument(name string, document Document) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("%w: dashboard name must be non-empty and trimmed", ErrInvalidRequest)
	}
	if err := document.Validate(); err != nil {
		return fmt.Errorf("%w: invalid document: %v", ErrInvalidRequest, err)
	}
	return nil
}
