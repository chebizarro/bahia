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

func (h *SecretHandler) requireEncryptor(w http.ResponseWriter) bool {
	if h.encryptor != nil {
		return true
	}
	writeError(w, http.StatusServiceUnavailable, "secret encryption is not configured")
	return false
}

// createSecretRequest is the request body for creating a secret.
type createSecretRequest struct {
	Name             string `json:"name"`
	Value            string `json:"value"`
	EnvironmentID    string `json:"environment_id,omitempty"`
	EncryptionMethod string `json:"encryption_method,omitempty"` // "nip44" or "aes256gcm" (default: nip44)
}

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

// Create handles POST /services/{id}/secrets.
func (h *SecretHandler) Create(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteSecrets) {
		return
	}
	if !h.requireEncryptor(w) {
		return
	}
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}

	var req createSecretRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if req.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	createdBy, ok := authenticatedSubject(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "authenticated principal is required")
		return
	}

	method := domain.EncryptionNIP44
	if req.EncryptionMethod != "" {
		method = domain.EncryptionMethod(req.EncryptionMethod)
		if method != domain.EncryptionNIP44 && method != domain.EncryptionAES256 {
			writeError(w, http.StatusBadRequest, "encryption_method must be 'nip44' or 'aes256gcm'")
			return
		}
	}

	// Encrypt the value.
	encrypted, err := h.encryptor.Encrypt(req.Value, method)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt secret")
		return
	}

	secret := &domain.ServiceSecret{
		ID:               uuid.New(),
		ServiceID:        serviceID,
		Name:             req.Name,
		EncryptedValue:   encrypted,
		EncryptionMethod: method,
		Version:          1,
		CreatedBy:        createdBy,
	}

	if req.EnvironmentID != "" {
		envID, err := uuid.Parse(req.EnvironmentID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid environment_id")
			return
		}
		secret.EnvironmentID = &envID
	}

	if err := h.repo.Create(r.Context(), secret); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create secret")
		return
	}

	ref := secret.ToRef()
	writeJSON(w, http.StatusCreated, toSecretRefResponse(ref))
}

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

// Delete handles DELETE /services/{id}/secrets/{secretId}.
func (h *SecretHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteSecrets) {
		return
	}
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}
	secretID, err := uuid.Parse(chi.URLParam(r, "secretId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid secret ID")
		return
	}

	existing, err := h.repo.GetByID(r.Context(), secretID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up secret")
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	if existing.ServiceID != serviceID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	if err := h.repo.Delete(r.Context(), secretID); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to delete secret")
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}

// Update handles PUT /services/{id}/secrets/{secretId}.
func (h *SecretHandler) Update(w http.ResponseWriter, r *http.Request) {
	if !requirePermission(w, r, domain.PermWriteSecrets) {
		return
	}
	if !h.requireEncryptor(w) {
		return
	}
	serviceID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid service ID")
		return
	}
	secretID, err := uuid.Parse(chi.URLParam(r, "secretId"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid secret ID")
		return
	}

	existing, err := h.repo.GetByID(r.Context(), secretID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to look up secret")
		return
	}
	if existing == nil {
		writeError(w, http.StatusNotFound, "secret not found")
		return
	}
	if existing.ServiceID != serviceID {
		writeError(w, http.StatusForbidden, "access denied")
		return
	}

	var req createSecretRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Value == "" {
		writeError(w, http.StatusBadRequest, "value is required")
		return
	}

	method := existing.EncryptionMethod
	if req.EncryptionMethod != "" {
		method = domain.EncryptionMethod(req.EncryptionMethod)
	}

	encrypted, err := h.encryptor.Encrypt(req.Value, method)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to encrypt secret")
		return
	}

	existing.EncryptedValue = encrypted
	existing.EncryptionMethod = method

	if err := h.repo.Update(r.Context(), existing); err != nil {
		writeError(w, http.StatusInternalServerError, "failed to update secret")
		return
	}

	// Re-fetch to get updated version.
	updated, _ := h.repo.GetByID(r.Context(), secretID)
	if updated == nil {
		updated = existing
	}

	writeJSON(w, http.StatusOK, toSecretRefResponse(updated.ToRef()))
}
