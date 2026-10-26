/*
Package notifications exposes per-user in-app records while keeping user
identity bound to the authenticated principal supplied by the Server.
*/
package notifications

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/DingZhizCA/homelab-monitor/internal/auth"
)

var ErrNotificationNotFound = errors.New("recipient notification not found")

type InAppNotification struct {
	Notification Notification `json:"notification"`
	ReadAt       *time.Time   `json:"read_at,omitempty"`
}

type InAppRepository interface {
	ListForUser(ctx context.Context, userID string, limit int) ([]InAppNotification, error)
	MarkRead(ctx context.Context, userID, notificationID string, readAt time.Time) error
}

type InAppService struct {
	repository InAppRepository
	now        func() time.Time
}

func NewInAppService(repository InAppRepository, now func() time.Time) (*InAppService, error) {
	if repository == nil || now == nil {
		return nil, errors.New("in-app notification repository and clock are required")
	}
	return &InAppService{repository: repository, now: now}, nil
}

func (s *InAppService) List(ctx context.Context, actor auth.Principal, limit int) ([]InAppNotification, error) {
	if err := authorizeNotificationUser(ctx, actor); err != nil {
		return nil, err
	}
	return s.repository.ListForUser(ctx, actor.UserID, limit)
}

func (s *InAppService) MarkRead(ctx context.Context, actor auth.Principal, notificationID string) error {
	if err := authorizeNotificationUser(ctx, actor); err != nil {
		return err
	}
	if ValidateIntentID(notificationID) != nil {
		return ErrNotificationNotFound
	}
	return s.repository.MarkRead(ctx, actor.UserID, notificationID, s.now().UTC())
}

func authorizeNotificationUser(ctx context.Context, actor auth.Principal) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(actor.UserID) == "" || (actor.Role != auth.RoleOwner && actor.Role != auth.RoleViewer) {
		return auth.ErrForbidden
	}
	return nil
}
