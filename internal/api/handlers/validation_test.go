package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/dto"
)

// These tests verify the handler validation layer rejects bad input with 400
// without needing a real RegistryService (the request is rejected before reaching it).

func postJSON(t *testing.T, handler http.HandlerFunc, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func assertStatus(t *testing.T, w *httptest.ResponseRecorder, expected int) {
	t.Helper()
	if w.Code != expected {
		t.Errorf("expected status %d, got %d; body: %s", expected, w.Code, w.Body.String())
	}
}

func assertErrorContains(t *testing.T, w *httptest.ResponseRecorder, substring string) {
	t.Helper()
	var resp dto.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Error == "" {
		t.Fatal("expected an error in response, got none")
	}
	for i := 0; i <= len(resp.Error)-len(substring); i++ {
		if resp.Error[i:i+len(substring)] == substring {
			return
		}
	}
	t.Errorf("expected error containing %q, got %q", substring, resp.Error)
}
