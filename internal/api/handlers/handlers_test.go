package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/api/dto"
)

func TestWriteJSON(t *testing.T) {
	w := httptest.NewRecorder()
	writeJSON(w, http.StatusOK, dto.APIResponse{Message: "hello"})

	if w.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", w.Code)
	}

	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected content-type application/json, got %s", ct)
	}

	var resp dto.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Message != "hello" {
		t.Errorf("expected message hello, got %s", resp.Message)
	}
}

func TestWriteError(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusBadRequest, "bad input")

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", w.Code)
	}

	var resp dto.APIResponse
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if resp.Error != "bad input" {
		t.Errorf("expected error 'bad input', got '%s'", resp.Error)
	}
}

func TestDecodeJSON(t *testing.T) {
	body := `{"name": "test-service", "artifact_repo": "harbor/test"}`
	r := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))

	var req dto.CreateServiceRequest
	if err := decodeJSON(r, &req); err != nil {
		t.Fatalf("failed to decode: %v", err)
	}

	if req.Name != "test-service" {
		t.Errorf("expected name 'test-service', got '%s'", req.Name)
	}
	if req.ArtifactRepo != "harbor/test" {
		t.Errorf("expected artifact_repo 'harbor/test', got '%s'", req.ArtifactRepo)
	}
}

func TestQueryInt(t *testing.T) {
	tests := []struct {
		query    string
		name     string
		def      int
		expected int
	}{
		{"limit=10", "limit", 50, 10},
		{"", "limit", 50, 50},
		{"limit=abc", "limit", 50, 50},
	}

	for _, tc := range tests {
		r := httptest.NewRequest(http.MethodGet, "/?"+tc.query, nil)
		got := queryInt(r, tc.name, tc.def)
		if got != tc.expected {
			t.Errorf("queryInt(%q, %q, %d) = %d, want %d", tc.query, tc.name, tc.def, got, tc.expected)
		}
	}
}
