package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
)

func TestSystemHandler_GetInfo_ExposesNostrAuthExchangeFeature(t *testing.T) {
	cfg := &config.Config{}
	cfg.Auth.JWTSecret = "test-secret"

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	w := httptest.NewRecorder()

	NewSystemHandler(cfg).GetInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var payload struct {
		Data struct {
			Features map[string]bool `json:"features"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !payload.Data.Features["nostr_auth_exchange"] {
		t.Fatalf("expected nostr_auth_exchange feature to be true")
	}
}
