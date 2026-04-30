package handlers

import (
	"net/http"
	"strings"

	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/repository"
)

// RepositoryCIHandler handles repository CI lookup requests.
type RepositoryCIHandler struct {
	hiveci repository.HiveCIRepository
}

// NewRepositoryCIHandler creates a new RepositoryCIHandler.
func NewRepositoryCIHandler(hiveci repository.HiveCIRepository) *RepositoryCIHandler {
	return &RepositoryCIHandler{hiveci: hiveci}
}

// Lookup handles POST /api/v1/repositories/ci/lookup
func (h *RepositoryCIHandler) Lookup(w http.ResponseWriter, r *http.Request) {
	var req dto.LookupRepositoryCIRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	// Normalize and dedupe coordinates
	seen := make(map[string]bool)
	var coords []string
	for _, c := range req.RepoCoordinates {
		c = strings.TrimSpace(c)
		if c != "" && !seen[c] {
			seen[c] = true
			coords = append(coords, c)
		}
	}

	if len(coords) == 0 {
		writeError(w, http.StatusBadRequest, "repo_coordinates is required and must contain at least one non-empty value")
		return
	}

	if len(coords) > 100 {
		writeError(w, http.StatusBadRequest, "too many coordinates; maximum is 100")
		return
	}

	results, err := h.hiveci.LookupRepositoryCI(r.Context(), coords, req.IncludeDisabledPolicies)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to lookup CI status")
		return
	}

	writeData(w, http.StatusOK, map[string]any{"results": results})
}
