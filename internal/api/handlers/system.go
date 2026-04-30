// Package handlers provides HTTP handlers for the Bahia API.
package handlers

import (
	"encoding/json"
	"net/http"

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

// SystemInfoResponse is the response for GET /api/v1/system/info.
type SystemInfoResponse struct {
	Registries []RegistryInfo `json:"registries"`
	OCIEnabled bool           `json:"oci_enabled"`
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

	resp := SystemInfoResponse{
		Registries: registries,
		OCIEnabled: h.cfg.OCI.Enabled,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{"data": resp})
}
