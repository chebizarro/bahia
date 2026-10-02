package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// NotificationIntentPublisher publishes canonical 30900 cp-state records for
// notification channel entities.
type NotificationIntentPublisher interface {
	PublishChannel(ctx context.Context, ch *domain.NotificationChannel) error
	PublishChannelDeleted(ctx context.Context, channelID uuid.UUID) error
}

// NotificationIntentCRUD is the read/write contract the notification intent
// handler uses for level-triggered reconciliation.
type NotificationIntentCRUD interface {
	CreateChannel(ctx context.Context, ch *domain.NotificationChannel) error
	GetChannelByID(ctx context.Context, id uuid.UUID) (*domain.NotificationChannel, error)
	UpdateChannel(ctx context.Context, ch *domain.NotificationChannel) error
	DeleteChannel(ctx context.Context, id uuid.UUID) error
	ListChannels(ctx context.Context, enabledOnly bool) ([]domain.NotificationChannel, error)
}

// NotificationChannelChangeNotifier is called after every channel mutation
// so that the notification dispatcher picks up changes event-driven without
// polling.
type NotificationChannelChangeNotifier interface {
	OnChannelChanged(ch *domain.NotificationChannel, deleted bool)
}

// NotificationIntentHandler processes kind-30900 intents for the "notification"
// domain. It handles channel create/update/delete.
//
// See design §7 Wave 5 N1.
type NotificationIntentHandler struct {
	registry  NotificationIntentCRUD
	publisher NotificationIntentPublisher
	notifier  NotificationChannelChangeNotifier
	status    *IntentStatusPublisher
	logger    *zap.Logger
}

// NotificationIntentHandlerConfig configures the notification intent handler.
type NotificationIntentHandlerConfig struct {
	Registry  NotificationIntentCRUD
	Publisher NotificationIntentPublisher
	Notifier  NotificationChannelChangeNotifier
	Status    *IntentStatusPublisher
	Logger    *zap.Logger
}

// NewNotificationIntentHandler constructs the handler.
func NewNotificationIntentHandler(cfg NotificationIntentHandlerConfig) *NotificationIntentHandler {
	logger := cfg.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	return &NotificationIntentHandler{
		registry:  cfg.Registry,
		publisher: cfg.Publisher,
		notifier:  cfg.Notifier,
		status:    cfg.Status,
		logger:    logger.Named("notification-intent"),
	}
}

// HandleIntent processes a single notification intent.
func (h *NotificationIntentHandler) HandleIntent(ctx context.Context, intent *Intent) error {
	switch intent.Op {
	case "delete":
		return h.handleDelete(ctx, intent)
	default:
		return h.handleCreateOrUpdate(ctx, intent)
	}
}

// PermissionFor returns domain.PermManageSettings for all notification ops.
func (h *NotificationIntentHandler) PermissionFor(_ string) domain.Permission {
	return domain.PermManageSettings
}

func (h *NotificationIntentHandler) handleCreateOrUpdate(ctx context.Context, intent *Intent) error {
	ch, err := notificationChannelFromIntentContent(intent)
	if err != nil {
		return fmt.Errorf("parse notification channel intent: %w", err)
	}

	// Level-triggered: try to load existing.
	existing, _ := h.registry.GetChannelByID(ctx, ch.ID)
	if existing == nil {
		// Create.
		now := time.Now().UTC()
		ch.CreatedAt = now
		ch.UpdatedAt = now

		if err := h.registry.CreateChannel(ctx, ch); err != nil {
			return fmt.Errorf("create notification channel: %w", err)
		}
	} else {
		// Update: merge onto existing.
		mergeNotificationChannelOntoExisting(existing, ch)
		existing.UpdatedAt = time.Now().UTC()

		if err := h.registry.UpdateChannel(ctx, existing); err != nil {
			return fmt.Errorf("update notification channel: %w", err)
		}
		ch = existing
	}

	// Publish canonical state.
	if h.publisher != nil {
		if err := h.publisher.PublishChannel(ctx, ch); err != nil {
			h.logger.Warn("failed to publish notification channel", zap.Error(err))
		}
	}

	// Notify dispatcher of channel change (event-driven, no polling).
	if h.notifier != nil {
		h.notifier.OnChannelChanged(ch, false)
	}

	h.logger.Info("notification channel created/updated via intent",
		zap.String("channel_id", ch.ID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

func (h *NotificationIntentHandler) handleDelete(ctx context.Context, intent *Intent) error {
	idStr, _ := intent.Content["id"].(string)
	channelID, err := uuid.Parse(idStr)
	if err != nil || channelID == uuid.Nil {
		channelID, err = uuid.Parse(intent.Coordinate)
		if err != nil || channelID == uuid.Nil {
			return fmt.Errorf("delete intent must carry entity id")
		}
	}

	// Load before deleting for the notifier.
	existing, _ := h.registry.GetChannelByID(ctx, channelID)

	if err := h.registry.DeleteChannel(ctx, channelID); err != nil {
		return fmt.Errorf("delete notification channel: %w", err)
	}

	// Publish tombstone.
	if h.publisher != nil {
		if err := h.publisher.PublishChannelDeleted(ctx, channelID); err != nil {
			h.logger.Warn("failed to publish notification channel tombstone", zap.Error(err))
		}
	}

	// Notify dispatcher of channel deletion (event-driven, no polling).
	if h.notifier != nil && existing != nil {
		h.notifier.OnChannelChanged(existing, true)
	}

	h.logger.Info("notification channel deleted via intent",
		zap.String("channel_id", channelID.String()),
		zap.String("intent_id", intent.IntentID),
	)
	return nil
}

// notificationChannelFromIntentContent parses intent content into a
// NotificationChannel.
func notificationChannelFromIntentContent(intent *Intent) (*domain.NotificationChannel, error) {
	content := intent.Content
	if content == nil {
		return nil, fmt.Errorf("intent content is nil")
	}

	ch := &domain.NotificationChannel{
		OrgID:   intent.OrgID,
		Enabled: true, // default to enabled
	}

	// Parse ID.
	if idStr, ok := content["id"].(string); ok && idStr != "" {
		id, err := uuid.Parse(idStr)
		if err != nil {
			return nil, fmt.Errorf("invalid channel id %q: %w", idStr, err)
		}
		ch.ID = id
	}
	if ch.ID == uuid.Nil {
		id, err := uuid.Parse(intent.Coordinate)
		if err != nil {
			ch.ID = domain.NewEntityID()
		} else {
			ch.ID = id
		}
	}

	// Name.
	if name, ok := content["name"].(string); ok {
		ch.Name = strings.TrimSpace(name)
	}

	// Channel type.
	if ct, ok := content["channel_type"].(string); ok {
		ch.ChannelType = domain.ChannelType(strings.TrimSpace(ct))
	}

	// Config.
	if cfg, ok := content["config"].(map[string]any); ok {
		ch.Config = cfg
	}

	// Event filter.
	if ef, ok := content["event_filter"].(map[string]any); ok {
		ch.EventFilter = ef
	}

	// Enabled.
	if enabled, ok := content["enabled"].(bool); ok {
		ch.Enabled = enabled
	}

	// OrgID from content overrides tag when present.
	if orgStr, ok := content["org_id"].(string); ok && orgStr != "" {
		if orgID, err := uuid.Parse(orgStr); err == nil && orgID != uuid.Nil {
			ch.OrgID = orgID
		}
	}

	return ch, nil
}

// mergeNotificationChannelOntoExisting applies the intent's desired state
// onto the existing channel.
func mergeNotificationChannelOntoExisting(existing, intent *domain.NotificationChannel) {
	if intent.Name != "" {
		existing.Name = intent.Name
	}
	if intent.ChannelType != "" {
		existing.ChannelType = intent.ChannelType
	}
	if intent.Config != nil {
		// Preserve webhook secrets that aren't in the update.
		if existing.ChannelType == domain.ChannelTypeWebhook && intent.Config != nil {
			if _, ok := intent.Config["secret"]; !ok {
				if secret, ok := existing.Config["secret"]; ok {
					intent.Config["secret"] = secret
				}
			}
		}
		existing.Config = intent.Config
	}
	if intent.EventFilter != nil {
		existing.EventFilter = intent.EventFilter
	}
	existing.Enabled = intent.Enabled
	if intent.OrgID != uuid.Nil {
		existing.OrgID = intent.OrgID
	}
}

// BuildNotificationIntentContent builds a map suitable for an in-process
// notification channel intent from a ContextVM mutation. Used for dual dispatch.
func BuildNotificationIntentContent(ch *domain.NotificationChannel) map[string]interface{} {
	content := map[string]interface{}{
		"id":           ch.ID.String(),
		"org_id":       ch.OrgID.String(),
		"name":         ch.Name,
		"channel_type": string(ch.ChannelType),
		"config":       ch.Config,
		"event_filter": ch.EventFilter,
		"enabled":      ch.Enabled,
	}
	return content
}

// notificationIntentPayload assists JSON deserialization.
type notificationIntentPayload struct {
	ID          string         `json:"id"`
	OrgID       string         `json:"org_id,omitempty"`
	Name        string         `json:"name,omitempty"`
	ChannelType string         `json:"channel_type,omitempty"`
	Config      map[string]any `json:"config,omitempty"`
	EventFilter map[string]any `json:"event_filter,omitempty"`
	Enabled     *bool          `json:"enabled,omitempty"`
}

// ensureNotificationIntentJSON marshals any to JSON and back into a map.
func ensureNotificationIntentJSON(v any) map[string]interface{} {
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out map[string]interface{}
	_ = json.Unmarshal(data, &out)
	return out
}
