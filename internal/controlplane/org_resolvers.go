package controlplane

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/repository"
)

// RepoBackedNotificationOrgResolver resolves org IDs for notification channels
// by looking up the channel record.
type RepoBackedNotificationOrgResolver struct {
	notifications repository.NotificationRepository
}

// NewRepoBackedNotificationOrgResolver creates a resolver backed by the
// notification repository.
func NewRepoBackedNotificationOrgResolver(notifications repository.NotificationRepository) *RepoBackedNotificationOrgResolver {
	return &RepoBackedNotificationOrgResolver{notifications: notifications}
}

// ChannelOrgID resolves the org ID for a notification channel.
func (r *RepoBackedNotificationOrgResolver) ChannelOrgID(ctx context.Context, channelID uuid.UUID) (uuid.UUID, error) {
	if r.notifications == nil {
		return uuid.Nil, fmt.Errorf("notification repository not configured")
	}
	ch, err := r.notifications.GetChannelByID(ctx, channelID)
	if err != nil {
		return uuid.Nil, fmt.Errorf("resolve channel %s: %w", channelID, err)
	}
	if ch == nil {
		return uuid.Nil, fmt.Errorf("channel %s not found", channelID)
	}
	return ch.OrgID, nil
}
