package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/notifications"
	"github.com/openagentsinc/bahia/internal/repository"
)

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

// ListChannels handles GET /notifications/channels.
func (h *NotificationHandler) ListChannels(w http.ResponseWriter, r *http.Request) {
	channels, err := h.repo.ListChannels(r.Context(), false)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list channels")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": channels})
}

// GetChannel handles GET /notifications/channels/{id}.
func (h *NotificationHandler) GetChannel(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel ID")
		return
	}

	ch, err := h.repo.GetChannelByID(r.Context(), id)
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
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel ID")
		return
	}

	existing, err := h.repo.GetChannelByID(r.Context(), id)
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

	if err := h.repo.UpdateChannel(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update channel")
		return
	}

	writeJSON(w, http.StatusOK, existing)
}

// DeleteChannel handles DELETE /notifications/channels/{id}.
func (h *NotificationHandler) DeleteChannel(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel ID")
		return
	}

	if err := h.repo.DeleteChannel(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete channel")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// TestChannel handles POST /notifications/channels/{id}/test.
func (h *NotificationHandler) TestChannel(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid channel ID")
		return
	}

	ch, err := h.repo.GetChannelByID(r.Context(), id)
	if err != nil || ch == nil {
		writeError(w, http.StatusNotFound, "channel not found")
		return
	}

	// Send a test notification.
	h.dispatcher.Dispatch(r.Context(), "test", map[string]any{
		"message":    "This is a test notification from Bahia",
		"channel_id": ch.ID.String(),
	})

	writeJSON(w, http.StatusOK, map[string]string{"status": "test sent"})
}

// ListLogs handles GET /notifications/log.
func (h *NotificationHandler) ListLogs(w http.ResponseWriter, r *http.Request) {
	logs, err := h.repo.ListRecentLogs(r.Context(), 50)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list logs")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": logs})
}
