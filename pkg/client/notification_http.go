package client

import (
	"context"
	"net/http"

	"github.com/openagentsinc/bahia/internal/domain"
)

// ListNotificationChannels returns channels through the compatibility API.
// Deprecated: use the NostrClient confidential notification read path.
// Phase 5 F2: retained until F3 lands.
func (c *Client) ListNotificationChannels(ctx context.Context) ([]domain.NotificationChannel, error) {
	var channels []domain.NotificationChannel
	if err := c.do(ctx, http.MethodGet, "/api/v1/notifications/channels", nil, &channels); err != nil {
		return nil, err
	}
	return channels, nil
}

// GetNotificationChannel returns one channel through the compatibility API.
// Deprecated: use the NostrClient confidential notification read path.
// Phase 5 F2: retained until F3 lands.
func (c *Client) GetNotificationChannel(ctx context.Context, id string) (*domain.NotificationChannel, error) {
	var channel domain.NotificationChannel
	if err := c.do(ctx, http.MethodGet, "/api/v1/notifications/channels/"+id, nil, &channel); err != nil {
		return nil, err
	}
	return &channel, nil
}
