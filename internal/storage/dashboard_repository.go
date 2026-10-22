/*
Package storage persists Dashboard drafts, immutable revisions, publications,
and matching management audit events in core.db. Authorization and publication
policy stay in the dashboard service; this adapter rechecks active actors and
commits revision comparisons atomically.
*/
package storage

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/DingZhizCA/homelab-monitor/internal/audit"
	"github.com/DingZhizCA/homelab-monitor/internal/auth"
	"github.com/DingZhizCA/homelab-monitor/internal/dashboard"
)

// DashboardRepository implements Dashboard persistence against core.db.
type DashboardRepository struct {
	database *sql.DB
}

var _ dashboard.Repository = (*DashboardRepository)(nil)

// NewDashboardRepository binds Dashboard operations to core.db.
func NewDashboardRepository(database *sql.DB) (*DashboardRepository, error) {
	if database == nil {
		return nil, errors.New("core database is required")
	}
	return &DashboardRepository{database: database}, nil
}

func (r *DashboardRepository) ListVisible(ctx context.Context, actorID string, role auth.Role) ([]dashboard.Dashboard, error) {
	if err := requireDashboardActor(ctx, r.database, actorID, role); err != nil {
		return nil, err
	}
	rows, err := r.database.QueryContext(ctx, dashboardSelect+`
		WHERE (d.scope = 'personal' AND d.owner_user_id = ?)
		   OR (d.scope = 'shared' AND (d.owner_user_id = ? OR d.published_revision IS NOT NULL))
		ORDER BY d.dashboard_id`, actorID, actorID)
	if err != nil {
		return nil, fmt.Errorf("list visible Dashboards: %w", err)
	}
	defer rows.Close()
	items := make([]dashboard.Dashboard, 0)
	for rows.Next() {
		item, err := scanDashboard(rows)
		if err != nil {
			return nil, fmt.Errorf("scan visible Dashboard: %w", err)
		}
		visible, ok := dashboardVisible(item, actorID, role)
		if ok {
			items = append(items, visible)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate visible Dashboards: %w", err)
	}
	return items, nil
}

func (r *DashboardRepository) GetVisible(ctx context.Context, actorID string, role auth.Role, dashboardID string) (dashboard.Dashboard, error) {
	if err := requireDashboardActor(ctx, r.database, actorID, role); err != nil {
		return dashboard.Dashboard{}, err
	}
	item, err := readDashboard(ctx, r.database, dashboardID)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	visible, ok := dashboardVisible(item, actorID, role)
	if !ok {
		return dashboard.Dashboard{}, dashboard.ErrDashboardNotFound
	}
	return visible, nil
}

func (r *DashboardRepository) DraftForOwner(ctx context.Context, actorID, dashboardID string, expectedRevision uint64) (dashboard.Document, error) {
	if err := requireDashboardActor(ctx, r.database, actorID, auth.RoleOwner); err != nil {
		return dashboard.Document{}, err
	}
	item, err := readDashboard(ctx, r.database, dashboardID)
	if err != nil {
		return dashboard.Document{}, err
	}
	if item.Scope != dashboard.ScopeShared || item.OwnerUserID != actorID {
		return dashboard.Document{}, dashboard.ErrDashboardNotFound
	}
	if item.Draft == nil || item.Draft.Number != expectedRevision {
		return dashboard.Document{}, dashboard.ErrVersionConflict
	}
	return item.Draft.Document, nil
}

func (r *DashboardRepository) CreatePersonal(ctx context.Context, actorID, name string, document dashboard.Document) (dashboard.Dashboard, error) {
	return r.create(ctx, actorID, name, dashboard.ScopePersonal, document, nil)
}

func (r *DashboardRepository) CreateSharedDraft(ctx context.Context, actorID, name string, document dashboard.Document, eventFactory dashboard.AuditEventFactory) (dashboard.Dashboard, error) {
	if eventFactory == nil {
		return dashboard.Dashboard{}, dashboard.ErrInvalidRequest
	}
	id, err := newStoredDashboardID()
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	event, err := eventFactory(id)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("create Dashboard audit event: %w", err)
	}
	return r.createWithID(ctx, id, actorID, name, dashboard.ScopeShared, document, &event)
}

func (r *DashboardRepository) SavePersonal(ctx context.Context, actorID, dashboardID string, expectedRevision uint64, document dashboard.Document) (dashboard.Dashboard, error) {
	return r.save(ctx, actorID, dashboardID, expectedRevision, document, dashboard.ScopePersonal, nil)
}

func (r *DashboardRepository) SaveSharedDraft(ctx context.Context, actorID, dashboardID string, expectedRevision uint64, document dashboard.Document, event audit.Event) (dashboard.Dashboard, error) {
	return r.save(ctx, actorID, dashboardID, expectedRevision, document, dashboard.ScopeShared, &event)
}

func (r *DashboardRepository) PublishShared(ctx context.Context, actorID, dashboardID string, expectedDraftRevision uint64, event audit.Event) (dashboard.Dashboard, error) {
	if err := validateStoredDashboardAudit(event, actorID, dashboard.DashboardAuditPublish, dashboardID); err != nil {
		return dashboard.Dashboard{}, err
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("begin Dashboard publication: %w", err)
	}
	defer transaction.Rollback()
	if err := requireDashboardActor(ctx, transaction, actorID, auth.RoleOwner); err != nil {
		return dashboard.Dashboard{}, err
	}
	item, err := readDashboard(ctx, transaction, dashboardID)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	if item.Scope != dashboard.ScopeShared || item.OwnerUserID != actorID {
		return dashboard.Dashboard{}, dashboard.ErrDashboardNotFound
	}
	if item.Draft == nil || item.Draft.Number != expectedDraftRevision {
		return dashboard.Dashboard{}, dashboard.ErrVersionConflict
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE dashboard_revisions SET published = 1 WHERE dashboard_id = ? AND revision = ?`, dashboardID, expectedDraftRevision); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("mark Dashboard revision published: %w", err)
	}
	result, err := transaction.ExecContext(ctx, `UPDATE dashboards SET published_revision = ? WHERE dashboard_id = ? AND draft_revision = ?`, expectedDraftRevision, dashboardID, expectedDraftRevision)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("publish Dashboard revision: %w", err)
	}
	if err := requireOneDashboardChange(result); err != nil {
		return dashboard.Dashboard{}, err
	}
	if err := insertAuditEvent(ctx, transaction, event); err != nil {
		return dashboard.Dashboard{}, err
	}
	item, err = readDashboard(ctx, transaction, dashboardID)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	if err := transaction.Commit(); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("commit Dashboard publication: %w", err)
	}
	return item, nil
}

func (r *DashboardRepository) ListPublishedRevisions(ctx context.Context, actorID, dashboardID string) ([]dashboard.Revision, error) {
	if err := requireDashboardActor(ctx, r.database, actorID, auth.RoleOwner); err != nil {
		return nil, err
	}
	var ownerID string
	var scope dashboard.Scope
	if err := r.database.QueryRowContext(ctx, `SELECT owner_user_id, scope FROM dashboards WHERE dashboard_id = ?`, dashboardID).Scan(&ownerID, &scope); errors.Is(err, sql.ErrNoRows) {
		return nil, dashboard.ErrDashboardNotFound
	} else if err != nil {
		return nil, fmt.Errorf("read Dashboard publication owner: %w", err)
	}
	if ownerID != actorID || scope != dashboard.ScopeShared {
		return nil, dashboard.ErrDashboardNotFound
	}
	rows, err := r.database.QueryContext(ctx, `SELECT revision, document_json FROM dashboard_revisions WHERE dashboard_id = ? AND published = 1 ORDER BY revision DESC`, dashboardID)
	if err != nil {
		return nil, fmt.Errorf("list Dashboard publications: %w", err)
	}
	defer rows.Close()
	revisions := make([]dashboard.Revision, 0)
	for rows.Next() {
		var number uint64
		var encoded []byte
		if err := rows.Scan(&number, &encoded); err != nil {
			return nil, fmt.Errorf("scan Dashboard publication: %w", err)
		}
		document, err := decodeDashboardDocument(encoded)
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, dashboard.Revision{Number: number, Document: document})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Dashboard publications: %w", err)
	}
	return revisions, nil
}

func (r *DashboardRepository) PublishHistoricalAsNew(ctx context.Context, actorID, dashboardID string, targetPublishedRevision, expectedCurrentPublishedRevision uint64, event audit.Event) (dashboard.Dashboard, error) {
	if err := validateStoredDashboardAudit(event, actorID, dashboard.DashboardAuditRollback, dashboardID); err != nil {
		return dashboard.Dashboard{}, err
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("begin Dashboard rollback: %w", err)
	}
	defer transaction.Rollback()
	if err := requireDashboardActor(ctx, transaction, actorID, auth.RoleOwner); err != nil {
		return dashboard.Dashboard{}, err
	}
	item, nextRevision, err := readOwnedDashboardForWrite(ctx, transaction, actorID, dashboardID, dashboard.ScopeShared)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	if item.Published == nil || item.Published.Number != expectedCurrentPublishedRevision {
		return dashboard.Dashboard{}, dashboard.ErrVersionConflict
	}
	if targetPublishedRevision == expectedCurrentPublishedRevision {
		return dashboard.Dashboard{}, dashboard.ErrDashboardNotFound
	}
	var encoded []byte
	if err := transaction.QueryRowContext(ctx, `SELECT document_json FROM dashboard_revisions WHERE dashboard_id = ? AND revision = ? AND published = 1`, dashboardID, targetPublishedRevision).Scan(&encoded); errors.Is(err, sql.ErrNoRows) {
		return dashboard.Dashboard{}, dashboard.ErrDashboardNotFound
	} else if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("read Dashboard rollback target: %w", err)
	}
	if _, err := decodeDashboardDocument(encoded); err != nil {
		return dashboard.Dashboard{}, err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO dashboard_revisions (dashboard_id, revision, document_json, published) VALUES (?, ?, ?, 1)`, dashboardID, nextRevision, encoded); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("insert Dashboard rollback revision: %w", err)
	}
	result, err := transaction.ExecContext(ctx, `UPDATE dashboards SET published_revision = ?, next_revision = ? WHERE dashboard_id = ? AND published_revision = ? AND next_revision = ?`, nextRevision, nextRevision+1, dashboardID, expectedCurrentPublishedRevision, nextRevision)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("commit Dashboard rollback pointer: %w", err)
	}
	if err := requireOneDashboardChange(result); err != nil {
		return dashboard.Dashboard{}, err
	}
	if err := insertAuditEvent(ctx, transaction, event); err != nil {
		return dashboard.Dashboard{}, err
	}
	item, err = readDashboard(ctx, transaction, dashboardID)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	if err := transaction.Commit(); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("commit Dashboard rollback: %w", err)
	}
	return item, nil
}

func (r *DashboardRepository) CopyVisibleToPersonal(ctx context.Context, actorID string, role auth.Role, sourceID, name string) (dashboard.Dashboard, error) {
	if err := validateStoredDashboardName(name); err != nil {
		return dashboard.Dashboard{}, err
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("begin Dashboard copy: %w", err)
	}
	defer transaction.Rollback()
	if err := requireDashboardActor(ctx, transaction, actorID, role); err != nil {
		return dashboard.Dashboard{}, err
	}
	source, err := readDashboard(ctx, transaction, sourceID)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	visible, ok := dashboardVisible(source, actorID, role)
	if !ok {
		return dashboard.Dashboard{}, dashboard.ErrDashboardNotFound
	}
	var document dashboard.Document
	if visible.Draft != nil {
		document = visible.Draft.Document
	} else if visible.Published != nil {
		document = visible.Published.Document
	} else {
		return dashboard.Dashboard{}, dashboard.ErrDashboardNotFound
	}
	id, err := newStoredDashboardID()
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	item, err := insertDashboard(ctx, transaction, id, actorID, name, dashboard.ScopePersonal, document)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	if err := transaction.Commit(); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("commit Dashboard copy: %w", err)
	}
	return item, nil
}

func (r *DashboardRepository) create(ctx context.Context, actorID, name string, scope dashboard.Scope, document dashboard.Document, event *audit.Event) (dashboard.Dashboard, error) {
	id, err := newStoredDashboardID()
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	return r.createWithID(ctx, id, actorID, name, scope, document, event)
}

func (r *DashboardRepository) createWithID(ctx context.Context, id, actorID, name string, scope dashboard.Scope, document dashboard.Document, event *audit.Event) (dashboard.Dashboard, error) {
	if err := validateStoredDashboardName(name); err != nil {
		return dashboard.Dashboard{}, err
	}
	if err := document.Validate(); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("%w: invalid Dashboard document: %v", dashboard.ErrInvalidRequest, err)
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("begin Dashboard creation: %w", err)
	}
	defer transaction.Rollback()
	requiredRole := auth.Role("")
	if scope == dashboard.ScopeShared {
		requiredRole = auth.RoleOwner
	}
	if err := requireDashboardActor(ctx, transaction, actorID, requiredRole); err != nil {
		return dashboard.Dashboard{}, err
	}
	if scope != dashboard.ScopePersonal && scope != dashboard.ScopeShared {
		return dashboard.Dashboard{}, dashboard.ErrInvalidRequest
	}
	if scope == dashboard.ScopeShared {
		if event == nil {
			return dashboard.Dashboard{}, dashboard.ErrInvalidRequest
		}
		if err := validateStoredDashboardAudit(*event, actorID, dashboard.DashboardAuditCreateShared, id); err != nil {
			return dashboard.Dashboard{}, err
		}
	}
	item, err := insertDashboard(ctx, transaction, id, actorID, name, scope, document)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	if event != nil {
		if err := insertAuditEvent(ctx, transaction, *event); err != nil {
			return dashboard.Dashboard{}, err
		}
	}
	if err := transaction.Commit(); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("commit Dashboard creation: %w", err)
	}
	return item, nil
}

func (r *DashboardRepository) save(ctx context.Context, actorID, dashboardID string, expectedRevision uint64, document dashboard.Document, scope dashboard.Scope, event *audit.Event) (dashboard.Dashboard, error) {
	if err := document.Validate(); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("%w: invalid Dashboard document: %v", dashboard.ErrInvalidRequest, err)
	}
	transaction, err := r.database.BeginTx(ctx, nil)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("begin Dashboard save: %w", err)
	}
	defer transaction.Rollback()
	requiredRole := auth.Role("")
	if scope == dashboard.ScopeShared {
		requiredRole = auth.RoleOwner
	}
	if err := requireDashboardActor(ctx, transaction, actorID, requiredRole); err != nil {
		return dashboard.Dashboard{}, err
	}
	item, nextRevision, err := readOwnedDashboardForWrite(ctx, transaction, actorID, dashboardID, scope)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	if item.Draft == nil || item.Draft.Number != expectedRevision {
		return dashboard.Dashboard{}, dashboard.ErrVersionConflict
	}
	if scope == dashboard.ScopeShared {
		if event == nil {
			return dashboard.Dashboard{}, dashboard.ErrInvalidRequest
		}
		if err := validateStoredDashboardAudit(*event, actorID, dashboard.DashboardAuditSaveDraft, dashboardID); err != nil {
			return dashboard.Dashboard{}, err
		}
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("encode Dashboard document: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO dashboard_revisions (dashboard_id, revision, document_json, published) VALUES (?, ?, ?, 0)`, dashboardID, nextRevision, encoded); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("insert Dashboard revision: %w", err)
	}
	result, err := transaction.ExecContext(ctx, `UPDATE dashboards SET draft_revision = ?, next_revision = ? WHERE dashboard_id = ? AND draft_revision = ? AND next_revision = ?`, nextRevision, nextRevision+1, dashboardID, expectedRevision, nextRevision)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("advance Dashboard draft: %w", err)
	}
	if err := requireOneDashboardChange(result); err != nil {
		return dashboard.Dashboard{}, err
	}
	if event != nil {
		if err := insertAuditEvent(ctx, transaction, *event); err != nil {
			return dashboard.Dashboard{}, err
		}
	}
	item, err = readDashboard(ctx, transaction, dashboardID)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	if err := transaction.Commit(); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("commit Dashboard save: %w", err)
	}
	return item, nil
}

const dashboardSelect = `SELECT d.dashboard_id, d.name, d.scope, d.owner_user_id,
	d.draft_revision, draft.document_json, d.published_revision, published.document_json
	FROM dashboards d
	JOIN dashboard_revisions draft ON draft.dashboard_id = d.dashboard_id AND draft.revision = d.draft_revision
	LEFT JOIN dashboard_revisions published ON published.dashboard_id = d.dashboard_id AND published.revision = d.published_revision`

type dashboardQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type dashboardScanner interface {
	Scan(...any) error
}

func readDashboard(ctx context.Context, querier dashboardQuerier, dashboardID string) (dashboard.Dashboard, error) {
	item, err := scanDashboard(querier.QueryRowContext(ctx, dashboardSelect+` WHERE d.dashboard_id = ?`, dashboardID))
	if errors.Is(err, sql.ErrNoRows) {
		return dashboard.Dashboard{}, dashboard.ErrDashboardNotFound
	}
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("read Dashboard: %w", err)
	}
	return item, nil
}

func scanDashboard(scanner dashboardScanner) (dashboard.Dashboard, error) {
	var item dashboard.Dashboard
	var draftNumber uint64
	var draftJSON []byte
	var publishedNumber sql.NullInt64
	var publishedJSON []byte
	if err := scanner.Scan(&item.ID, &item.Name, &item.Scope, &item.OwnerUserID, &draftNumber, &draftJSON, &publishedNumber, &publishedJSON); err != nil {
		return dashboard.Dashboard{}, err
	}
	if item.Scope != dashboard.ScopePersonal && item.Scope != dashboard.ScopeShared {
		return dashboard.Dashboard{}, errors.New("stored Dashboard scope is invalid")
	}
	draft, err := decodeDashboardDocument(draftJSON)
	if err != nil {
		return dashboard.Dashboard{}, err
	}
	item.Draft = &dashboard.Revision{Number: draftNumber, Document: draft}
	if publishedNumber.Valid {
		if publishedNumber.Int64 < 1 || len(publishedJSON) == 0 {
			return dashboard.Dashboard{}, errors.New("stored Dashboard publication is invalid")
		}
		published, err := decodeDashboardDocument(publishedJSON)
		if err != nil {
			return dashboard.Dashboard{}, err
		}
		item.Published = &dashboard.Revision{Number: uint64(publishedNumber.Int64), Document: published}
	}
	return item, nil
}

func readOwnedDashboardForWrite(ctx context.Context, transaction *sql.Tx, actorID, dashboardID string, scope dashboard.Scope) (dashboard.Dashboard, uint64, error) {
	item, err := readDashboard(ctx, transaction, dashboardID)
	if err != nil {
		return dashboard.Dashboard{}, 0, err
	}
	if item.OwnerUserID != actorID || item.Scope != scope {
		return dashboard.Dashboard{}, 0, dashboard.ErrDashboardNotFound
	}
	var nextRevision uint64
	if err := transaction.QueryRowContext(ctx, `SELECT next_revision FROM dashboards WHERE dashboard_id = ?`, dashboardID).Scan(&nextRevision); err != nil {
		return dashboard.Dashboard{}, 0, fmt.Errorf("read Dashboard next revision: %w", err)
	}
	if nextRevision < 2 {
		return dashboard.Dashboard{}, 0, errors.New("stored Dashboard next revision is invalid")
	}
	return item, nextRevision, nil
}

func insertDashboard(ctx context.Context, transaction *sql.Tx, id, actorID, name string, scope dashboard.Scope, document dashboard.Document) (dashboard.Dashboard, error) {
	encoded, err := json.Marshal(document)
	if err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("encode Dashboard document: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO dashboards (dashboard_id, name, scope, owner_user_id, next_revision, draft_revision) VALUES (?, ?, ?, ?, 2, 1)`, id, name, scope, actorID); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("insert Dashboard: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO dashboard_revisions (dashboard_id, revision, document_json, published) VALUES (?, 1, ?, 0)`, id, encoded); err != nil {
		return dashboard.Dashboard{}, fmt.Errorf("insert initial Dashboard revision: %w", err)
	}
	return dashboard.Dashboard{ID: id, Name: name, Scope: scope, OwnerUserID: actorID, Draft: &dashboard.Revision{Number: 1, Document: document}}, nil
}

func decodeDashboardDocument(encoded []byte) (dashboard.Document, error) {
	var document dashboard.Document
	if err := json.Unmarshal(encoded, &document); err != nil {
		return dashboard.Document{}, fmt.Errorf("decode stored Dashboard document: %w", err)
	}
	if err := document.Validate(); err != nil {
		return dashboard.Document{}, fmt.Errorf("validate stored Dashboard document: %w", err)
	}
	return document, nil
}

func requireDashboardActor(ctx context.Context, querier dashboardQuerier, actorID string, expectedRole auth.Role) error {
	if strings.TrimSpace(actorID) == "" || strings.TrimSpace(actorID) != actorID {
		return dashboard.ErrForbidden
	}
	var role auth.Role
	var disabled int
	err := querier.QueryRowContext(ctx, `SELECT role, disabled FROM users WHERE user_id = ?`, actorID).Scan(&role, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return dashboard.ErrForbidden
	}
	if err != nil {
		return fmt.Errorf("verify Dashboard actor: %w", err)
	}
	if disabled != 0 || (expectedRole != "" && role != expectedRole) || (role != auth.RoleOwner && role != auth.RoleViewer) {
		return dashboard.ErrForbidden
	}
	return nil
}

func validateStoredDashboardName(name string) error {
	if name == "" || strings.TrimSpace(name) != name {
		return fmt.Errorf("%w: Dashboard name must be non-empty and trimmed", dashboard.ErrInvalidRequest)
	}
	return nil
}

func validateStoredDashboardAudit(event audit.Event, actorID, action, dashboardID string) error {
	if err := event.Validate(); err != nil {
		return fmt.Errorf("validate Dashboard audit event: %w", err)
	}
	if event.ActorID != actorID || event.Action != action || event.ResourceType != dashboard.DashboardAuditResourceType || event.ResourceID != dashboardID {
		return errors.New("Dashboard audit event does not match mutation")
	}
	return nil
}

func dashboardVisible(item dashboard.Dashboard, actorID string, role auth.Role) (dashboard.Dashboard, bool) {
	if item.Scope == dashboard.ScopePersonal {
		return item, item.OwnerUserID == actorID
	}
	if item.Scope != dashboard.ScopeShared {
		return dashboard.Dashboard{}, false
	}
	if role == auth.RoleOwner && item.OwnerUserID == actorID {
		return item, true
	}
	if item.Published == nil {
		return dashboard.Dashboard{}, false
	}
	return dashboard.Dashboard{ID: item.ID, Name: item.Name, Scope: item.Scope, Published: item.Published}, true
}

func requireOneDashboardChange(result sql.Result) error {
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read Dashboard mutation result: %w", err)
	}
	if changed != 1 {
		return dashboard.ErrVersionConflict
	}
	return nil
}

func newStoredDashboardID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate Dashboard ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
