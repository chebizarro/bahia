package handlers

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

func TestSystemHandler_GetInfo_ExposesRelaySidecarCapabilities(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.EncryptedRelays = []string{"wss://encrypted.example"}
	cfg.Nostr.Sidecar.Enabled = true
	cfg.Nostr.Sidecar.PublicURL = "ws://localhost:3000/relay"
	cfg.Nostr.BrowserRelays = []string{"ws://localhost:3000/relay"}
	cfg.Auth.Enabled = true

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	w := httptest.NewRecorder()

	NewSystemHandler(cfg).GetInfo(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var payload struct {
		Data struct {
			Nostr struct {
				Relays                 []string `json:"relays"`
				BrowserRelays          []string `json:"browser_relays"`
				SidecarURL             string   `json:"sidecar_url"`
				PrivateRelays          []string `json:"private_relays"`
				EncryptedBrowserRelays []string `json:"encrypted_browser_relays"`
				PrivateBrowserRelays   []string `json:"private_browser_relays"`
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
	if !payload.Data.Features["relay_read_models"] {
		t.Fatalf("expected relay_read_models feature to be true when sidecar publishing is enabled")
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
	if len(payload.Data.Nostr.EncryptedBrowserRelays) != 0 || len(payload.Data.Nostr.PrivateBrowserRelays) != 0 {
		t.Fatalf("encrypted browser relays should only expose explicit encrypted browser relays: canonical=%#v deprecated=%#v", payload.Data.Nostr.EncryptedBrowserRelays, payload.Data.Nostr.PrivateBrowserRelays)
	}
	if len(payload.Data.Nostr.Relays) != 0 {
		t.Fatalf("backend relays should not be exposed while sidecar bootstrap is enabled: %#v", payload.Data.Nostr.Relays)
	}
}

func TestSystemHandler_GetInfo_ExposesExplicitEncryptedBrowserRelays(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"
	cfg.Nostr.EncryptedRelays = []string{"wss://backend-encrypted.example"}
	cfg.Nostr.EncryptedBrowserRelays = []string{"wss://browser-encrypted.example"}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	w := httptest.NewRecorder()

	NewSystemHandler(cfg).GetInfo(w, req)

	var payload struct {
		Data struct {
			Nostr struct {
				PrivateRelays          []string `json:"private_relays"`
				EncryptedBrowserRelays []string `json:"encrypted_browser_relays"`
				PrivateBrowserRelays   []string `json:"private_browser_relays"`
			} `json:"nostr"`
			Features map[string]bool `json:"features"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Data.Nostr.PrivateRelays) != 0 {
		t.Fatalf("backend encrypted relay aliases must not be exposed: %#v", payload.Data.Nostr.PrivateRelays)
	}
	if got := payload.Data.Nostr.EncryptedBrowserRelays; len(got) != 1 || got[0] != "wss://browser-encrypted.example" {
		t.Fatalf("encrypted_browser_relays = %#v", got)
	}
	if got := payload.Data.Nostr.PrivateBrowserRelays; len(got) != 1 || got[0] != "wss://browser-encrypted.example" {
		t.Fatalf("deprecated private_browser_relays alias = %#v", got)
	}
	if !payload.Data.Features["encrypted_nostr_requests"] {
		t.Fatalf("expected encrypted_nostr_requests feature when encrypted browser relays and service key are configured")
	}
	if !payload.Data.Features["private_nostr_transport"] {
		t.Fatalf("expected deprecated private_nostr_transport alias to mirror encrypted_nostr_requests")
	}
}

func TestSystemHandler_GetInfo_DoesNotAdvertiseEncryptedRequestsWithoutBackendEncryptedRelays(t *testing.T) {
	cfg := config.Defaults()
	cfg.Nostr.PrivateKey = "0000000000000000000000000000000000000000000000000000000000000001"
	cfg.Nostr.EncryptedBrowserRelays = []string{"wss://browser-encrypted.example"}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	w := httptest.NewRecorder()

	NewSystemHandler(cfg).GetInfo(w, req)

	var payload struct {
		Data struct {
			Features map[string]bool `json:"features"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.Data.Features["encrypted_nostr_requests"] {
		t.Fatalf("encrypted_nostr_requests must stay false until backend encrypted_relays are configured")
	}
	if payload.Data.Features["private_nostr_transport"] {
		t.Fatalf("deprecated private_nostr_transport alias must stay false until backend encrypted_relays are configured")
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

func TestSystemHandler_GetInfo_ExposesMCPTransportOnlyWhenEnabled(t *testing.T) {
	cfg := config.Defaults()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	w := httptest.NewRecorder()

	NewSystemHandler(cfg, WithSystemMCPTransport(true)).GetInfo(w, req)

	var payload struct {
		Data struct {
			Features     map[string]bool `json:"features"`
			ControlPlane struct {
				MCP struct {
					AsyncCorrelation bool `json:"async_correlation"`
				} `json:"mcp"`
			} `json:"control_plane"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !payload.Data.Features["mcp_transport"] {
		t.Fatalf("expected mcp_transport feature when handler is wired")
	}
	if !payload.Data.ControlPlane.MCP.AsyncCorrelation {
		t.Fatalf("expected MCP async correlation guidance when MCP transport is wired")
	}
}

func TestSystemHandler_GetInfo_DoesNotAdvertiseDisabledControlPlaneCapabilities(t *testing.T) {
	cfg := config.Defaults()
	cfg.LLM.Enabled = false

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	w := httptest.NewRecorder()

	NewSystemHandler(cfg).GetInfo(w, req)

	var payload struct {
		Data struct {
			Features     map[string]bool `json:"features"`
			ControlPlane struct {
				Capabilities   []string       `json:"capabilities"`
				RequestKinds   map[string]int `json:"request_kinds"`
				StatusKinds    map[string]int `json:"status_kinds"`
				ResultKinds    map[string]int `json:"result_kinds"`
				ReadModelKinds map[string]int `json:"read_model_kinds"`
			} `json:"control_plane"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if payload.Data.Features["llm_control_plane"] {
		t.Fatalf("expected llm_control_plane feature to be false when LLM is disabled")
	}
	if containsString(payload.Data.ControlPlane.Capabilities, "llm_routes") || containsString(payload.Data.ControlPlane.Capabilities, "mcp_async_correlation") {
		t.Fatalf("unexpected disabled capabilities: %#v", payload.Data.ControlPlane.Capabilities)
	}
	if _, ok := payload.Data.ControlPlane.RequestKinds["llm_deploy_request"]; ok {
		t.Fatalf("disabled LLM request kind was advertised")
	}
	if _, ok := payload.Data.ControlPlane.StatusKinds["llm_deployment_status"]; ok {
		t.Fatalf("disabled LLM status kind was advertised")
	}
	if _, ok := payload.Data.ControlPlane.ResultKinds["llm_deployment_result"]; ok {
		t.Fatalf("disabled LLM result kind was advertised")
	}
	if _, ok := payload.Data.ControlPlane.ReadModelKinds["llm_route_state"]; ok {
		t.Fatalf("disabled LLM read-model kind was advertised")
	}
}

func TestSystemHandler_GetInfo_AdvertisesControlPlaneKinds(t *testing.T) {
	cfg := config.Defaults()
	cfg.LLM.Enabled = true

	req := httptest.NewRequest(http.MethodGet, "/api/v1/system/info", nil)
	w := httptest.NewRecorder()

	NewSystemHandler(cfg, WithSystemMCPTransport(true)).GetInfo(w, req)

	var payload struct {
		Data struct {
			Features     map[string]bool `json:"features"`
			ControlPlane struct {
				Version         string         `json:"version"`
				Capabilities    []string       `json:"capabilities"`
				RequestKinds    map[string]int `json:"request_kinds"`
				StatusKinds     map[string]int `json:"status_kinds"`
				ResultKinds     map[string]int `json:"result_kinds"`
				ReadModelKinds  map[string]int `json:"read_model_kinds"`
				CorrelationTags []string       `json:"correlation_tags"`
			} `json:"control_plane"`
		} `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if !payload.Data.Features["llm_control_plane"] {
		t.Fatalf("expected llm_control_plane feature when LLM is enabled")
	}
	if payload.Data.ControlPlane.Version != "bahia-controlplane-v1" {
		t.Fatalf("unexpected control plane version %q", payload.Data.ControlPlane.Version)
	}
	if !containsString(payload.Data.ControlPlane.Capabilities, "llm_routes") || !containsString(payload.Data.ControlPlane.Capabilities, "mcp_async_correlation") {
		t.Fatalf("missing enabled capabilities: %#v", payload.Data.ControlPlane.Capabilities)
	}
	assertKind := func(group string, got map[string]int, key string, want int) {
		t.Helper()
		if got[key] != want {
			t.Fatalf("%s[%s] = %d, want %d", group, key, got[key], want)
		}
	}
	assertKind("request_kinds", payload.Data.ControlPlane.RequestKinds, "llm_route_create", controlplane.KindLLMRouteCreate)
	assertKind("request_kinds", payload.Data.ControlPlane.RequestKinds, "llm_release_register", controlplane.KindLLMReleaseRegister)
	assertKind("request_kinds", payload.Data.ControlPlane.RequestKinds, "llm_deploy_request", controlplane.KindLLMDeployRequest)
	assertKind("request_kinds", payload.Data.ControlPlane.RequestKinds, "llm_deployment_approval", controlplane.KindLLMDeploymentApproval)
	assertKind("request_kinds", payload.Data.ControlPlane.RequestKinds, "llm_rollback_request", controlplane.KindLLMRollbackRequest)
	assertKind("status_kinds", payload.Data.ControlPlane.StatusKinds, "llm_deployment_status", controlplane.KindLLMDeploymentStatus)
	assertKind("result_kinds", payload.Data.ControlPlane.ResultKinds, "llm_route_create_result", controlplane.KindLLMRouteCreateResult)
	assertKind("result_kinds", payload.Data.ControlPlane.ResultKinds, "llm_release_register_result", controlplane.KindLLMReleaseRegisterResult)
	assertKind("result_kinds", payload.Data.ControlPlane.ResultKinds, "llm_deployment_result", controlplane.KindLLMDeploymentResult)
	assertKind("read_model_kinds", payload.Data.ControlPlane.ReadModelKinds, "llm_route_registry", controlplane.KindLLMRouteRegistry)
	assertKind("read_model_kinds", payload.Data.ControlPlane.ReadModelKinds, "llm_route_state", controlplane.KindLLMRouteState)
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func TestSystemHandler_GetInfo_AdvertisesRemovedLegacyFeaturesAsDisabled(t *testing.T) {
	cfg := config.Defaults()
	cfg.Auth.Enabled = true

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

	for _, feature := range []string{"nostr_auth_exchange", "legacy_sse", "legacy_jwt_exchange", "legacy_agent_http"} {
		if payload.Data.Features[feature] {
			t.Fatalf("expected %s feature to be false after cutover", feature)
		}
	}
}
