package handlers

import (
	"context"
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
	GetChannelByIDForOrg(ctx context.Context, id, orgID uuid.UUID) (*domain.NotificationChannel, error)
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

func (h *NotificationHandler) tenantRepo(w http.ResponseWriter) (tenantNotificationRepository, bool) {
	repo, ok := h.repo.(tenantNotificationRepository)
	if !ok {
		writeError(w, http.StatusInternalServerError, "tenant notification repository is not configured")
	}
	return repo, ok
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
