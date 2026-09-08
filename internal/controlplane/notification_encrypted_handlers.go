package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/auth"
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
	repo       tenantNotificationRepository
	dispatcher *notifications.Dispatcher
	authorizer encryptedTenantAuthorizer
}

type notificationChannelPayload struct {
	ID          string         `json:"id,omitempty"`
	OrgID       string         `json:"org_id,omitempty"`
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

type tenantNotificationRepository interface {
	repository.NotificationRepository
	GetChannelByIDForOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.NotificationChannel, error)
	ListChannelsByOrg(ctx context.Context, orgID uuid.UUID, enabledOnly bool) ([]domain.NotificationChannel, error)
	UpdateChannelForOrg(ctx context.Context, ch *domain.NotificationChannel, orgID uuid.UUID) error
	DeleteChannelForOrg(ctx context.Context, id, orgID uuid.UUID) error
	ListRecentLogsByOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]domain.NotificationLog, error)
}

// RegisterNotificationEncryptedHandlers wires notification CRUD/test/log queries
// onto the ContextVM encrypted control-plane runtime. Notification configs and
// delivery logs are never projected to the public sidecar; result payloads are
// returned through ContextVM responses.
func RegisterNotificationEncryptedHandlers(transport *EncryptedRequestTransport, repo tenantNotificationRepository, dispatcher *notifications.Dispatcher, rbac *auth.RBAC) {
	if transport == nil || repo == nil {
		return
	}
	h := &notificationEncryptedHandler{
		repo:       repo,
		dispatcher: dispatcher,
		authorizer: encryptedTenantAuthorizer{rbac: rbac},
	}
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

func (h *notificationEncryptedHandler) listChannels(ctx context.Context, request EncryptedRequest) (any, error) {
	var payload notificationChannelPayload
	if err := decodeNotificationEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	orgIDs, err := h.requesterOrgIDs(ctx, request)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(payload.OrgID) != "" {
		orgID, err := parseNotificationOrgID(payload.OrgID)
		if err != nil {
			return nil, err
		}
		if !containsNotificationOrgID(orgIDs, orgID) {
			return nil, &auth.AccessDeniedError{Reason: "not a member of this organization", OrgID: orgID}
		}
		orgIDs = []uuid.UUID{orgID}
	}

	channels := make([]domain.NotificationChannel, 0)
	for _, orgID := range orgIDs {
		if err := h.authorizer.authorizeOrg(ctx, request.Event, orgID, domain.PermReadServices); err != nil {
			return nil, err
		}
		orgChannels, err := h.repo.ListChannelsByOrg(ctx, orgID, false)
		if err != nil {
			return nil, fmt.Errorf("failed to list notification channels")
		}
		channels = append(channels, orgChannels...)
	}
	sort.SliceStable(channels, func(i, j int) bool { return channels[i].Name < channels[j].Name })
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
	ch, err := h.authorizedChannel(ctx, request, id, domain.PermReadServices)
	if err != nil {
		return nil, err
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
	orgID, err := h.resolveCreateOrg(ctx, request, payload.OrgID)
	if err != nil {
		return nil, err
	}
	ch := &domain.NotificationChannel{
		ID:          uuid.New(),
		OrgID:       orgID,
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
	existing, err := h.authorizedChannel(ctx, request, id, domain.PermManageSettings)
	if err != nil {
		return nil, err
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
	if err := h.repo.UpdateChannelForOrg(ctx, existing, existing.OrgID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("notification channel not found")
		}
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
	existing, err := h.authorizedChannel(ctx, request, id, domain.PermManageSettings)
	if err != nil {
		return nil, err
	}
	if err := h.repo.DeleteChannelForOrg(ctx, id, existing.OrgID); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, fmt.Errorf("notification channel not found")
		}
		return nil, fmt.Errorf("failed to delete notification channel")
	}
	return map[string]any{"status": "deleted", "id": id.String()}, nil
}

func (h *notificationEncryptedHandler) testChannel(ctx context.Context, request EncryptedRequest) (any, error) {
	var payload notificationChannelPayload
	if err := decodeNotificationEncryptedPayload(request, &payload); err != nil {
		return nil, err
	}
	id, err := parseNotificationChannelID(payload.ID)
	if err != nil {
		return nil, err
	}
	ch, err := h.authorizedChannel(ctx, request, id, domain.PermManageSettings)
	if err != nil {
		return nil, err
	}
	if h.dispatcher == nil {
		return nil, fmt.Errorf("notification dispatcher is not configured")
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
		ch, authErr := h.authorizedChannel(ctx, request, channelID, domain.PermReadLogs)
		if authErr != nil {
			return nil, authErr
		}
		logs, err = h.repo.ListLogsByChannel(ctx, ch.ID, limit)
	} else {
		orgIDs, authErr := h.requesterOrgIDs(ctx, request)
		if authErr != nil {
			return nil, authErr
		}
		for _, orgID := range orgIDs {
			if authErr := h.authorizer.authorizeOrg(ctx, request.Event, orgID, domain.PermReadLogs); authErr != nil {
				return nil, authErr
			}
			orgLogs, listErr := h.repo.ListRecentLogsByOrg(ctx, orgID, limit)
			if listErr != nil {
				err = listErr
				break
			}
			logs = append(logs, orgLogs...)
		}
		sort.SliceStable(logs, func(i, j int) bool { return logs[i].CreatedAt.After(logs[j].CreatedAt) })
		if len(logs) > limit {
			logs = logs[:limit]
		}
	}
	if err != nil {
		return nil, fmt.Errorf("failed to list notification logs")
	}
	return map[string]any{"logs": logs}, nil
}

func (h *notificationEncryptedHandler) requesterOrgIDs(ctx context.Context, request EncryptedRequest) ([]uuid.UUID, error) {
	memberships, err := h.authorizer.requesterOrgMemberships(ctx, request.Event)
	if err != nil {
		return nil, err
	}
	orgIDs := make([]uuid.UUID, 0, len(memberships))
	seen := make(map[uuid.UUID]struct{}, len(memberships))
	for _, membership := range memberships {
		if membership.OrgID == uuid.Nil {
			return nil, fmt.Errorf("requester organization membership has no organization")
		}
		if _, ok := seen[membership.OrgID]; ok {
			continue
		}
		seen[membership.OrgID] = struct{}{}
		orgIDs = append(orgIDs, membership.OrgID)
	}
	if len(orgIDs) == 0 {
		return nil, &auth.AccessDeniedError{Reason: "requester is not a member of any organization"}
	}
	return orgIDs, nil
}

func (h *notificationEncryptedHandler) resolveCreateOrg(ctx context.Context, request EncryptedRequest, requested string) (uuid.UUID, error) {
	orgIDs, err := h.requesterOrgIDs(ctx, request)
	if err != nil {
		return uuid.Nil, err
	}
	var orgID uuid.UUID
	if strings.TrimSpace(requested) == "" {
		if len(orgIDs) != 1 {
			return uuid.Nil, fmt.Errorf("org_id is required when requester belongs to multiple organizations")
		}
		orgID = orgIDs[0]
	} else {
		orgID, err = parseNotificationOrgID(requested)
		if err != nil {
			return uuid.Nil, err
		}
		if !containsNotificationOrgID(orgIDs, orgID) {
			return uuid.Nil, &auth.AccessDeniedError{Reason: "not a member of this organization", OrgID: orgID}
		}
	}
	if err := h.authorizer.authorizeOrg(ctx, request.Event, orgID, domain.PermManageSettings); err != nil {
		return uuid.Nil, err
	}
	return orgID, nil
}

func (h *notificationEncryptedHandler) authorizedChannel(ctx context.Context, request EncryptedRequest, id uuid.UUID, permission domain.Permission) (*domain.NotificationChannel, error) {
	existing, err := h.repo.GetChannelByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("failed to get notification channel")
	}
	if existing == nil {
		return nil, fmt.Errorf("notification channel not found")
	}
	if existing.OrgID == uuid.Nil {
		return nil, fmt.Errorf("notification channel is not assigned to an organization")
	}
	if err := h.authorizer.authorizeOrg(ctx, request.Event, existing.OrgID, permission); err != nil {
		return nil, err
	}
	ch, err := h.repo.GetChannelByIDForOrg(ctx, id, existing.OrgID)
	if err != nil {
		return nil, fmt.Errorf("failed to get notification channel")
	}
	if ch == nil {
		return nil, fmt.Errorf("notification channel not found")
	}
	return ch, nil
}

func containsNotificationOrgID(orgIDs []uuid.UUID, orgID uuid.UUID) bool {
	for _, candidate := range orgIDs {
		if candidate == orgID {
			return true
		}
	}
	return false
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

func parseNotificationOrgID(value string) (uuid.UUID, error) {
	id, err := uuid.Parse(strings.TrimSpace(value))
	if err != nil {
		return uuid.Nil, fmt.Errorf("invalid organization ID")
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
