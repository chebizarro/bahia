package handlers

import (
	"net/http/httptest"
	"testing"
)

// These tests verify the handler validation layer rejects bad input with 400
// without needing a real RegistryService (the request is rejected before reaching it).

func assertStatus(t *testing.T, w *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if w.Code != expected {
		t.Errorf("expected status %d, got %d; body: %s", expected, w.Code, w.Body.String())
	}
}
