package handlers

import (
	"net/http"

	"github.com/openagentsinc/bahia/internal/config"
)

// SoulFactoryHandler exposes non-secret SoulFactory control-plane policy so the
// web UI can intersect live runtime capabilities with the administratively
// enabled agent runtime set. It never exposes signer, relay credential, or
// workspace secret configuration.
type SoulFactoryHandler struct {
	agentRuntimes []string
}

// NewSoulFactoryHandler creates a handler from the validated config snapshot.
func NewSoulFactoryHandler(cfg *config.Config) *SoulFactoryHandler {
	runtimes := []string{}
	if cfg != nil {
		runtimes = append(runtimes, cfg.SoulFactory.AgentRuntimes...)
	}
	return &SoulFactoryHandler{agentRuntimes: runtimes}
}

// GetRuntimes returns the administratively enabled SoulFactory agent runtime
// targets.
// GET /soulfactory/runtimes
//
// Response:
//
//	{
//	  "data": {
//	    "agent_runtimes": ["openclaw", "metiq"]
//	  }
//	}
func (h *SoulFactoryHandler) GetRuntimes(w http.ResponseWriter, r *http.Request) {
	writeData(w, http.StatusOK, map[string][]string{"agent_runtimes": h.agentRuntimes})
}
