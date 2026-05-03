// Package handlers provides HTTP handlers for the Bahia API.
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

// SystemHandler handles system info endpoints.
type SystemHandler struct {
	cfg                 *config.Config
	mcpTransportEnabled bool
}

type SystemOption func(*SystemHandler)

func WithSystemMCPTransport(enabled bool) SystemOption {
	return func(h *SystemHandler) {
		h.mcpTransportEnabled = enabled
	}
}

// NewSystemHandler creates a new SystemHandler.
func NewSystemHandler(cfg *config.Config, opts ...SystemOption) *SystemHandler {
	h := &SystemHandler{cfg: cfg}
	for _, opt := range opts {
		opt(h)
	}
	return h
}

// RegistryInfo describes an available artifact registry.
type RegistryInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	BaseURL string `json:"base_url"`
	Type    string `json:"type"` // native, harbor, ghcr, dockerhub, quay, custom
	Default bool   `json:"default,omitempty"`
	Enabled bool   `json:"enabled"`
}

// NostrConfig describes the Nostr configuration.
type NostrConfigInfo struct {
	Relays                        []string `json:"relays"`
	BrowserRelays                 []string `json:"browser_relays,omitempty"`
	BrowserEncryptedRequestRelays []string `json:"browser_encrypted_request_relays,omitempty"`
	SidecarURL                    string   `json:"sidecar_url,omitempty"`
	PublishEnabled                bool     `json:"publish_enabled"`
	ServicePubkey                 string   `json:"service_pubkey,omitempty"`
	ServiceNpub                   string   `json:"service_npub,omitempty"`
}

// ControlPlaneInfo advertises the canonical Nostr control-plane contract.
type ControlPlaneInfo struct {
	Version         string         `json:"version"`
	Capabilities    []string       `json:"capabilities"`
	RequestKinds    map[string]int `json:"request_kinds"`
	StatusKinds     map[string]int `json:"status_kinds"`
	ResultKinds     map[string]int `json:"result_kinds"`
	ReadModelKinds  map[string]int `json:"read_model_kinds"`
	CorrelationTags []string       `json:"correlation_tags"`
	MCP             MCPInfo        `json:"mcp"`
}

// MCPInfo describes MCP correlation guidance for async control-plane tools.
type MCPInfo struct {
	AsyncCorrelation bool     `json:"async_correlation"`
	Fields           []string `json:"fields"`
}

// BlossomConfigInfo describes Blossom storage configuration.
type BlossomConfigInfo struct {
	Enabled      bool     `json:"enabled"`
	URL          string   `json:"url,omitempty"`
	Servers      []string `json:"servers,omitempty"`
	StorageClass string   `json:"storage_class,omitempty"`
}

// RuntimeConfigInfo describes runtime configuration.
type RuntimeConfigInfo struct {
	Type         string   `json:"type"`
	Environments []string `json:"environments,omitempty"`
}

// OCIConfigInfo describes OCI registry configuration.
type OCIConfigInfo struct {
	Enabled    bool   `json:"enabled"`
	PublicHost string `json:"public_host,omitempty"`
}

// SystemInfoResponse is the response for GET /api/v1/system/info.
type SystemInfoResponse struct {
	Registries   []RegistryInfo    `json:"registries"`
	Nostr        NostrConfigInfo   `json:"nostr"`
	ControlPlane ControlPlaneInfo  `json:"control_plane"`
	Blossom      BlossomConfigInfo `json:"blossom"`
	Runtime      RuntimeConfigInfo `json:"runtime"`
	OCI          OCIConfigInfo     `json:"oci"`
	Features     map[string]bool   `json:"features"`
}

// GetInfo returns system information including available registries.
// GET /api/v1/system/info
func (h *SystemHandler) GetInfo(w http.ResponseWriter, r *http.Request) {
	registries := []RegistryInfo{}

	// Add Bahia's native OCI registry if enabled
	if h.cfg.OCI.Enabled && h.cfg.OCI.PublicHost != "" {
		registries = append(registries, RegistryInfo{
			ID:      "bahia-oci",
			Name:    "Bahia Registry",
			BaseURL: h.cfg.OCI.PublicHost,
			Type:    "native",
			Default: true,
			Enabled: true,
		})
	}

	// Add Harbor if enabled
	if h.cfg.Harbor.Enabled && h.cfg.Harbor.URL != "" {
		registries = append(registries, RegistryInfo{
			ID:      "harbor",
			Name:    "Harbor",
			BaseURL: h.cfg.Harbor.URL,
			Type:    "harbor",
			Enabled: true,
		})
	}

	// Add configured registry adapter
	if h.cfg.Registry.URL != "" {
		registries = append(registries, RegistryInfo{
			ID:      "configured",
			Name:    "Configured Registry",
			BaseURL: h.cfg.Registry.URL,
			Type:    h.cfg.Registry.Type,
			Enabled: true,
		})
	}

	// Add common public registries
	publicRegistries := []RegistryInfo{
		{
			ID:      "ghcr",
			Name:    "GitHub Container Registry",
			BaseURL: "ghcr.io",
			Type:    "ghcr",
			Enabled: true,
		},
		{
			ID:      "dockerhub",
			Name:    "Docker Hub",
			BaseURL: "docker.io",
			Type:    "dockerhub",
			Enabled: true,
		},
		{
			ID:      "quay",
			Name:    "Quay.io",
			BaseURL: "quay.io",
			Type:    "quay",
			Enabled: true,
		},
	}
	registries = append(registries, publicRegistries...)

	// Build Nostr config info
	nostrInfo := NostrConfigInfo{
		PublishEnabled: h.cfg.Nostr.PublishEnabled,
	}
	if !h.cfg.Nostr.Sidecar.Enabled {
		nostrInfo.Relays = h.cfg.Nostr.Relays
	}
	if h.cfg.Nostr.Sidecar.Enabled {
		nostrInfo.BrowserRelays = h.browserRelays()
		nostrInfo.SidecarURL = h.cfg.Nostr.Sidecar.PublicURL
	}
	browserEncryptedRequestRelays := h.browserEncryptedRequestRelays()
	nostrInfo.BrowserEncryptedRequestRelays = browserEncryptedRequestRelays

	// Derive service pubkey from private key if available
	if h.cfg.Nostr.PrivateKey != "" {
		// Import go-nostr to derive pubkey
		if pk, err := nip19.EncodePublicKey(derivePublicKey(h.cfg.Nostr.PrivateKey)); err == nil {
			nostrInfo.ServiceNpub = pk
		}
		nostrInfo.ServicePubkey = derivePublicKey(h.cfg.Nostr.PrivateKey)
	}

	// Build Blossom config info
	blossomInfo := BlossomConfigInfo{
		Enabled:      h.cfg.Blossom.Enabled,
		URL:          h.cfg.Blossom.URL,
		Servers:      h.cfg.Blossom.Servers,
		StorageClass: h.cfg.Blossom.StorageClass,
	}

	// Build Runtime config info
	envNames := make([]string, 0, len(h.cfg.Runtime.Environments))
	for name := range h.cfg.Runtime.Environments {
		envNames = append(envNames, name)
	}
	runtimeInfo := RuntimeConfigInfo{
		Type:         h.cfg.Runtime.Type,
		Environments: envNames,
	}

	// Build OCI config info
	ociInfo := OCIConfigInfo{
		Enabled:    h.cfg.OCI.Enabled,
		PublicHost: h.cfg.OCI.PublicHost,
	}

	encryptedNostrRequestsEnabled := len(browserEncryptedRequestRelays) > 0 && len(h.encryptedRequestRelays()) > 0 && h.cfg.Nostr.PrivateKey != ""

	features := map[string]bool{
		"oci":                      h.cfg.OCI.Enabled,
		"harbor":                   h.cfg.Harbor.Enabled,
		"blossom":                  h.cfg.Blossom.Enabled,
		"hiveci":                   h.cfg.HiveCI.Enabled,
		"cashu":                    h.cfg.Cashu.Enabled,
		"telemetry":                h.cfg.Telemetry.Enabled,
		"notifications":            h.cfg.Notifications.Enabled,
		"auth":                     h.cfg.Auth.Enabled,
		"nostr_auth_exchange":      false,
		"relay_sidecar":            h.cfg.Nostr.Sidecar.Enabled,
		"relay_read_models":        h.cfg.Nostr.Sidecar.Enabled && h.cfg.Nostr.PublishEnabled,
		"encrypted_nostr_requests": encryptedNostrRequestsEnabled,
		"llm_control_plane":        h.cfg.LLM.Enabled,
		"direct_nostr_http_auth":   h.cfg.Auth.Enabled,
		"mcp_transport":            h.mcpTransportEnabled,
		"legacy_sse":               false,
		"legacy_jwt_exchange":      false,
		"legacy_agent_http":        false,
	}

	resp := SystemInfoResponse{
		Registries:   registries,
		Nostr:        nostrInfo,
		ControlPlane: controlPlaneInfo(h.cfg.LLM.Enabled, h.mcpTransportEnabled),
		Blossom:      blossomInfo,
		Runtime:      runtimeInfo,
		OCI:          ociInfo,
		Features:     features,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": resp})
}

func controlPlaneInfo(llmEnabled, mcpTransportEnabled bool) ControlPlaneInfo {
	capabilities := []string{"service_deployments", "service_registry_read_models", "relay_read_models"}
	requestKinds := map[string]int{
		"deploy_request":      controlplane.KindDeployRequest,
		"rollback_request":    controlplane.KindRollbackRequest,
		"service_action":      controlplane.KindServiceAction,
		"service_create":      controlplane.KindServiceCreate,
		"environment_create":  controlplane.KindEnvironmentCreate,
		"deployment_approval": controlplane.KindDeploymentApproval,
		"observation_submit":  controlplane.KindObservationSubmit,
		"drift_remediate":     controlplane.KindDriftRemediate,
	}
	statusKinds := map[string]int{
		"deployment_status": controlplane.KindDeploymentStatus,
		"service_status":    controlplane.KindServiceStatus,
	}
	resultKinds := map[string]int{
		"deployment_result":         controlplane.KindDeploymentResult,
		"action_result":             controlplane.KindActionResult,
		"service_create_result":     controlplane.KindServiceCreateResult,
		"environment_create_result": controlplane.KindEnvCreateResult,
		"observation_result":        controlplane.KindObservationResult,
		"remediation_result":        controlplane.KindRemediationResult,
	}
	readModelKinds := map[string]int{
		"service_state":        controlplane.KindServiceState,
		"service_registry":     controlplane.KindServiceRegistry,
		"environment_registry": controlplane.KindEnvironmentRegistry,
	}
	correlationTags := []string{"service", "environment", "artifact", "intent", "run", "e", "p", "status", "step"}
	mcpFields := []string{"request_event_id", "request_kind", "status_kind", "result_kind", "registry_kind", "state_kind", "service_id", "environment_id", "intent_id", "run_id"}

	if llmEnabled {
		capabilities = append(capabilities, "llm_routes", "llm_deployments", "llm_rollback")
		requestKinds["llm_route_create"] = controlplane.KindLLMRouteCreate
		requestKinds["llm_release_register"] = controlplane.KindLLMReleaseRegister
		requestKinds["llm_deploy_request"] = controlplane.KindLLMDeployRequest
		requestKinds["llm_deployment_approval"] = controlplane.KindLLMDeploymentApproval
		requestKinds["llm_rollback_request"] = controlplane.KindLLMRollbackRequest
		statusKinds["llm_deployment_status"] = controlplane.KindLLMDeploymentStatus
		resultKinds["llm_route_create_result"] = controlplane.KindLLMRouteCreateResult
		resultKinds["llm_release_register_result"] = controlplane.KindLLMReleaseRegisterResult
		resultKinds["llm_deployment_result"] = controlplane.KindLLMDeploymentResult
		readModelKinds["llm_route_registry"] = controlplane.KindLLMRouteRegistry
		readModelKinds["llm_route_state"] = controlplane.KindLLMRouteState
		correlationTags = append(correlationTags, "route", "release")
		mcpFields = append(mcpFields, "route_id", "release_id")
	}
	if mcpTransportEnabled {
		capabilities = append(capabilities, "mcp_async_correlation")
	}

	return ControlPlaneInfo{
		Version:         "bahia-controlplane-v1",
		Capabilities:    capabilities,
		RequestKinds:    requestKinds,
		StatusKinds:     statusKinds,
		ResultKinds:     resultKinds,
		ReadModelKinds:  readModelKinds,
		CorrelationTags: correlationTags,
		MCP: MCPInfo{
			AsyncCorrelation: mcpTransportEnabled,
			Fields:           mcpFields,
		},
	}
}

// derivePublicKey derives a public key hex from a private key (hex or nsec).
func (h *SystemHandler) browserRelays() []string {
	if len(h.cfg.Nostr.BrowserRelays) > 0 {
		return h.cfg.Nostr.BrowserRelays
	}
	if h.cfg.Nostr.Sidecar.PublicURL != "" {
		return []string{h.cfg.Nostr.Sidecar.PublicURL}
	}
	return nil
}

func (h *SystemHandler) encryptedRequestRelays() []string {
	if len(h.cfg.Nostr.EncryptedRequestRelays) > 0 {
		return append([]string(nil), h.cfg.Nostr.EncryptedRequestRelays...)
	}
	return nil
}

func (h *SystemHandler) browserEncryptedRequestRelays() []string {
	if len(h.cfg.Nostr.BrowserEncryptedRequestRelays) > 0 {
		return append([]string(nil), h.cfg.Nostr.BrowserEncryptedRequestRelays...)
	}
	return nil
}

func derivePublicKey(privateKey string) string {
	// Handle nsec format
	if len(privateKey) > 4 && privateKey[:4] == "nsec" {
		if _, v, err := nip19.Decode(privateKey); err == nil {
			if sk, ok := v.(string); ok {
				privateKey = sk
			}
		}
	}

	// For hex private key, derive the public key using go-nostr
	if len(privateKey) == 64 {
		pk, err := nostr.GetPublicKey(privateKey)
		if err != nil {
			return ""
		}
		return pk
	}
	return ""
}
