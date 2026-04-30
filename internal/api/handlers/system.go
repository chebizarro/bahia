// Package handlers provides HTTP handlers for the Bahia API.
package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/openagentsinc/bahia/internal/config"
)

// SystemHandler handles system info endpoints.
type SystemHandler struct {
	cfg *config.Config
}

// NewSystemHandler creates a new SystemHandler.
func NewSystemHandler(cfg *config.Config) *SystemHandler {
	return &SystemHandler{cfg: cfg}
}

// RegistryInfo describes an available artifact registry.
type RegistryInfo struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	BaseURL  string `json:"base_url"`
	Type     string `json:"type"` // native, harbor, ghcr, dockerhub, quay, custom
	Default  bool   `json:"default,omitempty"`
	Enabled  bool   `json:"enabled"`
}

// NostrConfig describes the Nostr configuration.
type NostrConfigInfo struct {
	Relays         []string `json:"relays"`
	PrivateRelays  []string `json:"private_relays,omitempty"`
	PublishEnabled bool     `json:"publish_enabled"`
	ServicePubkey  string   `json:"service_pubkey,omitempty"`
	ServiceNpub    string   `json:"service_npub,omitempty"`
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
	Type          string   `json:"type"`
	Environments  []string `json:"environments,omitempty"`
}

// OCIConfigInfo describes OCI registry configuration.
type OCIConfigInfo struct {
	Enabled    bool   `json:"enabled"`
	PublicHost string `json:"public_host,omitempty"`
}

// SystemInfoResponse is the response for GET /api/v1/system/info.
type SystemInfoResponse struct {
	Registries []RegistryInfo    `json:"registries"`
	Nostr      NostrConfigInfo   `json:"nostr"`
	Blossom    BlossomConfigInfo `json:"blossom"`
	Runtime    RuntimeConfigInfo `json:"runtime"`
	OCI        OCIConfigInfo     `json:"oci"`
	Features   map[string]bool   `json:"features"`
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
		Relays:         h.cfg.Nostr.Relays,
		PrivateRelays:  h.cfg.Nostr.PrivateRelays,
		PublishEnabled: h.cfg.Nostr.PublishEnabled,
	}

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

	// Feature flags
	features := map[string]bool{
		"oci":           h.cfg.OCI.Enabled,
		"harbor":        h.cfg.Harbor.Enabled,
		"blossom":       h.cfg.Blossom.Enabled,
		"hiveci":        h.cfg.HiveCI.Enabled,
		"cashu":         h.cfg.Cashu.Enabled,
		"telemetry":     h.cfg.Telemetry.Enabled,
		"notifications": h.cfg.Notifications.Enabled,
		"auth":          h.cfg.Auth.Enabled,
	}

	resp := SystemInfoResponse{
		Registries: registries,
		Nostr:      nostrInfo,
		Blossom:    blossomInfo,
		Runtime:    runtimeInfo,
		OCI:        ociInfo,
		Features:   features,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": resp})
}

// derivePublicKey derives a public key hex from a private key (hex or nsec).
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
