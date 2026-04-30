package handlers

import (
	"net/http"

	"github.com/openagentsinc/bahia/internal/adapters/blossom"
	"github.com/openagentsinc/bahia/internal/api/dto"
)

// BlossomHandler handles HTTP requests for Blossom blob operations.
type BlossomHandler struct {
	client *blossom.Client
}

// NewBlossomHandler creates a new BlossomHandler.
func NewBlossomHandler(client *blossom.Client) *BlossomHandler {
	return &BlossomHandler{client: client}
}

// ListBlobs lists blobs from configured Blossom servers.
// POST /blossom/list
//
// Request body:
//
//	{
//	  "pubkey": "optional-hex-pubkey"  // If empty, lists own blobs (requires server identity)
//	}
//
// Response:
//
//	{
//	  "data": [
//	    {
//	      "url": "https://blossom.example.com/abc123...",
//	      "sha256": "abc123...",
//	      "size": 12345,
//	      "type": "image/png",
//	      "uploaded": "2024-01-15T10:30:00Z"
//	    }
//	  ]
//	}
func (h *BlossomHandler) ListBlobs(w http.ResponseWriter, r *http.Request) {
	var req dto.ListBlossomBlobsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	var blobs []blossom.BlobDescriptor
	var err error

	if req.Pubkey != "" {
		// List blobs for specified pubkey
		blobs, err = h.client.ListByPubkey(r.Context(), req.Pubkey)
	} else {
		// List own blobs (requires private key)
		blobs, err = h.client.List(r.Context())
	}

	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	writeData(w, http.StatusOK, blobs)
}

// GetServers returns the list of configured Blossom server URLs.
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

// GetStats returns upload/download statistics for all servers.
// GET /blossom/stats
func (h *BlossomHandler) GetStats(w http.ResponseWriter, r *http.Request) {
	stats := h.client.GetStats()
	writeData(w, http.StatusOK, stats)
}
