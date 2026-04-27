package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
)

// NIP05Manager handles NIP-05 registration for agents.
type NIP05Manager struct {
	domain       string
	wellKnownDir string
	logger       *slog.Logger
	mu           sync.Mutex
}

// NIP05Config holds NIP-05 manager configuration.
type NIP05Config struct {
	Domain       string // Domain for NIP-05 (e.g., sharegap.net)
	WellKnownDir string // Path to .well-known directory
}

// NIP05JSON represents the nostr.json file structure.
type NIP05JSON struct {
	Names  map[string]string   `json:"names"`
	Relays map[string][]string `json:"relays,omitempty"`
}

// NewNIP05Manager creates a new NIP-05 manager.
func NewNIP05Manager(config NIP05Config, logger *slog.Logger) *NIP05Manager {
	if config.Domain == "" {
		config.Domain = "sharegap.net"
	}
	if logger == nil {
		logger = slog.Default()
	}

	return &NIP05Manager{
		domain:       config.Domain,
		wellKnownDir: config.WellKnownDir,
		logger:       logger.With("component", "nip05"),
	}
}

// Register adds an agent to the NIP-05 registry.
func (m *NIP05Manager) Register(ctx context.Context, agentID, pubkey string, relays []string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("registering NIP-05",
		"agent_id", agentID,
		"pubkey", pubkey[:16]+"...",
		"domain", m.domain,
	)

	// If no well-known dir configured, just log
	if m.wellKnownDir == "" {
		m.logger.Info("NIP-05 registration (simulated - no well-known dir configured)",
			"nip05", fmt.Sprintf("%s@%s", agentID, m.domain),
		)
		return nil
	}

	// Load existing nostr.json
	nip05, err := m.loadNIP05()
	if err != nil {
		return fmt.Errorf("load nostr.json: %w", err)
	}

	// Add entry
	nip05.Names[agentID] = pubkey

	// Add relays if provided
	if len(relays) > 0 {
		if nip05.Relays == nil {
			nip05.Relays = make(map[string][]string)
		}
		nip05.Relays[pubkey] = relays
	}

	// Save
	if err := m.saveNIP05(nip05); err != nil {
		return fmt.Errorf("save nostr.json: %w", err)
	}

	m.logger.Info("NIP-05 registered",
		"nip05", fmt.Sprintf("%s@%s", agentID, m.domain),
	)

	return nil
}

// Unregister removes an agent from the NIP-05 registry.
func (m *NIP05Manager) Unregister(ctx context.Context, agentID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.logger.Info("unregistering NIP-05", "agent_id", agentID)

	if m.wellKnownDir == "" {
		return nil
	}

	nip05, err := m.loadNIP05()
	if err != nil {
		return fmt.Errorf("load nostr.json: %w", err)
	}

	// Get pubkey before deletion for relay cleanup
	pubkey := nip05.Names[agentID]

	// Remove entry
	delete(nip05.Names, agentID)

	// Remove relays if they exist
	if pubkey != "" && nip05.Relays != nil {
		delete(nip05.Relays, pubkey)
	}

	if err := m.saveNIP05(nip05); err != nil {
		return fmt.Errorf("save nostr.json: %w", err)
	}

	return nil
}

// Lookup checks if an agent is registered.
func (m *NIP05Manager) Lookup(ctx context.Context, agentID string) (pubkey string, exists bool, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.wellKnownDir == "" {
		return "", false, nil
	}

	nip05, err := m.loadNIP05()
	if err != nil {
		return "", false, fmt.Errorf("load nostr.json: %w", err)
	}

	pubkey, exists = nip05.Names[agentID]
	return pubkey, exists, nil
}

// GetNIP05 returns the full NIP-05 identifier for an agent.
func (m *NIP05Manager) GetNIP05(agentID string) string {
	return fmt.Sprintf("%s@%s", agentID, m.domain)
}

func (m *NIP05Manager) loadNIP05() (*NIP05JSON, error) {
	path := filepath.Join(m.wellKnownDir, "nostr.json")

	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		// Return empty structure if file doesn't exist
		return &NIP05JSON{
			Names:  make(map[string]string),
			Relays: make(map[string][]string),
		}, nil
	}
	if err != nil {
		return nil, err
	}

	var nip05 NIP05JSON
	if err := json.Unmarshal(data, &nip05); err != nil {
		return nil, err
	}

	if nip05.Names == nil {
		nip05.Names = make(map[string]string)
	}

	return &nip05, nil
}

func (m *NIP05Manager) saveNIP05(nip05 *NIP05JSON) error {
	// Ensure directory exists
	if err := os.MkdirAll(m.wellKnownDir, 0755); err != nil {
		return err
	}

	path := filepath.Join(m.wellKnownDir, "nostr.json")

	data, err := json.MarshalIndent(nip05, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(path, data, 0644)
}
