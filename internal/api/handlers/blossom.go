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
