package dns

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"
)

// AgentHealthStatus is the cached health state of a DNS agent, read from
// NIP-38 kind 30315 status events the agent publishes.
type AgentHealthStatus struct {
	Alive           bool              `json:"alive"`
	LastApplySerial int64             `json:"last_apply_serial"`
	LastApplyAt     string            `json:"last_apply_at"`
	Capabilities    map[string]string // capability → version
	LastSeen        time.Time
	EventID         string
}

// HasCapability checks whether the agent advertises a given capability.
func (s AgentHealthStatus) HasCapability(name string) bool {
	_, ok := s.Capabilities[name]
	return ok
}

// AgentHealthReader subscribes to NIP-38 kind 30315 status events from DNS
// agents and caches their health and capabilities. The daemon reads this
// instead of making ContextVM Health() RPCs to agents (C-34).
type AgentHealthReader struct {
	mu     sync.RWMutex
	agents map[string]*AgentHealthStatus // agentPubkey → status
	logger *zap.Logger
	now    func() time.Time
}

// NewAgentHealthReader creates a health reader.
func NewAgentHealthReader(logger *zap.Logger) *AgentHealthReader {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &AgentHealthReader{
		agents: make(map[string]*AgentHealthStatus),
		logger: logger.Named("agent-health-reader"),
		now:    time.Now,
	}
}

// HandleEvent processes a NIP-38 kind 30315 status event from an agent.
// Call this from the relay subscription callback.
func (r *AgentHealthReader) HandleEvent(_ context.Context, ev nostr.Event) {
	if ev.Kind != 30315 {
		return
	}

	// Check d-tag is "dns-agent".
	dTag := ""
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			dTag = tag[1]
			break
		}
	}
	if dTag != "dns-agent" {
		return
	}

	// Check NIP-40 expiration.
	now := r.now()
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "expiration" {
			expiry, err := strconv.ParseInt(tag[1], 10, 64)
			if err == nil && now.Unix() > expiry {
				return // expired
			}
		}
	}

	// Parse capabilities from tags.
	caps := make(map[string]string)
	for _, tag := range ev.Tags {
		if len(tag) >= 3 && tag[0] == "capability" {
			caps[tag[1]] = tag[2]
		}
	}

	// Parse content.
	var status AgentHealthStatus
	if err := json.Unmarshal([]byte(ev.Content), &status); err != nil {
		r.logger.Warn("invalid agent health event content",
			zap.String("pubkey", ev.PubKey.Hex()),
			zap.Error(err))
		return
	}
	status.Capabilities = caps
	status.LastSeen = now
	status.EventID = ev.ID.Hex()

	pubkey := ev.PubKey.Hex()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.agents[pubkey] = &status
	r.logger.Debug("agent health updated",
		zap.String("pubkey", pubkey),
		zap.Bool("alive", status.Alive),
		zap.Int("capabilities", len(caps)))
}

// Get returns the cached health status for an agent, or nil if unknown.
func (r *AgentHealthReader) Get(agentPubkey string) *AgentHealthStatus {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.agents[agentPubkey]
}

// IsHealthy returns true if the agent's last known status was alive and
// the status event has not expired.
func (r *AgentHealthReader) IsHealthy(agentPubkey string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	status := r.agents[agentPubkey]
	if status == nil {
		return false
	}
	return status.Alive
}

// HasCapability checks whether a specific agent advertises a capability.
func (r *AgentHealthReader) HasCapability(agentPubkey, capability string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	status := r.agents[agentPubkey]
	if status == nil {
		return false
	}
	return status.HasCapability(capability)
}
