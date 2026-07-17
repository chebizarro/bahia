package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/secrets"
	"github.com/openagentsinc/bahia/internal/auth"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestSecretHandlerCreateAttributesAuthenticatedPrincipal(t *testing.T) {
	repo := &recordingSecretRepo{}
	encryptor, err := secrets.NewEncryptor("1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("NewEncryptor() error = %v", err)
	}
	h := NewSecretHandler(repo, encryptor)
	serviceID := uuid.New()
	req := secretCreateRequest(t, serviceID, `{"name":"DATABASE_URL","value":"postgres://secret","encryption_method":"aes256gcm"}`)
	req = req.WithContext(auth.ContextWithPrincipal(req.Context(), &auth.Principal{
		Subject: "npub1creator",
		Method:  auth.MethodNIP98,
	}))
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("Create status = %d, want %d; body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if repo.created == nil {
		t.Fatal("repo.Create was not called")
	}
	if repo.created.CreatedBy != "npub1creator" {
		t.Fatalf("CreatedBy = %q, want authenticated principal subject", repo.created.CreatedBy)
	}
	var resp secretRefResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response JSON decode error = %v; body=%s", err, rec.Body.String())
	}
	if resp.CreatedBy != "npub1creator" {
		t.Fatalf("response created_by = %q, want authenticated principal subject", resp.CreatedBy)
	}
}

func TestSecretHandlerCreateFailsClosedWithoutAuthenticatedPrincipal(t *testing.T) {
	repo := &recordingSecretRepo{}
	encryptor, err := secrets.NewEncryptor("1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatalf("NewEncryptor() error = %v", err)
	}
	h := NewSecretHandler(repo, encryptor)
	req := secretCreateRequest(t, uuid.New(), `{"name":"DATABASE_URL","value":"postgres://secret","encryption_method":"aes256gcm"}`)
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("Create status = %d, want %d; body=%s", rec.Code, http.StatusUnauthorized, rec.Body.String())
	}
	if repo.created != nil {
		t.Fatalf("repo.Create was called with CreatedBy=%q; want no persistence without principal", repo.created.CreatedBy)
	}
}

func TestSecretHandlerCreateFailsClosedWithoutEncryptor(t *testing.T) {
	h := NewSecretHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/services/not-used/secrets", strings.NewReader(`{"name":"DATABASE_URL","value":"postgres://secret"}`))
	rec := httptest.NewRecorder()

	h.Create(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Create status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "secret encryption is not configured") {
		t.Fatalf("Create body = %q, want explicit encryption configuration error", rec.Body.String())
	}
}

func TestSecretHandlerUpdateFailsClosedWithoutEncryptor(t *testing.T) {
	h := NewSecretHandler(nil, nil)
	req := httptest.NewRequest(http.MethodPut, "/services/not-used/secrets/not-used", strings.NewReader(`{"value":"postgres://secret"}`))
	rec := httptest.NewRecorder()

	h.Update(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("Update status = %d, want %d; body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "secret encryption is not configured") {
		t.Fatalf("Update body = %q, want explicit encryption configuration error", rec.Body.String())
	}
}

func secretCreateRequest(t *testing.T, serviceID uuid.UUID, body string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/services/"+serviceID.String()+"/secrets", strings.NewReader(body))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", serviceID.String())
	return req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
}

type recordingSecretRepo struct {
	created *domain.ServiceSecret
}

func (r *recordingSecretRepo) Create(ctx context.Context, s *domain.ServiceSecret) error {
	copy := *s
	r.created = &copy
	return nil
}

func (r *recordingSecretRepo) GetByID(ctx context.Context, id uuid.UUID) (*domain.ServiceSecret, error) {
	return nil, nil
}

func (r *recordingSecretRepo) GetCurrentVersion(ctx context.Context, secretID uuid.UUID) (*domain.SecretVersion, error) {
	return nil, nil
}

func (r *recordingSecretRepo) ListByService(ctx context.Context, serviceID uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}

func (r *recordingSecretRepo) ListByServiceAndEnv(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}

func (r *recordingSecretRepo) ListEffective(ctx context.Context, serviceID, envID uuid.UUID) ([]domain.ServiceSecret, error) {
	return nil, nil
}

func (r *recordingSecretRepo) Update(ctx context.Context, s *domain.ServiceSecret) error {
	return nil
}

func (r *recordingSecretRepo) RecordSecretAccessAudit(ctx context.Context, audit *domain.SecretAccessAudit) error {
	return nil
}

func (r *recordingSecretRepo) Delete(ctx context.Context, id uuid.UUID) error {
	return nil
}

func (r *recordingSecretRepo) DeleteByName(ctx context.Context, serviceID uuid.UUID, envID *uuid.UUID, name string) error {
	return nil
}
