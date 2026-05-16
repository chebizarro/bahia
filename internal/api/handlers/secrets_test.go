package handlers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
