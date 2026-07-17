package handlers

import (
	"context"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/repository"
)

// tenantNotificationRepository provides the org-qualified operations required by
// the authenticated HTTP API. The base repository methods remain available to
// the system dispatcher, which processes events across all organizations.
type tenantNotificationRepository interface {
	repository.NotificationRepository
	GetChannelByIDForOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.NotificationChannel, error)
	ListChannelsByOrg(ctx context.Context, orgID uuid.UUID, enabledOnly bool) ([]domain.NotificationChannel, error)
	UpdateChannelForOrg(ctx context.Context, ch *domain.NotificationChannel, orgID uuid.UUID) error
	DeleteChannelForOrg(ctx context.Context, id, orgID uuid.UUID) error
	ListRecentLogsByOrg(ctx context.Context, orgID uuid.UUID, limit int) ([]domain.NotificationLog, error)
}

// NotificationHandler provides HTTP handlers for notification management.
type NotificationHandler struct {
	repo       repository.NotificationRepository
	dispatcher *notifications.Dispatcher
}

// NewNotificationHandler creates a new NotificationHandler.
func NewNotificationHandler(repo repository.NotificationRepository, dispatcher *notifications.Dispatcher) *NotificationHandler {
	return &NotificationHandler{repo: repo, dispatcher: dispatcher}
}

type createChannelRequest struct {
	Name        string         `json:"name"`
	ChannelType string         `json:"channel_type"`
	Config      map[string]any `json:"config"`
	EventFilter map[string]any `json:"event_filter,omitempty"`
	Enabled     *bool          `json:"enabled,omitempty"`
}

func (h *NotificationHandler) tenantRepo(w http.ResponseWriter) (tenantNotificationRepository, bool) {
	repo, ok := h.repo.(tenantNotificationRepository)
	if !ok {
		writeError(w, http.StatusInternalServerError, "tenant notification repository is not configured")
	}
	return repo, ok
}

// ListChannels handles GET /notifications/channels.
func (h *NotificationHandler) ListChannels(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	orgID := authzOrgID(r)
	var channels []domain.NotificationChannel
	var err error
	if orgID == uuid.Nil {
		channels, err = h.repo.ListChannels(r.Context(), false)
	} else if repo, ok := h.tenantRepo(w); ok {
		channels, err = repo.ListChannelsByOrg(r.Context(), orgID, false)
	} else {
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list channels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": channels})
}

// GetChannel handles GET /notifications/channels/{id}.
func (h *NotificationHandler) GetChannel(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel ID")
		return
	}

	var ch *domain.NotificationChannel
	if orgID := authzOrgID(r); orgID == uuid.Nil {
		ch, err = h.repo.GetChannelByID(r.Context(), id)
	} else if repo, ok := h.tenantRepo(w); ok {
		ch, err = repo.GetChannelByIDForOrg(r.Context(), id, orgID)
	} else {
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get channel")
		return
	}
	if ch == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}
	writeJSON(w, http.StatusOK, ch)
}

// CreateChannel handles POST /notifications/channels.
func (h *NotificationHandler) CreateChannel(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	var req createChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	ct := domain.ChannelType(req.ChannelType)
	if ct != domain.ChannelTypeWebhook && ct != domain.ChannelTypeNostrDM {
		writeError(w, http.StatusBadRequest, "channel_type must be 'webhook' or 'nostr_dm'")
		return
	}

	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}

	ch := &domain.NotificationChannel{
		ID:          uuid.New(),
		OrgID:       authzOrgID(r),
		Name:        req.Name,
		ChannelType: ct,
		Config:      req.Config,
		EventFilter: req.EventFilter,
		Enabled:     enabled,
	}

	if err := h.repo.CreateChannel(r.Context(), ch); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create channel")
		return
	}

	writeJSON(w, http.StatusCreated, ch)
}

// UpdateChannel handles PUT /notifications/channels/{id}.
func (h *NotificationHandler) UpdateChannel(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel ID")
		return
	}

	orgID := authzOrgID(r)
	var existing *domain.NotificationChannel
	var tenantRepo tenantNotificationRepository
	if orgID == uuid.Nil {
		existing, err = h.repo.GetChannelByID(r.Context(), id)
	} else if repo, ok := h.tenantRepo(w); ok {
		tenantRepo = repo
		existing, err = repo.GetChannelByIDForOrg(r.Context(), id, orgID)
	} else {
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to get channel")
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}

	var req createChannelRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name != "" {
		existing.Name = req.Name
	}
	if req.ChannelType != "" {
		existing.ChannelType = domain.ChannelType(req.ChannelType)
	}
	if req.Config != nil {
		existing.Config = req.Config
	}
	if req.EventFilter != nil {
		existing.EventFilter = req.EventFilter
	}
	if req.Enabled != nil {
		existing.Enabled = *req.Enabled
	}

	if tenantRepo != nil {
		err = tenantRepo.UpdateChannelForOrg(r.Context(), existing, orgID)
	} else {
		err = h.repo.UpdateChannel(r.Context(), existing)
	}
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to update channel")
		return
	}

	writeJSON(w, http.StatusOK, existing)
}

// DeleteChannel handles DELETE /notifications/channels/{id}.
func (h *NotificationHandler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel ID")
		return
	}

	if orgID := authzOrgID(r); orgID == uuid.Nil {
		err = h.repo.DeleteChannel(r.Context(), id)
	} else if repo, ok := h.tenantRepo(w); ok {
		err = repo.DeleteChannelForOrg(r.Context(), id, orgID)
	} else {
		return
	}
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			writeError(w, http.StatusNotFound, "channel not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "failed to delete channel")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// TestChannel handles POST /notifications/channels/{id}/test.
func (h *NotificationHandler) TestChannel(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel ID")
		return
	}

	var ch *domain.NotificationChannel
	if orgID := authzOrgID(r); orgID == uuid.Nil {
		ch, err = h.repo.GetChannelByID(r.Context(), id)
	} else if repo, ok := h.tenantRepo(w); ok {
		ch, err = repo.GetChannelByIDForOrg(r.Context(), id, orgID)
	} else {
		return
	}
	if err != nil || ch == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}

	if err := h.dispatcher.DispatchToChannel(r.Context(), ch, "test", map[string]any{
		"message":    "This is a test notification from Bahia",
		"channel_id": ch.ID.String(),
	}); err != nil {
		writeError(w, http.StatusBadGateway, "failed to send test notification")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "test sent"})
}

// ListLogs handles GET /notifications/log.
func (h *NotificationHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	if !requireMember(w, r) {
		return
	}
	orgID := authzOrgID(r)
	var logs []domain.NotificationLog
	var err error
	if orgID == uuid.Nil {
		logs, err = h.repo.ListRecentLogs(r.Context(), 50)
	} else if repo, ok := h.tenantRepo(w); ok {
		logs, err = repo.ListRecentLogsByOrg(r.Context(), orgID, 50)
	} else {
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list logs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": logs})
}
