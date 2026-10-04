package handlers

import (
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
)

// BlossomHandler handles HTTP requests for Blossom blob operations.
type BlossomHandler struct {
	client *blossom.Client
}

// NewBlossomHandler creates a new BlossomHandler.
func NewBlossomHandler(client *blossom.Client) *BlossomHandler {
	return &BlossomHandler{client: client}
}

// GetServers returns the configured Blossom server URLs.
// GET /blossom/servers
func (h *BlossomHandler) GetServers(w http.ResponseWriter, r *http.Request) {
	servers := h.client.Servers()
	writeData(w, http.StatusOK, servers)
}

// HealthCheck checks connectivity to all Blossom servers.
// GET /blossom/health
func (h *BlossomHandler) HealthCheck(w http.ResponseWriter, r *http.Request) {
	results := h.client.HealthCheck(r.Context())

	// Convert error map to string map for JSON serialization
	statusMap := make(map[string]string)
	allHealthy := true
	for server, err := range results {
		if err != nil {
			statusMap[server] = err.Error()
			allHealthy = false
		} else {
			statusMap[server] = "ok"
		}
	}

	status := http.StatusOK
	if !allHealthy {
		status = http.StatusServiceUnavailable
	}

	writeData(w, status, statusMap)
}

// DownloadBlob proxies a Blossom blob download by SHA-256 hash.
// The backend fetches from configured Blossom servers (which may use internal
// HTTP addresses) and streams the content back over the HTTPS API, avoiding
// mixed-content browser errors.
// GET /blossom/blob/{hash}
func (h *BlossomHandler) DownloadBlob(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	if hash == "" || len(hash) != 64 {
		writeError(w, http.StatusBadRequest, "invalid SHA-256 hash")
		return
	}

	data, err := h.client.DownloadByHash(r.Context(), hash)
	if err != nil {
		if strings.Contains(err.Error(), "failed to download") {
			writeError(w, http.StatusNotFound, "blob not found")
			return
		}
		writeError(w, http.StatusBadGateway, "failed to fetch blob from Blossom server")
		return
	}

	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		return
	}
}

// GetStats returns upload/download statistics for all servers.
// GET /blossom/stats
func (h *BlossomHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	stats := h.client.GetStats()
	writeData(w, http.StatusOK, stats)
}
