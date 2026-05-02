package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
)

func TestSystemHandler_GetInfo_ExposesRelaySidecarCapabilities(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.PrivateRelays = []string{"wss://private.example"}
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.PublicURL = "ws://localhost:3000/relay"
	cfg.Nostr.BrowserRelays = []string{"ws://localhost:3000/relay"}
	cfg.Auth.NIP98Enabled = true

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	w := httptest.NewRecorder()

	NewSystemHandler(cfg).GetInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var payload struct {
		Data struct {
			Nostr struct {
				Relays        []string `json:"relays"`
				BrowserRelays []string `json:"browser_relays"`
				SidecarURL    string   `json:"sidecar_url"`
				PrivateRelays []string `json:"private_relays"`
			} `json:"nostr"`
			Features map[string]bool `json:"features"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !payload.Data.Features["relay_sidecar"] {
		t.Fatalf("expected relay_sidecar feature to be true")
	}
	if payload.Data.Features["relay_read_models"] {
		t.Fatalf("expected relay_read_models feature to remain false until projector work lands")
	}
	if !payload.Data.Features["direct_nostr_http_auth"] {
		t.Fatalf("expected direct_nostr_http_auth feature to be true")
	}
	if payload.Data.Nostr.SidecarURL != "ws://localhost:3000/relay" {
		t.Fatalf("sidecar_url = %q", payload.Data.Nostr.SidecarURL)
	}
	if len(payload.Data.Nostr.BrowserRelays) != 1 || payload.Data.Nostr.BrowserRelays[0] != "ws://localhost:3000/relay" {
		t.Fatalf("browser_relays = %#v", payload.Data.Nostr.BrowserRelays)
	}
	if len(payload.Data.Nostr.PrivateRelays) != 0 {
		t.Fatalf("private_relays should not be exposed in system info: %#v", payload.Data.Nostr.PrivateRelays)
	}
	if len(payload.Data.Nostr.Relays) != 0 {
		t.Fatalf("backend relays should not be exposed while sidecar bootstrap is enabled: %#v", payload.Data.Nostr.Relays)
	}
}

func TestSystemHandler_GetInfo_DoesNotAdvertiseSidecarWhenDisabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.Sidecar.Enabled = false
	cfg.Nostr.Sidecar.PublicURL = "ws://localhost:3000/relay"
	cfg.Nostr.BrowserRelays = []string{"ws://localhost:3000/relay"}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	w := httptest.NewRecorder()

	NewSystemHandler(cfg).GetInfo(w, req)

	var payload struct {
		Data struct {
			Nostr struct {
				BrowserRelays []string `json:"browser_relays"`
				SidecarURL    string   `json:"sidecar_url"`
			} `json:"nostr"`
			Features map[string]bool `json:"features"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.Features["relay_sidecar"] {
		t.Fatalf("expected relay_sidecar feature to be false")
	}
	if payload.Data.Nostr.SidecarURL != "" || len(payload.Data.Nostr.BrowserRelays) != 0 {
		t.Fatalf("disabled sidecar should not advertise relay bootstrap fields: %+v", payload.Data.Nostr)
	}
}

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
