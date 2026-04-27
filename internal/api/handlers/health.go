package handlers

import (
	"net/http"

	"github.com/openagentsinc/bahia/internal/api/dto"
)

// WriteHealthJSON writes a HealthResponse as JSON. Exported for use in router.
func WriteHealthJSON(w http.ResponseWriter, status int, resp dto.HealthResponse) {
	writeJSON(w, status, resp)
}
