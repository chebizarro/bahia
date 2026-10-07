package handlers

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
)

// SecretHandler provides HTTP handlers for service secret management.
type SecretHandler struct {
	repo      repository.SecretRepository
	encryptor *secrets.Encryptor
}

// NewSecretHandler creates a new SecretHandler.
func NewSecretHandler(repo repository.SecretRepository, encryptor *secrets.Encryptor) *SecretHandler {
	return &SecretHandler{repo: repo, encryptor: encryptor}
}

// requireEncryptor: deleted in N1 (not needed without Create/Update).

// createSecretRequest: deleted in N1 (not needed).

// secretRefResponse is the API response for a secret (never includes the value).
type secretRefResponse struct {
	ID               string  `json:"id"`
	ServiceID        string  `json:"service_id"`
	EnvironmentID    *string `json:"environment_id,omitempty"`
	Name             string  `json:"name"`
	EncryptionMethod string  `json:"encryption_method"`
	Version          int     `json:"version"`
	CreatedBy        string  `json:"created_by"`
	CreatedAt        string  `json:"created_at"`
	UpdatedAt        string  `json:"updated_at"`
}

func toSecretRefResponse(ref domain.SecretRef) secretRefResponse {
	resp := secretRefResponse{
		ID:               ref.ID.String(),
		ServiceID:        ref.ServiceID.String(),
		Name:             ref.Name,
		EncryptionMethod: string(ref.EncryptionMethod),
		Version:          ref.Version,
		CreatedBy:        ref.CreatedBy,
		CreatedAt:        ref.CreatedAt.String(),
		UpdatedAt:        ref.UpdatedAt.String(),
	}
	if ref.EnvironmentID != nil {
		s := ref.EnvironmentID.String()
		resp.EnvironmentID = &s
	}
	return resp
}

// Create: deleted in N1 — secret mutations go through intent publishing.

// List handles GET /services/{id}/secrets.
func (h *SecretHandler) List(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermReadSecrets) {
		return
	}
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

	secs, err := h.repo.ListByService(r.Context(), serviceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to list secrets")
		return
	}

	refs := make([]secretRefResponse, len(secs))
	for i, s := range secs {
		refs[i] = toSecretRefResponse(s.ToRef())
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": refs})
}

// Delete: deleted in N1 — secret mutations go through intent publishing.

// Update: deleted in N1 — secret mutations go through intent publishing.
