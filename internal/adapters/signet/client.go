// Package signet provides a client for the Signet NIP-46 bunker.
//
// Signet holds agent private keys and provides signing services via NIP-46.
// Agents never hold their own nsec - all signing goes through Signet.
package signet

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip46"
)

var (
	// ErrNoBunkerConfigured is returned when production/default configuration lacks a Signet bunker URI.
	ErrNoBunkerConfigured = errors.New("signet bunker URI is required")
	// ErrNotConnected is returned when an operation requires a connected Signet client.
	ErrNotConnected = errors.New("signet client is not connected")
	// ErrAgentNotFound is returned when an agent identity is unknown to this client.
	ErrAgentNotFound = errors.New("signet agent not found")
	// ErrAgentSuspended is returned when a suspended mock agent is asked to sign.
	ErrAgentSuspended = errors.New("signet agent is suspended")
	// ErrInvalidEvent is returned when a nil Nostr event is passed for signing.
	ErrInvalidEvent = errors.New("nostr event is nil")
)

const (
	// AgentStatusActive is the active Signet agent state.
	AgentStatusActive = "active"
	// AgentStatusSuspended is the suspended Signet agent state.
	AgentStatusSuspended = "suspended"
)

// Client communicates with Signet via NIP-46.
type Client struct {
	bunkerURI       string
	relays          []string
	pool            *nostr.SimplePool
	logger          *slog.Logger
	clientSecretKey string // Ephemeral key for NIP-46 session
	allowMock       bool   // Explicit test/dev-only mock signing mode

	mu        sync.Mutex
	bunker    *nip46.BunkerClient // Active NIP-46 connection
	agents    map[string]*AgentIdentity
	connected bool
}

// AgentIdentity holds the identity info for a provisioned agent.
type AgentIdentity struct {
	AgentID       string
	Pubkey        string
	Npub          string
	BunkerURI     string
	bunkerClient  *nip46.BunkerClient // Agent-specific bunker connection
	mockSecretKey string              // Explicit mock-mode-only agent signing key
	mockStatus    string              // Explicit mock-mode-only lifecycle status
}

// Config holds Signet client configuration.
type Config struct {
	BunkerURI       string   // bunker://<pubkey>?relay=...&secret=...
	Relays          []string // Backup relays if not in URI
	ClientSecretKey string   // Optional: persistent client key (generated if empty)
	AllowMock       bool     // Explicit test/dev-only mock mode; production defaults to fail-closed
}

// NewClient creates a new Signet client.
func NewClient(config Config, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Generate ephemeral client key if not provided
	clientSK := config.ClientSecretKey
	if clientSK == "" {
		clientSK = nostr.GeneratePrivateKey()
	}

	c := &Client{
		bunkerURI:       config.BunkerURI,
		relays:          config.Relays,
		pool:            nostr.NewSimplePool(context.Background()),
		logger:          logger.With("component", "signet"),
		clientSecretKey: clientSK,
		allowMock:       config.AllowMock,
		agents:          make(map[string]*AgentIdentity),
	}

	return c, nil
}

// Connect establishes connection to the Signet bunker.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected {
		return nil
	}

	if c.bunkerURI == "" {
		if !c.allowMock {
			return ErrNoBunkerConfigured
		}
		c.logger.Warn("no bunker URI configured, running in explicit mock mode")
		c.connected = true
		return nil
	}

	c.logger.Info("connecting to Signet bunker", "uri", c.bunkerURI)

	// Connect using go-nostr's NIP-46 implementation
	bunker, err := nip46.ConnectBunker(
		ctx,
		c.clientSecretKey,
		c.bunkerURI,
		c.pool,
		func(authURL string) {
			c.logger.Info("bunker auth required", "url", authURL)
		},
	)
	if err != nil {
		return fmt.Errorf("connect to bunker: %w", err)
	}

	// Verify connection with ping
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := bunker.Ping(pingCtx); err != nil {
		return fmt.Errorf("bunker ping failed: %w", err)
	}

	c.bunker = bunker
	c.connected = true

	c.logger.Info("connected to Signet bunker")
	return nil
}

// IsConnected returns whether the client is connected to a bunker.
func (c *Client) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// IsMockMode returns true if running without a real bunker.
func (c *Client) IsMockMode() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.allowMock && c.bunkerURI == "" && c.connected
}

// ProvisionAgent registers a new agent with Signet.
// Returns the agent's pubkey, npub, and bunker URI.
func (c *Client) ProvisionAgent(ctx context.Context, agentID string, allowedKinds []int) (pubkey, npub, bunkerURI string, err error) {
	c.logger.Info("provisioning agent", "agent_id", agentID, "allowed_kinds", allowedKinds)

	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	c.mu.Unlock()

	if !connected {
		return "", "", "", ErrNotConnected
	}

	if mockMode {
		return c.provisionAgentMock(agentID)
	}

	if bunker == nil {
		return "", "", "", ErrNotConnected
	}

	// Call Signet's custom provision_agent RPC
	params := map[string]interface{}{
		"agent_id":      agentID,
		"allowed_kinds": allowedKinds,
	}
	paramsJSON, _ := json.Marshal(params)

	result, err := bunker.RPC(ctx, "provision_agent", []string{string(paramsJSON)})
	if err != nil {
		return "", "", "", fmt.Errorf("provision_agent RPC failed: %w", err)
	}

	// Parse response
	var resp struct {
		Pubkey    string `json:"pubkey"`
		BunkerURI string `json:"bunker_uri"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return "", "", "", fmt.Errorf("parse provision response: %w", err)
	}

	npubEncoded, _ := nip19.EncodePublicKey(resp.Pubkey)

	identity := &AgentIdentity{
		AgentID:   agentID,
		Pubkey:    resp.Pubkey,
		Npub:      npubEncoded,
		BunkerURI: resp.BunkerURI,
	}

	c.mu.Lock()
	c.agents[agentID] = identity
	c.mu.Unlock()

	c.logger.Info("agent provisioned",
		"agent_id", agentID,
		"pubkey", redactPubkey(resp.Pubkey),
		"npub", npubEncoded,
	)

	return resp.Pubkey, npubEncoded, resp.BunkerURI, nil
}

// provisionAgentMock creates a mock agent identity for testing without a bunker.
func (c *Client) provisionAgentMock(agentID string) (pubkey, npub, bunkerURI string, err error) {
	mockSK := nostr.GeneratePrivateKey()
	mockPK, _ := nostr.GetPublicKey(mockSK)
	mockNpub, _ := nip19.EncodePublicKey(mockPK)

	// Build an intentionally non-production URI so test/dev mock identities are obvious.
	agentBunkerURI := fmt.Sprintf("mock-bunker://%s", mockPK)

	identity := &AgentIdentity{
		AgentID:       agentID,
		Pubkey:        mockPK,
		Npub:          mockNpub,
		BunkerURI:     agentBunkerURI,
		mockSecretKey: mockSK,
		mockStatus:    AgentStatusActive,
	}

	c.mu.Lock()
	c.agents[agentID] = identity
	c.mu.Unlock()

	c.logger.Info("agent provisioned (explicit mock mode)",
		"agent_id", agentID,
		"pubkey", redactPubkey(mockPK),
		"npub", mockNpub,
	)

	return mockPK, mockNpub, agentBunkerURI, nil
}

// Sign signs an event using the Signet bunker's key.
func (c *Client) Sign(ctx context.Context, event *nostr.Event) error {
	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	c.mu.Unlock()

	if !connected {
		return ErrNotConnected
	}

	if mockMode {
		return c.signMock(event)
	}

	if bunker == nil {
		return ErrNotConnected
	}

	// Use NIP-46 SignEvent
	if err := bunker.SignEvent(ctx, event); err != nil {
		return fmt.Errorf("bunker sign_event: %w", err)
	}

	return nil
}

// signMock signs with the explicit mock client's stable key for testing.
func (c *Client) signMock(event *nostr.Event) error {
	return signEventWithKey(event, c.clientSecretKey)
}

// SignAs signs an event as a specific agent.
func (c *Client) SignAs(ctx context.Context, agentID string, event *nostr.Event) error {
	c.mu.Lock()
	connected := c.connected
	identity, ok := c.agents[agentID]
	mockMode := c.allowMock && c.bunkerURI == ""
	c.mu.Unlock()

	if !connected {
		return ErrNotConnected
	}

	if !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	if mockMode {
		if identity.mockStatus == AgentStatusSuspended {
			return fmt.Errorf("%w: %s", ErrAgentSuspended, agentID)
		}
		if identity.mockSecretKey == "" {
			return fmt.Errorf("mock agent missing signing key: %s", agentID)
		}
		c.logger.Debug("signing as agent (explicit mock mode)", "agent_id", agentID)
		return signEventWithKey(event, identity.mockSecretKey)
	}

	// Connect to agent's bunker if needed
	if identity.bunkerClient == nil {
		bunker, err := nip46.ConnectBunker(
			ctx,
			c.clientSecretKey,
			identity.BunkerURI,
			c.pool,
			nil,
		)
		if err != nil {
			return fmt.Errorf("connect to agent bunker: %w", err)
		}

		c.mu.Lock()
		identity.bunkerClient = bunker
		c.mu.Unlock()
	}

	// Sign using agent's bunker connection
	if err := identity.bunkerClient.SignEvent(ctx, event); err != nil {
		return fmt.Errorf("agent sign_event: %w", err)
	}

	c.logger.Debug("signed as agent", "agent_id", agentID, "pubkey", redactPubkey(identity.Pubkey))
	return nil
}

// RevokeAgent permanently destroys an agent's keypair.
func (c *Client) RevokeAgent(ctx context.Context, pubkey string) error {
	c.logger.Info("revoking agent", "pubkey", redactPubkey(pubkey))

	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	c.mu.Unlock()

	if !connected {
		return ErrNotConnected
	}

	if !mockMode && bunker == nil {
		return ErrNotConnected
	}

	if !mockMode {
		// Call Signet's custom revoke_agent RPC
		params := map[string]interface{}{
			"pubkey": pubkey,
		}
		paramsJSON, _ := json.Marshal(params)

		_, err := bunker.RPC(ctx, "revoke_agent", []string{string(paramsJSON)})
		if err != nil {
			return fmt.Errorf("revoke_agent RPC failed: %w", err)
		}
	}

	// Remove from local cache
	if !c.removeAgentByPubkey(pubkey) && mockMode {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, pubkey)
	}

	c.logger.Info("agent revoked", "pubkey", redactPubkey(pubkey))
	return nil
}

// SuspendAgent temporarily blocks signing for an agent.
func (c *Client) SuspendAgent(ctx context.Context, pubkey string) error {
	c.logger.Info("suspending agent", "pubkey", redactPubkey(pubkey))

	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	c.mu.Unlock()

	if !connected {
		return ErrNotConnected
	}

	if mockMode {
		if err := c.setMockAgentStatus(pubkey, AgentStatusSuspended); err != nil {
			return err
		}
		c.logger.Info("agent suspended (explicit mock mode)", "pubkey", redactPubkey(pubkey))
		return nil
	}

	if bunker == nil {
		return ErrNotConnected
	}

	// Call Signet's custom suspend_agent RPC
	params := map[string]interface{}{
		"pubkey": pubkey,
	}
	paramsJSON, _ := json.Marshal(params)

	_, err := bunker.RPC(ctx, "suspend_agent", []string{string(paramsJSON)})
	if err != nil {
		return fmt.Errorf("suspend_agent RPC failed: %w", err)
	}

	c.logger.Info("agent suspended", "pubkey", redactPubkey(pubkey))
	return nil
}

// ResumeAgent re-enables signing for a suspended agent.
func (c *Client) ResumeAgent(ctx context.Context, pubkey string) error {
	c.logger.Info("resuming agent", "pubkey", redactPubkey(pubkey))

	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	c.mu.Unlock()

	if !connected {
		return ErrNotConnected
	}

	if mockMode {
		if err := c.setMockAgentStatus(pubkey, AgentStatusActive); err != nil {
			return err
		}
		c.logger.Info("agent resumed (explicit mock mode)", "pubkey", redactPubkey(pubkey))
		return nil
	}

	if bunker == nil {
		return ErrNotConnected
	}

	// Call Signet's custom resume_agent RPC
	params := map[string]interface{}{
		"pubkey": pubkey,
	}
	paramsJSON, _ := json.Marshal(params)

	_, err := bunker.RPC(ctx, "resume_agent", []string{string(paramsJSON)})
	if err != nil {
		return fmt.Errorf("resume_agent RPC failed: %w", err)
	}

	c.logger.Info("agent resumed", "pubkey", redactPubkey(pubkey))
	return nil
}

// GetAgentStatus checks the status of an agent in Signet.
func (c *Client) GetAgentStatus(ctx context.Context, pubkey string) (string, error) {
	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	c.mu.Unlock()

	if !connected {
		return "", ErrNotConnected
	}

	if mockMode {
		return c.getMockAgentStatus(pubkey)
	}

	if bunker == nil {
		return "", ErrNotConnected
	}

	// Call Signet's custom get_agent_status RPC
	params := map[string]interface{}{
		"pubkey": pubkey,
	}
	paramsJSON, _ := json.Marshal(params)

	result, err := bunker.RPC(ctx, "get_agent_status", []string{string(paramsJSON)})
	if err != nil {
		return "", fmt.Errorf("get_agent_status RPC failed: %w", err)
	}

	var resp struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(result), &resp); err != nil {
		return "", fmt.Errorf("parse status response: %w", err)
	}

	return resp.Status, nil
}

// ListAgents returns all agents managed by this client.
func (c *Client) ListAgents(ctx context.Context) ([]*AgentIdentity, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	agents := make([]*AgentIdentity, 0, len(c.agents))
	for _, identity := range c.agents {
		agents = append(agents, identity)
	}
	return agents, nil
}

// GetPublicKey returns the bunker's public key.
func (c *Client) GetPublicKey(ctx context.Context) (string, error) {
	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	c.mu.Unlock()

	if !connected {
		return "", ErrNotConnected
	}

	if mockMode {
		pk, _ := nostr.GetPublicKey(c.clientSecretKey)
		return pk, nil
	}

	if bunker == nil {
		return "", ErrNotConnected
	}

	return bunker.GetPublicKey(ctx)
}

// --- NIP-98 Auth Helpers ---

// SignNIP98 creates a NIP-98 auth header for HTTP requests.
func (c *Client) SignNIP98(ctx context.Context, url, method string, payloadHash string) (string, error) {
	event := &nostr.Event{
		Kind:      27235, // NIP-98
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"u", url},
			{"method", method},
		},
		Content: "",
	}

	if payloadHash != "" {
		event.Tags = append(event.Tags, nostr.Tag{"payload", payloadHash})
	}

	if err := c.Sign(ctx, event); err != nil {
		return "", fmt.Errorf("sign NIP-98: %w", err)
	}

	// Encode event as base64 for Authorization header
	eventJSON, _ := json.Marshal(event)
	encoded := base64.StdEncoding.EncodeToString(eventJSON)
	return "Nostr " + encoded, nil
}

// Close shuts down the client and all connections.
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Close agent bunker connections
	for _, identity := range c.agents {
		if identity.bunkerClient != nil {
			// Note: BunkerClient doesn't have a Close method currently
		}
	}

	c.connected = false
	c.bunker = nil

	c.logger.Info("signet client closed")
	return nil
}

// ParseBunkerURI parses a bunker:// URI into its components.
func ParseBunkerURI(uri string) (pubkey string, relays []string, secret string, err error) {
	if !strings.HasPrefix(uri, "bunker://") {
		return "", nil, "", fmt.Errorf("invalid bunker URI: must start with bunker://")
	}

	// Parse as URL
	u, err := url.Parse(uri)
	if err != nil {
		return "", nil, "", fmt.Errorf("parse bunker URI: %w", err)
	}

	pubkey = u.Host
	if len(pubkey) != 64 {
		return "", nil, "", fmt.Errorf("invalid pubkey in bunker URI: expected 64 hex chars")
	}

	// Extract relays and secret from query params
	for key, values := range u.Query() {
		switch key {
		case "relay":
			relays = append(relays, values...)
		case "secret":
			if len(values) > 0 {
				secret = values[0]
			}
		}
	}

	return pubkey, relays, secret, nil
}

func signEventWithKey(event *nostr.Event, secretKey string) error {
	if event == nil {
		return ErrInvalidEvent
	}

	pubkey, err := nostr.GetPublicKey(secretKey)
	if err != nil {
		return fmt.Errorf("derive public key: %w", err)
	}
	event.PubKey = pubkey

	if err := event.Sign(secretKey); err != nil {
		return fmt.Errorf("sign event: %w", err)
	}

	return nil
}

func (c *Client) setMockAgentStatus(pubkey, status string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, identity := range c.agents {
		if identity.Pubkey == pubkey {
			identity.mockStatus = status
			return nil
		}
	}

	return fmt.Errorf("%w: %s", ErrAgentNotFound, pubkey)
}

func (c *Client) getMockAgentStatus(pubkey string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, identity := range c.agents {
		if identity.Pubkey == pubkey {
			if identity.mockStatus == "" {
				return AgentStatusActive, nil
			}
			return identity.mockStatus, nil
		}
	}

	return "", fmt.Errorf("%w: %s", ErrAgentNotFound, pubkey)
}

func (c *Client) removeAgentByPubkey(pubkey string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	for id, identity := range c.agents {
		if identity.Pubkey == pubkey {
			delete(c.agents, id)
			return true
		}
	}
	return false
}

func redactPubkey(pubkey string) string {
	if len(pubkey) <= 16 {
		return pubkey
	}
	return pubkey[:16] + "..."
}

// Ensure Client implements the expected interface
var _ interface {
	Connect(ctx context.Context) error
	Sign(ctx context.Context, event *nostr.Event) error
	SignAs(ctx context.Context, agentID string, event *nostr.Event) error
	ProvisionAgent(ctx context.Context, agentID string, allowedKinds []int) (pubkey, npub, bunkerURI string, err error)
	RevokeAgent(ctx context.Context, pubkey string) error
	SuspendAgent(ctx context.Context, pubkey string) error
	ResumeAgent(ctx context.Context, pubkey string) error
	GetPublicKey(ctx context.Context) (string, error)
	Close() error
} = (*Client)(nil)
