/*
Package dashboard provides an ephemeral repository for local development and tests.

The repository is process-local and loses all dashboards on restart. It is not
a production persistence implementation and does not replace live auth checks.
*/
package dashboard

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

type memoryDashboard struct {
	dashboard          Dashboard
	nextRevision       uint64
	publishedRevisions map[uint64]Document
}

// MemoryRepository stores dashboards in process memory for development and tests only.
type MemoryRepository struct {
	mu         sync.RWMutex
	dashboards map[string]*memoryDashboard
	audits     []audit.Event
}

// NewMemoryRepository creates an empty, ephemeral Dashboard repository.
func NewMemoryRepository() *MemoryRepository {
	return &MemoryRepository{dashboards: make(map[string]*memoryDashboard)}
}

func (r *MemoryRepository) ListVisible(ctx context.Context, actorID string, role auth.Role) ([]Dashboard, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return nil, err
	}
	if err := validateMemoryActor(actorID, role); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	items := make([]Dashboard, 0)
	for _, record := range r.dashboards {
		if visible, ok := visibleDashboard(record.dashboard, actorID, role); ok {
			items = append(items, visible)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (r *MemoryRepository) GetVisible(ctx context.Context, actorID string, role auth.Role, dashboardID string) (Dashboard, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	if err := validateMemoryActor(actorID, role); err != nil {
		return Dashboard{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	record, ok := r.dashboards[dashboardID]
	if !ok {
		return Dashboard{}, ErrDashboardNotFound
	}
	visible, ok := visibleDashboard(record.dashboard, actorID, role)
	if !ok {
		return Dashboard{}, ErrDashboardNotFound
	}
	return visible, nil
}

func (r *MemoryRepository) DraftForOwner(ctx context.Context, actorID, dashboardID string, expectedRevision uint64) (Document, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return Document{}, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	record, err := r.ownedSharedDashboard(actorID, dashboardID)
	if err != nil {
		return Document{}, err
	}
	if record.dashboard.Draft == nil || record.dashboard.Draft.Number != expectedRevision {
		return Document{}, ErrVersionConflict
	}
	return cloneDocument(record.dashboard.Draft.Document), nil
}

func (r *MemoryRepository) CreatePersonal(ctx context.Context, actorID, name string, document Document) (Dashboard, error) {
	return r.create(ctx, actorID, name, ScopePersonal, document)
}

func (r *MemoryRepository) CreateSharedDraft(ctx context.Context, actorID, name string, document Document, eventFactory AuditEventFactory) (Dashboard, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	if actorID == "" || eventFactory == nil {
		return Dashboard{}, ErrInvalidRequest
	}
	if err := validateMemoryName(name); err != nil {
		return Dashboard{}, err
	}
	if err := document.Validate(); err != nil {
		return Dashboard{}, fmt.Errorf("invalid Dashboard document: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	id, err := newDashboardID()
	if err != nil {
		return Dashboard{}, err
	}
	event, err := eventFactory(id)
	if err != nil {
		return Dashboard{}, fmt.Errorf("create Dashboard audit event: %w", err)
	}
	if err := validateDashboardAudit(event, actorID, DashboardAuditCreateShared, id); err != nil {
		return Dashboard{}, err
	}
	dashboard, err := r.createLockedWithID(id, actorID, name, ScopeShared, document)
	if err != nil {
		return Dashboard{}, err
	}
	r.audits = append(r.audits, event)
	return dashboard, nil
}

func (r *MemoryRepository) SavePersonal(ctx context.Context, actorID, dashboardID string, expectedRevision uint64, document Document) (Dashboard, error) {
	return r.save(ctx, actorID, dashboardID, expectedRevision, document, ScopePersonal)
}

func (r *MemoryRepository) SaveSharedDraft(ctx context.Context, actorID, dashboardID string, expectedRevision uint64, document Document, event audit.Event) (Dashboard, error) {
	return r.saveAudited(ctx, actorID, dashboardID, expectedRevision, document, ScopeShared, event, DashboardAuditSaveDraft)
}

func (r *MemoryRepository) PublishShared(ctx context.Context, actorID, dashboardID string, expectedDraftRevision uint64, event audit.Event) (Dashboard, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	record, err := r.ownedSharedDashboard(actorID, dashboardID)
	if err != nil {
		return Dashboard{}, err
	}
	if record.dashboard.Draft == nil || record.dashboard.Draft.Number != expectedDraftRevision {
		return Dashboard{}, ErrVersionConflict
	}
	if err := validateDashboardAudit(event, actorID, DashboardAuditPublish, dashboardID); err != nil {
		return Dashboard{}, err
	}
	published := cloneRevision(*record.dashboard.Draft)
	record.dashboard.Published = &published
	record.publishedRevisions[published.Number] = cloneDocument(published.Document)
	r.audits = append(r.audits, event)
	return cloneDashboard(record.dashboard), nil
}

func (r *MemoryRepository) ListPublishedRevisions(ctx context.Context, actorID, dashboardID string) ([]Revision, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return nil, err
	}
	if err := validateMemoryActor(actorID, auth.RoleOwner); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if err := checkRepositoryContext(ctx); err != nil {
		return nil, err
	}
	record, err := r.ownedSharedDashboard(actorID, dashboardID)
	if err != nil {
		return nil, err
	}
	revisions := make([]Revision, 0, len(record.publishedRevisions))
	for number, document := range record.publishedRevisions {
		revisions = append(revisions, Revision{Number: number, Document: cloneDocument(document)})
	}
	sort.Slice(revisions, func(i, j int) bool { return revisions[i].Number > revisions[j].Number })
	return revisions, nil
}

func (r *MemoryRepository) PublishHistoricalAsNew(ctx context.Context, actorID, dashboardID string, targetPublishedRevision, expectedCurrentPublishedRevision uint64, event audit.Event) (Dashboard, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	record, err := r.ownedSharedDashboard(actorID, dashboardID)
	if err != nil {
		return Dashboard{}, err
	}
	if record.dashboard.Published == nil || record.dashboard.Published.Number != expectedCurrentPublishedRevision {
		return Dashboard{}, ErrVersionConflict
	}
	target, ok := record.publishedRevisions[targetPublishedRevision]
	if !ok || targetPublishedRevision == expectedCurrentPublishedRevision {
		return Dashboard{}, ErrDashboardNotFound
	}
	if err := validateDashboardAudit(event, actorID, DashboardAuditRollback, dashboardID); err != nil {
		return Dashboard{}, err
	}
	revision := Revision{Number: record.nextRevision, Document: cloneDocument(target)}
	record.nextRevision++
	record.publishedRevisions[revision.Number] = cloneDocument(revision.Document)
	record.dashboard.Published = &revision
	r.audits = append(r.audits, event)
	return cloneDashboard(record.dashboard), nil
}

func (r *MemoryRepository) CopyVisibleToPersonal(ctx context.Context, actorID string, role auth.Role, sourceID, name string) (Dashboard, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	if err := validateMemoryActor(actorID, role); err != nil {
		return Dashboard{}, err
	}
	if err := validateMemoryName(name); err != nil {
		return Dashboard{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	source, ok := r.dashboards[sourceID]
	if !ok {
		return Dashboard{}, ErrDashboardNotFound
	}
	visible, ok := visibleDashboard(source.dashboard, actorID, role)
	if !ok {
		return Dashboard{}, ErrDashboardNotFound
	}
	var document Document
	if visible.Draft != nil {
		document = visible.Draft.Document
	} else if visible.Published != nil {
		document = visible.Published.Document
	} else {
		return Dashboard{}, ErrDashboardNotFound
	}
	return r.createLocked(actorID, name, ScopePersonal, document)
}

func (r *MemoryRepository) create(ctx context.Context, actorID, name string, scope Scope, document Document) (Dashboard, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	if actorID == "" || (scope != ScopePersonal && scope != ScopeShared) {
		return Dashboard{}, ErrInvalidRequest
	}
	if err := validateMemoryName(name); err != nil {
		return Dashboard{}, err
	}
	if err := document.Validate(); err != nil {
		return Dashboard{}, fmt.Errorf("invalid Dashboard document: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	return r.createLocked(actorID, name, scope, document)
}

func (r *MemoryRepository) createLocked(actorID, name string, scope Scope, document Document) (Dashboard, error) {
	if actorID == "" || (scope != ScopePersonal && scope != ScopeShared) {
		return Dashboard{}, ErrInvalidRequest
	}
	if err := validateMemoryName(name); err != nil {
		return Dashboard{}, err
	}
	id, err := newDashboardID()
	if err != nil {
		return Dashboard{}, err
	}
	return r.createLockedWithID(id, actorID, name, scope, document)
}

func (r *MemoryRepository) createLockedWithID(id, actorID, name string, scope Scope, document Document) (Dashboard, error) {
	if _, exists := r.dashboards[id]; exists {
		return Dashboard{}, errors.New("generated Dashboard ID already exists")
	}
	revision := Revision{Number: 1, Document: cloneDocument(document)}
	dashboard := Dashboard{ID: id, Name: name, Scope: scope, OwnerUserID: actorID, Draft: &revision}
	record := &memoryDashboard{
		dashboard: dashboard, nextRevision: 2,
		publishedRevisions: make(map[uint64]Document),
	}
	r.dashboards[id] = record
	return cloneDashboard(dashboard), nil
}

func (r *MemoryRepository) saveAudited(ctx context.Context, actorID, dashboardID string, expectedRevision uint64, document Document, scope Scope, event audit.Event, action string) (Dashboard, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	if err := document.Validate(); err != nil {
		return Dashboard{}, fmt.Errorf("invalid Dashboard document: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	record, ok := r.dashboards[dashboardID]
	if !ok || record.dashboard.Scope != scope || record.dashboard.OwnerUserID != actorID {
		return Dashboard{}, ErrDashboardNotFound
	}
	if record.dashboard.Draft == nil || record.dashboard.Draft.Number != expectedRevision {
		return Dashboard{}, ErrVersionConflict
	}
	if err := validateDashboardAudit(event, actorID, action, dashboardID); err != nil {
		return Dashboard{}, err
	}
	revision := Revision{Number: record.nextRevision, Document: cloneDocument(document)}
	record.nextRevision++
	record.dashboard.Draft = &revision
	r.audits = append(r.audits, event)
	return cloneDashboard(record.dashboard), nil
}

func validateDashboardAudit(event audit.Event, actorID, action, dashboardID string) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate Dashboard audit event: %w", err)
	}
	if event.ActorID != actorID || event.Action != action || event.ResourceType != DashboardAuditResourceType || event.ResourceID != dashboardID {
		return errors.New("Dashboard audit event does not match mutation")
	}
	return nil
}

// AuditEvents returns a copy of development-only in-memory management events.
func (r *MemoryRepository) AuditEvents() []audit.Event {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]audit.Event(nil), r.audits...)
}

func (r *MemoryRepository) save(ctx context.Context, actorID, dashboardID string, expectedRevision uint64, document Document, scope Scope) (Dashboard, error) {
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	if err := document.Validate(); err != nil {
		return Dashboard{}, fmt.Errorf("invalid Dashboard document: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := checkRepositoryContext(ctx); err != nil {
		return Dashboard{}, err
	}
	record, ok := r.dashboards[dashboardID]
	if !ok || record.dashboard.Scope != scope || record.dashboard.OwnerUserID != actorID {
		return Dashboard{}, ErrDashboardNotFound
	}
	if record.dashboard.Draft == nil || record.dashboard.Draft.Number != expectedRevision {
		return Dashboard{}, ErrVersionConflict
	}
	revision := Revision{Number: record.nextRevision, Document: cloneDocument(document)}
	record.nextRevision++
	record.dashboard.Draft = &revision
	return cloneDashboard(record.dashboard), nil
}

func (r *MemoryRepository) ownedSharedDashboard(actorID, dashboardID string) (*memoryDashboard, error) {
	record, ok := r.dashboards[dashboardID]
	if !ok || record.dashboard.Scope != ScopeShared || record.dashboard.OwnerUserID != actorID {
		return nil, ErrDashboardNotFound
	}
	return record, nil
}

func visibleDashboard(dashboard Dashboard, actorID string, role auth.Role) (Dashboard, bool) {
	switch dashboard.Scope {
	case ScopePersonal:
		if dashboard.OwnerUserID != actorID {
			return Dashboard{}, false
		}
		return cloneDashboard(dashboard), true
	case ScopeShared:
		if role == auth.RoleOwner && dashboard.OwnerUserID == actorID {
			return cloneDashboard(dashboard), true
		}
		if dashboard.Published == nil {
			return Dashboard{}, false
		}
		visible := Dashboard{ID: dashboard.ID, Name: dashboard.Name, Scope: dashboard.Scope, Published: cloneRevisionPointer(dashboard.Published)}
		return visible, true
	default:
		return Dashboard{}, false
	}
}

func checkRepositoryContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("dashboard repository context is required")
	}
	return ctx.Err()
}

func validateMemoryActor(actorID string, role auth.Role) error {
	if strings.TrimSpace(actorID) != actorID || actorID == "" || (role != auth.RoleOwner && role != auth.RoleViewer) {
		return ErrForbidden
	}
	return nil
}

func validateMemoryName(name string) error {
	if strings.TrimSpace(name) != name || name == "" {
		return fmt.Errorf("%w: dashboard name must be non-empty and trimmed", ErrInvalidRequest)
	}
	return nil
}

func newDashboardID() (string, error) {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		return "", fmt.Errorf("generate Dashboard ID: %w", err)
	}
	return hex.EncodeToString(bytes[:]), nil
}

func cloneDocument(document Document) Document {
	cloned := Document{Widgets: append([]Widget(nil), document.Widgets...), Layouts: make([]Layout, len(document.Layouts))}
	for index, layout := range document.Layouts {
		cloned.Layouts[index] = Layout{Breakpoint: layout.Breakpoint, Items: append([]Placement(nil), layout.Items...)}
	}
	return cloned
}

func cloneRevision(revision Revision) Revision {
	return Revision{Number: revision.Number, Document: cloneDocument(revision.Document)}
}

func cloneRevisionPointer(revision *Revision) *Revision {
	if revision == nil {
		return nil
	}
	cloned := cloneRevision(*revision)
	return &cloned
}

func cloneDashboard(dashboard Dashboard) Dashboard {
	dashboard.Draft = cloneRevisionPointer(dashboard.Draft)
	dashboard.Published = cloneRevisionPointer(dashboard.Published)
	return dashboard
}

var _ Repository = (*MemoryRepository)(nil)
