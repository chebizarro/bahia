package domain

import (
	"time"

	"github.com/google/uuid"
)

// ChannelType identifies the delivery mechanism for notifications.
type ChannelType string

const (
	ChannelTypeWebhook ChannelType = "webhook"
	ChannelTypeNostrDM ChannelType = "nostr_dm"
)

// NotificationStatus tracks the delivery state of a notification.
type NotificationStatus string

const (
	NotificationStatusSent     NotificationStatus = "sent"
	NotificationStatusFailed   NotificationStatus = "failed"
	NotificationStatusRetrying NotificationStatus = "retrying"
	NotificationStatusPending  NotificationStatus = "pending"
)

// NotificationChannel defines a destination for event notifications.
type NotificationChannel struct {
	ID          uuid.UUID      `json:"id"`
	OrgID       uuid.UUID      `json:"org_id"`
	Name        string         `json:"name"`
	ChannelType ChannelType    `json:"channel_type"`
	Config      map[string]any `json:"config"`       // type-specific config (url, pubkey, etc.)
	EventFilter map[string]any `json:"event_filter"` // which event types to send
	Enabled     bool           `json:"enabled"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
}

// NotificationLog records a notification delivery attempt.
type NotificationLog struct {
	ID        uuid.UUID          `json:"id"`
	ChannelID uuid.UUID          `json:"channel_id"`
	EventType string             `json:"event_type"`
	Payload   map[string]any     `json:"payload"`
	Status    NotificationStatus `json:"status"`
	Attempts  int                `json:"attempts"`
	LastError string             `json:"last_error,omitempty"`
	CreatedAt time.Time          `json:"created_at"`
	UpdatedAt time.Time          `json:"updated_at"`
}

// MatchesEvent checks if this channel's event filter matches the given event type.
// An empty filter matches all events.
func (c *NotificationChannel) MatchesEvent(eventType string) bool {
	if len(c.EventFilter) == 0 {
		return true
	}

	// Check "types" array filter.
	if types, ok := c.EventFilter["types"].([]any); ok {
		for _, t := range types {
			if s, ok := t.(string); ok && s == eventType {
				return true
			}
		}
		return false
	}

	// Check "type" single value.
	if t, ok := c.EventFilter["type"].(string); ok {
		return t == eventType || t == "*"
	}

	return true // no recognized filter = match all
}
