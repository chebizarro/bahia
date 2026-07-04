package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/repository"
)

const (
	EncryptedOperationNotificationChannelsList   = "notifications.channels.list"
	EncryptedOperationNotificationChannelsGet    = "notifications.channels.get"
	EncryptedOperationNotificationChannelsCreate = "notifications.channels.create"
	EncryptedOperationNotificationChannelsUpdate = "notifications.channels.update"
	EncryptedOperationNotificationChannelsDelete = "notifications.channels.delete"
	EncryptedOperationNotificationChannelsTest   = "notifications.channels.test"
	EncryptedOperationNotificationLogsList       = "notifications.logs.list"
)

type notificationEncryptedHandler struct {
	repo       repository.NotificationRepository
	dispatcher *notifications.Dispatcher
}

type notificationChannelPayload struct {
	ID          string         `json:"id,omitempty"`
	Name        string         `json:"name,omitempty"`
	ChannelType string         `json:"channel_type,omitempty"`
	Config      map[string]any `json:"config,omitempty"`
	EventFilter map[string]any `json:"event_filter,omitempty"`
	Enabled     *bool          `json:"enabled,omitempty"`
}

type notificationLogsPayload struct {
	ChannelID string `json:"channel_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

// RegisterNotificationEncryptedHandlers wires notification CRUD/test/log queries
// onto the ContextVM encrypted control-plane runtime. Notification configs and
// delivery logs are never projected to the public sidecar; result payloads are
// returned through ContextVM responses.
func RegisterNotificationEncryptedHandlers(transport *EncryptedRequestTransport, repo repository.NotificationRepository, dispatcher *notifications.Dispatcher) {
	if transport == nil || repo == nil {
		return
	}
	h := &notificationEncryptedHandler{repo: repo, dispatcher: dispatcher}
	h.register(transport, EncryptedOperationNotificationChannelsList, h.listChannels, "notifications/list")
	h.register(transport, EncryptedOperationNotificationChannelsGet, h.getChannel, "notifications/get")
	h.register(transport, EncryptedOperationNotificationChannelsCreate, h.createChannel, "notifications/new", "notifications/create")
	h.register(transport, EncryptedOperationNotificationChannelsUpdate, h.updateChannel, "notifications/update")
	h.register(transport, EncryptedOperationNotificationChannelsDelete, h.deleteChannel, "notifications/delete")
	h.register(transport, EncryptedOperationNotificationChannelsTest, h.testChannel, "notifications/test")
	h.register(transport, EncryptedOperationNotificationLogsList, h.listLogs, "notifications/logs")
}

func (h *notificationEncryptedHandler) register(transport *EncryptedRequestTransport, operation string, handler EncryptedRequestHandler, contextVMAliases ...string) {
	transport.RegisterHandler(operation, handler)
	register := func(method string) {
		transport.RegisterContextVMHandler(method, func(ctx context.Context, request ContextVMRequest) (any, error) {
			return handler(ctx, EncryptedRequest{
				Event: request.Event,
				Envelope: EncryptedRequestEnvelope{
					Version:         ContextVMWireVersion,
					Operation:       request.RPC.Method,
					RequesterPubkey: request.Event.PubKey.Hex(),
					Payload:         request.RPC.Params,
				},
			})
		})
	}
	register(operation)
	for _, alias := range contextVMAliases {
		register(alias)
	}
}

func (h *notificationEncryptedHandler) listChannels(ctx context.Context, _ EncryptedRequest) (any, error) {
	channels, err := h.repo.ListChannels(ctx, false)
	if err != nil {
		return nil, fmt.Errorf("failed to list notification channels")
	}
	return map[string]any{"channels": sanitizeNotificationChannels(channels)}, nil
}

func (h *notificationEncryptedHandler) getChannel(ctx context.Context, request EncryptedRequest) (any, error) {
	var payload notificationChannelPayload
	if err := decodeNotificationEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	id, err := parseNotificationChannelID(payload.ID)
	if err != nil {
		return nil, err
	}
	ch, err := h.repo.GetChannelByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get notification channel")
	}
	if ch == nil {
		return nil, fmt.Errorf("notification channel not found")
	}
	return map[string]any{"channel": sanitizeNotificationChannel(*ch)}, nil
}

func (h *notificationEncryptedHandler) createChannel(ctx context.Context, request EncryptedRequest) (any, error) {
	var payload notificationChannelPayload
	if err := decodeNotificationEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.Name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	channelType, err := parseNotificationChannelType(payload.ChannelType)
	if err != nil {
		return nil, err
	}
	enabled := true
	if payload.Enabled != nil {
		enabled = *payload.Enabled
	}
	ch := &domain.NotificationChannel{
		ID:          uuid.New(),
		Name:        strings.TrimSpace(payload.Name),
		ChannelType: channelType,
		Config:      cloneMap(payload.Config),
		EventFilter: cloneMap(payload.EventFilter),
		Enabled:     enabled,
	}
	if err := h.repo.CreateChannel(ctx, ch); err != nil {
		return nil, fmt.Errorf("failed to create notification channel")
	}
	return map[string]any{"channel": sanitizeNotificationChannel(*ch)}, nil
}

func (h *notificationEncryptedHandler) updateChannel(ctx context.Context, request EncryptedRequest) (any, error) {
	var payload notificationChannelPayload
	if err := decodeNotificationEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	id, err := parseNotificationChannelID(payload.ID)
	if err != nil {
		return nil, err
	}
	existing, err := h.repo.GetChannelByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get notification channel")
	}
	if existing == nil {
		return nil, fmt.Errorf("notification channel not found")
	}
	if strings.TrimSpace(payload.Name) != "" {
		existing.Name = strings.TrimSpace(payload.Name)
	}
	if strings.TrimSpace(payload.ChannelType) != "" {
		channelType, err := parseNotificationChannelType(payload.ChannelType)
		if err != nil {
			return nil, err
		}
		existing.ChannelType = channelType
	}
	if payload.Config != nil {
		nextConfig := cloneMap(payload.Config)
		// Webhook signing secrets are write-only in browser responses. If an
		// update omits the secret, keep the stored value instead of clearing it.
		if existing.ChannelType == domain.ChannelTypeWebhook {
			if _, ok := nextConfig["secret"]; !ok {
				if secret, ok := existing.Config["secret"]; ok {
					nextConfig["secret"] = secret
				}
			}
		}
		existing.Config = nextConfig
	}
	if payload.EventFilter != nil {
		existing.EventFilter = cloneMap(payload.EventFilter)
	}
	if payload.Enabled != nil {
		existing.Enabled = *payload.Enabled
	}
	if err := h.repo.UpdateChannel(ctx, existing); err != nil {
		return nil, fmt.Errorf("failed to update notification channel")
	}
	return map[string]any{"channel": sanitizeNotificationChannel(*existing)}, nil
}

func (h *notificationEncryptedHandler) deleteChannel(ctx context.Context, request EncryptedRequest) (any, error) {
	var payload notificationChannelPayload
	if err := decodeNotificationEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	id, err := parseNotificationChannelID(payload.ID)
	if err != nil {
		return nil, err
	}
	if err := h.repo.DeleteChannel(ctx, id); err != nil {
		return nil, fmt.Errorf("failed to delete notification channel")
	}
	return map[string]any{"status": "deleted", "id": id.String()}, nil
}

func (h *notificationEncryptedHandler) testChannel(ctx context.Context, request EncryptedRequest) (any, error) {
	if h.dispatcher == nil {
		return nil, fmt.Errorf("notification dispatcher is not configured")
	}
	var payload notificationChannelPayload
	if err := decodeNotificationEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	id, err := parseNotificationChannelID(payload.ID)
	if err != nil {
		return nil, err
	}
	ch, err := h.repo.GetChannelByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get notification channel")
	}
	if ch == nil {
		return nil, fmt.Errorf("notification channel not found")
	}
	if err := h.dispatcher.DispatchToChannel(ctx, ch, "test", map[string]any{
		"message":    "This is a test notification from Bahia",
		"channel_id": ch.ID.String(),
	}); err != nil {
		return nil, fmt.Errorf("failed to send test notification")
	}
	return map[string]any{"status": "test sent", "id": ch.ID.String()}, nil
}

func (h *notificationEncryptedHandler) listLogs(ctx context.Context, request EncryptedRequest) (any, error) {
	var payload notificationLogsPayload
	if err := decodeNotificationEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	limit := payload.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var logs []domain.NotificationLog
	var err error
	if strings.TrimSpace(payload.ChannelID) != "" {
		channelID, parseErr := parseNotificationChannelID(payload.ChannelID)
		if parseErr != nil {
			return nil, parseErr
		}
		logs, err = h.repo.ListLogsByChannel(ctx, channelID, limit)
	} else {
		logs, err = h.repo.ListRecentLogs(ctx, limit)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list notification logs")
	}
	return map[string]any{"logs": logs}, nil
}

func decodeNotificationEncryptedPayload(request EncryptedRequest, target any) error {
	if len(request.Envelope.Payload) == 0 || string(request.Envelope.Payload) == "null" {
		return nil
	}
	if err := json.Unmarshal(request.Envelope.Payload, target); err != nil {
		return fmt.Errorf("invalid notification payload")
	}
	return nil
}

func parseNotificationChannelID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid notification channel ID")
	}
	return id, nil
}

func parseNotificationChannelType(value string) (domain.ChannelType, error) {
	switch domain.ChannelType(strings.TrimSpace(value)) {
	case domain.ChannelTypeWebhook:
		return domain.ChannelTypeWebhook, nil
	case domain.ChannelTypeNostrDM:
		return domain.ChannelTypeNostrDM, nil
	default:
		return "", fmt.Errorf("channel_type must be 'webhook' or 'nostr_dm'")
	}
}

func sanitizeNotificationChannels(channels []domain.NotificationChannel) []domain.NotificationChannel {
	out := make([]domain.NotificationChannel, 0, len(channels))
	for _, ch := range channels {
		out = append(out, sanitizeNotificationChannel(ch))
	}
	return out
}

func sanitizeNotificationChannel(ch domain.NotificationChannel) domain.NotificationChannel {
	ch.Config = cloneMap(ch.Config)
	if ch.Config != nil {
		delete(ch.Config, "secret")
	}
	return ch
}

func cloneMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}
