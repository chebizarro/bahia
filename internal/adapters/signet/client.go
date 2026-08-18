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

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip19"
	"fiatjaf.com/nostr/nip44"
	"fiatjaf.com/nostr/nip46"
	cascadia "git.sharegap.net/cascadia/cascadia-go"
	"github.com/openagentsinc/bahia/internal/nostrutil"
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
	// ErrAuthoritativeAgentListingUnsupported prevents volatile cache state from being presented as Signet truth.
	ErrAuthoritativeAgentListingUnsupported = errors.New("authoritative Signet agent listing is unsupported")
)

const (
	// AgentStatusActive is the active Signet agent state.
	AgentStatusActive = "active"
	// AgentStatusSuspended is the suspended Signet agent state.
	AgentStatusSuspended = "suspended"

	// Signet's canonical ContextVM management plane is kind 25910. The pinned
	// cascadia-go release still aliases CAS_INTENT to the obsolete kind 6950.
	// Keep the wire boundary explicit until that dependency is corrected.
	signetKindContextVM nostr.Kind = 25910
	signetKindGiftWrap             = cascadia.NIP59_GIFT_WRAP

	// signetMethodNIP44EncryptBinary is Signet's binary-safe NIP-44 encrypt:
	// params are [recipient_pubkey_hex, base64(plaintext)] and the result is an
	// ordinary NIP-44 v2 payload. It exists because NIP-46's JSON params cannot
	// carry non-UTF-8 plaintext.
	signetMethodNIP44EncryptBinary = "nip44_encrypt_b64"
)

// Client communicates with Signet via NIP-46.
type Client struct {
	bunkerURI       string
	relays          []string
	pool            *nostr.Pool
	logger          *slog.Logger
	clientSecretKey string // Ephemeral key for NIP-46 session
	requireReal     bool   // Fail closed unless a real Signet bunker is configured and reachable
	allowMock       bool   // Explicit test/dev-only mock signing mode
	connectTimeout  time.Duration

	connectMu    sync.Mutex
	mu           sync.Mutex
	bunker       *nip46.BunkerClient // Active NIP-46 connection
	agents       map[string]*AgentIdentity
	connected    bool
	lifetime     context.Context
	stateChanged chan struct{}
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
	BunkerURI       string        // bunker://<pubkey>?relay=...&secret=...
	Relays          []string      // Backup relays if not in URI
	ClientSecretKey string        // Optional: persistent client key (generated if empty)
	RequireReal     bool          // When true, missing/unreachable bunker is a hard error
	AllowMock       bool          // Legacy explicit test/dev-only mock mode; production callers should prefer RequireReal=true
	ConnectTimeout  time.Duration // Bounds each connection attempt without becoming the successful connection lifetime
	SignTimeout     time.Duration // Deprecated: caller context controls signing lifetime
}

// NewClient creates a new Signet client.
func NewClient(config Config, logger *slog.Logger) (*Client, error) {
	if logger == nil {
		logger = slog.Default()
	}

	// Generate ephemeral client key if not provided
	clientSK := config.ClientSecretKey
	if clientSK == "" {
		clientSK = nostrutil.GeneratePrivateKeyHex()
	}

	c := &Client{
		bunkerURI:       config.BunkerURI,
		relays:          config.Relays,
		pool:            nostr.NewPool(),
		logger:          logger.With("component", "signet"),
		clientSecretKey: clientSK,
		requireReal:     config.RequireReal,
		allowMock:       config.AllowMock,
		connectTimeout:  config.ConnectTimeout,
		agents:          make(map[string]*AgentIdentity),
		stateChanged:    make(chan struct{}),
	}

	return c, nil
}

// ConnectAttemptTimeout returns the configured orchestration timeout.
func (c *Client) ConnectAttemptTimeout() time.Duration { return c.connectTimeout }

// Connect establishes connection to the Signet bunker.
func (c *Client) Connect(ctx context.Context) error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	c.mu.Lock()
	if c.connected {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	if c.bunkerURI == "" {
		if c.requireReal || !c.allowMock {
			return ErrNoBunkerConfigured
		}
		c.logger.Warn("no bunker URI configured, running in explicit dev/mock mode")
		c.setConnection(nil, ctx, true)
		return nil
	}

	bunkerPubkey, relayHosts := bunkerLogDetails(c.bunkerURI)
	c.logger.Info("connecting to Signet bunker", "bunker_pubkey", bunkerPubkey, "relay_hosts", relayHosts)

	// Connect using the canonical NIP-46 implementation.
	clientSecret, err := nostrutil.SecretKeyFromHex(c.clientSecretKey)
	if err != nil {
		return fmt.Errorf("decode Signet client secret key: %w", err)
	}

	// ConnectBunker retains ctx for its response subscription. The application
	// context is therefore the connection lifetime; installing a child deadline
	// here silently kills later NIP-46 RPCs.
	bunker, err := nip46.ConnectBunker(
		ctx,
		clientSecret,
		c.bunkerURI,
		c.pool,
		func(authURL string) {
			c.logger.Info("bunker auth required", "url", authURL)
		},
	)
	if err != nil {
		return fmt.Errorf("connect to bunker: %w", err)
	}
	if err := bunker.Ping(ctx); err != nil {
		return fmt.Errorf("bunker ping failed: %w", err)
	}

	c.setConnection(bunker, ctx, true)

	c.logger.Info("connected to Signet bunker")
	return nil
}

// Ping verifies that the currently installed bunker connection is responsive.
func (c *Client) Ping(ctx context.Context) error {
	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	c.mu.Unlock()
	if !connected {
		return ErrNotConnected
	}
	if mockMode {
		return nil
	}
	if bunker == nil {
		return ErrNotConnected
	}
	if err := bunker.Ping(ctx); err != nil {
		return fmt.Errorf("bunker ping failed: %w", err)
	}
	return nil
}

// WaitUntilConnected blocks until a connection is available or ctx is cancelled.
func (c *Client) WaitUntilConnected(ctx context.Context) error {
	return c.waitForConnectionState(ctx, true)
}

// WaitUntilDisconnected blocks until the current connection is lost or ctx is cancelled.
func (c *Client) WaitUntilDisconnected(ctx context.Context) error {
	return c.waitForConnectionState(ctx, false)
}

func (c *Client) waitForConnectionState(ctx context.Context, want bool) error {
	for {
		c.mu.Lock()
		connected := c.connected
		changed := c.stateChanged
		c.mu.Unlock()
		if connected == want {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

func (c *Client) setConnection(bunker *nip46.BunkerClient, lifetime context.Context, connected bool) {
	c.mu.Lock()
	changed := c.connected != connected || c.bunker != bunker
	c.bunker = bunker
	c.lifetime = lifetime
	c.connected = connected
	if changed {
		close(c.stateChanged)
		c.stateChanged = make(chan struct{})
	}
	c.mu.Unlock()
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

	var resp struct {
		Pubkey    string `json:"pubkey"`
		BunkerURI string `json:"bunker_uri"`
	}
	if err := c.callManagement(ctx, "agent/provision", map[string]interface{}{
		"agent_id":      agentID,
		"allowed_kinds": allowedKinds,
	}, &resp); err != nil {
		return "", "", "", fmt.Errorf("agent/provision intent failed: %w", err)
	}

	provisionedPubKey, err := nostrutil.PubKeyFromHex(resp.Pubkey)
	if err != nil {
		return "", "", "", fmt.Errorf("decode provisioned pubkey: %w", err)
	}
	npubEncoded := nip19.EncodeNpub(provisionedPubKey)

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
	mockSK := nostrutil.GeneratePrivateKeyHex()
	mockPK, _ := nostrutil.PublicKeyHexFromPrivateKeyHex(mockSK)
	mockNpub, _ := nostrutil.EncodeNpubFromHex(mockPK)

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

	if err := bunker.SignEvent(ctx, event); err != nil {
		return fmt.Errorf("bunker sign_event: %w", err)
	}

	return nil
}

// NIP44Encrypt encrypts plaintext to a recipient using the Signet-held staff key.
// In production the private key remains inside the NIP-46 bunker.
func (c *Client) NIP44Encrypt(ctx context.Context, recipient nostr.PubKey, plaintext string) (string, error) {
	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	clientSecretKey := c.clientSecretKey
	c.mu.Unlock()

	if !connected {
		return "", ErrNotConnected
	}
	if mockMode {
		secret, err := nostr.SecretKeyFromHex(clientSecretKey)
		if err != nil {
			return "", fmt.Errorf("decode mock Signet key: %w", err)
		}
		conversationKey, err := nip44.GenerateConversationKey(recipient, secret)
		if err != nil {
			return "", fmt.Errorf("derive mock Signet NIP-44 conversation key: %w", err)
		}
		ciphertext, err := nip44.Encrypt(plaintext, conversationKey)
		if err != nil {
			return "", fmt.Errorf("mock Signet NIP-44 encrypt: %w", err)
		}
		return ciphertext, nil
	}
	if bunker == nil {
		return "", ErrNotConnected
	}
	ciphertext, err := bunker.NIP44Encrypt(ctx, recipient, plaintext)
	if err != nil {
		return "", fmt.Errorf("bunker nip44_encrypt: %w", err)
	}
	return ciphertext, nil
}

// NIP44EncryptBytes encrypts a raw binary plaintext to a recipient using the
// Signet-held staff key.
//
// NIP-46 carries params as JSON strings, so plaintext that is not valid UTF-8
// cannot travel over NIP44Encrypt: Go's encoder substitutes U+FFFD for every
// invalid byte and the bunker would encrypt corrupted material without anyone
// noticing. Concord CORD-06 rekey blobs are fixed-width binary whose width is
// the format signal (72/104/136 bytes), so silent substitution is not a
// recoverable error. This path base64-encodes the plaintext for the transport
// only; the bunker decodes it and encrypts the exact bytes, returning an
// ordinary NIP-44 v2 payload.
//
// A bunker without the method fails the call rather than returning a payload
// over mangled bytes, so an out-of-date Signet degrades to a loud error.
func (c *Client) NIP44EncryptBytes(ctx context.Context, recipient nostr.PubKey, plaintext []byte) (string, error) {
	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	clientSecretKey := c.clientSecretKey
	c.mu.Unlock()

	if !connected {
		return "", ErrNotConnected
	}
	if len(plaintext) == 0 {
		return "", fmt.Errorf("Signet NIP-44 binary encrypt requires a non-empty plaintext")
	}
	if mockMode {
		secret, err := nostr.SecretKeyFromHex(clientSecretKey)
		if err != nil {
			return "", fmt.Errorf("decode mock Signet key: %w", err)
		}
		conversationKey, err := nip44.GenerateConversationKey(recipient, secret)
		if err != nil {
			return "", fmt.Errorf("derive mock Signet NIP-44 conversation key: %w", err)
		}
		// A Go string is a byte string, so the local path carries binary
		// plaintext verbatim; only the JSON transport needs the encoding.
		ciphertext, err := nip44.Encrypt(string(plaintext), conversationKey)
		if err != nil {
			return "", fmt.Errorf("mock Signet NIP-44 encrypt: %w", err)
		}
		return ciphertext, nil
	}
	if bunker == nil {
		return "", ErrNotConnected
	}
	ciphertext, err := bunker.RPC(ctx, signetMethodNIP44EncryptBinary, []string{
		recipient.Hex(),
		base64.StdEncoding.EncodeToString(plaintext),
	})
	if err != nil {
		return "", fmt.Errorf("bunker %s: %w", signetMethodNIP44EncryptBinary, err)
	}
	if strings.TrimSpace(ciphertext) == "" {
		return "", fmt.Errorf("bunker %s returned an empty payload", signetMethodNIP44EncryptBinary)
	}
	return ciphertext, nil
}

// NIP44Decrypt decrypts ciphertext from a counterparty using the Signet-held
// staff key. Sealing custody to the staff pubkey and unsealing it here keeps
// Concord invite material ciphertext at rest: the private key never leaves the
// NIP-46 bunker in production.
func (c *Client) NIP44Decrypt(ctx context.Context, counterparty nostr.PubKey, ciphertext string) (string, error) {
	c.mu.Lock()
	connected := c.connected
	mockMode := c.allowMock && c.bunkerURI == ""
	bunker := c.bunker
	clientSecretKey := c.clientSecretKey
	c.mu.Unlock()

	if !connected {
		return "", ErrNotConnected
	}
	if mockMode {
		secret, err := nostr.SecretKeyFromHex(clientSecretKey)
		if err != nil {
			return "", fmt.Errorf("decode mock Signet key: %w", err)
		}
		conversationKey, err := nip44.GenerateConversationKey(counterparty, secret)
		if err != nil {
			return "", fmt.Errorf("derive mock Signet NIP-44 conversation key: %w", err)
		}
		plaintext, err := nip44.Decrypt(ciphertext, conversationKey)
		if err != nil {
			return "", fmt.Errorf("mock Signet NIP-44 decrypt: %w", err)
		}
		return plaintext, nil
	}
	if bunker == nil {
		return "", ErrNotConnected
	}
	plaintext, err := bunker.NIP44Decrypt(ctx, counterparty, ciphertext)
	if err != nil {
		return "", fmt.Errorf("bunker nip44_decrypt: %w", err)
	}
	return plaintext, nil
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
		clientSecret, err := nostrutil.SecretKeyFromHex(c.clientSecretKey)
		if err != nil {
			return fmt.Errorf("decode Signet client secret key: %w", err)
		}
		connectCtx := c.lifetime
		if connectCtx == nil {
			connectCtx = ctx
		}
		bunker, err := nip46.ConnectBunker(
			connectCtx,
			clientSecret,
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
		if err := c.callManagement(ctx, "agent/revoke", c.managementParamsForPubkey(pubkey), nil); err != nil {
			return fmt.Errorf("agent/revoke intent failed: %w", err)
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

	params := c.managementParamsForPubkey(pubkey)
	params["policy"] = map[string]interface{}{"status": AgentStatusSuspended}
	if err := c.callManagement(ctx, "agent/set-policy", params, nil); err != nil {
		return fmt.Errorf("agent/set-policy suspend intent failed: %w", err)
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

	params := c.managementParamsForPubkey(pubkey)
	params["policy"] = map[string]interface{}{"status": AgentStatusActive}
	if err := c.callManagement(ctx, "agent/set-policy", params, nil); err != nil {
		return fmt.Errorf("agent/set-policy resume intent failed: %w", err)
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

	var resp struct {
		Status string `json:"status"`
	}
	if err := c.callManagement(ctx, "agent/get-status", c.managementParamsForPubkey(pubkey), &resp); err != nil {
		return "", fmt.Errorf("agent/get-status intent failed: %w", err)
	}

	return resp.Status, nil
}

// ListAgents refuses to present the volatile process cache as an authoritative
// Signet inventory. Use ListCachedAgents when cache-only semantics are intended.
func (c *Client) ListAgents(context.Context) ([]*AgentIdentity, error) {
	return nil, ErrAuthoritativeAgentListingUnsupported
}

// ListCachedAgents returns only identities learned by this Client process.
func (c *Client) ListCachedAgents() []*AgentIdentity {
	c.mu.Lock()
	defer c.mu.Unlock()

	agents := make([]*AgentIdentity, 0, len(c.agents))
	for _, identity := range c.agents {
		agents = append(agents, identity)
	}
	return agents
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
		pk, err := nostrutil.PublicKeyHexFromPrivateKeyHex(c.clientSecretKey)
		if err != nil {
			return "", fmt.Errorf("derive mock public key: %w", err)
		}
		return pk, nil
	}

	if bunker == nil {
		return "", ErrNotConnected
	}

	pubkey, err := bunker.GetPublicKey(ctx)
	if err != nil {
		return "", err
	}
	return pubkey.Hex(), nil
}

// ConfiguredPublicKey returns the identity encoded in configuration without
// requiring a live bunker connection. It is safe for startup wiring only; a
// successful connection still verifies operational access before signing.
func (c *Client) ConfiguredPublicKey() (string, error) {
	if strings.TrimSpace(c.bunkerURI) != "" {
		pubkey, _, _, err := ParseBunkerURI(c.bunkerURI)
		return pubkey, err
	}
	if c.allowMock && strings.TrimSpace(c.bunkerURI) == "" {
		pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(c.clientSecretKey)
		if err != nil {
			return "", fmt.Errorf("derive mock public key: %w", err)
		}
		return pubkey, nil
	}
	return "", fmt.Errorf("Signet signing pubkey is unavailable before connection")
}

type signetJSONRPCRequest struct {
	JSONRPC string                 `json:"jsonrpc"`
	ID      string                 `json:"id"`
	Method  string                 `json:"method"`
	Params  map[string]interface{} `json:"params,omitempty"`
}

type signetJSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    string `json:"data,omitempty"`
	} `json:"error,omitempty"`
}

func (c *Client) callManagement(ctx context.Context, method string, params map[string]interface{}, out interface{}) error {
	bunkerPubkey, relayURLs, _, err := ParseBunkerURI(c.bunkerURI)
	if err != nil {
		return err
	}
	if len(relayURLs) == 0 {
		relayURLs = append([]string{}, c.relays...)
	}
	if len(relayURLs) == 0 {
		return fmt.Errorf("signet management relays are required")
	}
	bunkerPK, err := nostr.PubKeyFromHex(bunkerPubkey)
	if err != nil {
		return fmt.Errorf("decode Signet bunker pubkey: %w", err)
	}
	c.mu.Lock()
	bunker := c.bunker
	c.mu.Unlock()
	if bunker == nil {
		return ErrNotConnected
	}
	provisionerPK, err := bunker.GetPublicKey(ctx)
	if err != nil {
		return fmt.Errorf("get Signet provisioner pubkey: %w", err)
	}

	requestID := nostrutil.GeneratePrivateKeyHex()[:16]
	body, err := json.Marshal(signetJSONRPCRequest{
		JSONRPC: "2.0",
		ID:      requestID,
		Method:  method,
		Params:  params,
	})
	if err != nil {
		return fmt.Errorf("encode Signet management request: %w", err)
	}

	rumor := nostr.Event{
		// Signet carries ContextVM JSON inside a NIP-17 private direct
		// message, then NIP-59 gift-wraps that event. The outer subscription
		// includes kind 25910 for direct intents, but gift-wrapped intents must
		// use the NIP-17 inner kind or Signet's decoder rejects them.
		Kind:      nostr.KindDirectMessage,
		Content:   string(body),
		Tags:      nostr.Tags{nostr.Tag{"p", bunkerPK.Hex()}},
		CreatedAt: nostr.Now(),
		PubKey:    provisionerPK,
	}
	rumor.ID = rumor.GetID()
	rumorCiphertext, err := bunker.NIP44Encrypt(ctx, bunkerPK, rumor.String())
	if err != nil {
		return fmt.Errorf("encrypt Signet management rumor: %w", err)
	}
	seal := nostr.Event{
		Kind:      nostr.KindSeal,
		Content:   rumorCiphertext,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
	}
	if err := bunker.SignEvent(ctx, &seal); err != nil {
		return fmt.Errorf("sign Signet management seal: %w", err)
	}
	nonceKey := nostr.Generate()
	conversationKey, err := nip44.GenerateConversationKey(bunkerPK, nonceKey)
	if err != nil {
		return fmt.Errorf("derive Signet gift-wrap key: %w", err)
	}
	sealCiphertext, err := nip44.Encrypt(seal.String(), conversationKey)
	if err != nil {
		return fmt.Errorf("encrypt Signet management seal: %w", err)
	}
	gift := nostr.Event{
		Kind:      signetKindGiftWrap,
		Content:   sealCiphertext,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{nostr.Tag{"p", bunkerPK.Hex()}},
	}
	if err := gift.Sign(nonceKey); err != nil {
		return fmt.Errorf("sign Signet management gift-wrap: %w", err)
	}

	// Subscribe before publishing. Signet can answer immediately, and creating
	// the response subscription afterwards loses that reply on fast relays.
	responses := c.pool.SubscribeMany(ctx, relayURLs, nostr.Filter{
		Kinds: []nostr.Kind{signetKindGiftWrap},
		Tags:  nostr.TagMap{"p": []string{provisionerPK.Hex()}},
		// NIP-59 deliberately backdates gift-wrap timestamps (go-nostr uses a
		// window of up to ten hours). Correlation by the private JSON-RPC id
		// below makes a wider history window safe and prevents a freshly
		// published Signet response from being filtered out as "old".
		Since: nostr.Now() - 12*60*60,
	}, nostr.SubscriptionOptions{Label: "signet-mgmt"})

	// Publishing is transport, not request/response flow control. A relay OK is
	// useful telemetry, but waiting for one before consuming the already-live
	// response subscription can deadlock indefinitely on otherwise functional
	// pub/sub relays. Start the publish and drain acknowledgements independently;
	// the correlated Signet response below is the operation's completion signal.
	publishResults := c.pool.PublishMany(ctx, relayURLs, gift)
	go func() {
		for result := range publishResults {
			if result.Error != nil {
				c.logger.Warn("Signet management relay rejected publish",
					"relay", result.RelayURL,
					"error", result.Error,
				)
			}
		}
	}()

	for relayEvent := range responses {
		sealJSON, err := bunker.NIP44Decrypt(ctx, relayEvent.Event.PubKey, relayEvent.Event.Content)
		if err != nil {
			continue
		}
		var responseSeal nostr.Event
		if err := json.Unmarshal([]byte(sealJSON), &responseSeal); err != nil ||
			!responseSeal.VerifySignature() || responseSeal.PubKey != bunkerPK {
			continue
		}
		rumorJSON, err := bunker.NIP44Decrypt(ctx, responseSeal.PubKey, responseSeal.Content)
		if err != nil {
			continue
		}
		var rumor nostr.Event
		if err := json.Unmarshal([]byte(rumorJSON), &rumor); err != nil {
			continue
		}
		rumor.PubKey = responseSeal.PubKey
		var resp signetJSONRPCResponse
		if err := json.Unmarshal([]byte(rumor.Content), &resp); err != nil {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("Signet management error %d: %s", resp.Error.Code, resp.Error.Message)
		}
		if resp.ID != requestID {
			continue
		}
		if out == nil {
			return nil
		}
		if len(resp.Result) == 0 || string(resp.Result) == "null" {
			return nil
		}
		if err := json.Unmarshal(resp.Result, out); err != nil {
			return fmt.Errorf("parse Signet management result: %w", err)
		}
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("await Signet management response: %w", err)
	}
	return fmt.Errorf("await Signet management response: subscription closed")
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

	return encodeNostrAuthorizationEvent(event)
}

func encodeNostrAuthorizationEvent(event any) (string, error) {
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return "", fmt.Errorf("marshal Nostr authorization event: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(eventJSON)
	return "Nostr " + encoded, nil
}

// Close shuts down the client and all connections.
func (c *Client) Close() error {
	c.mu.Lock()

	// Close agent bunker connections
	for _, identity := range c.agents {
		if identity.bunkerClient != nil {
			// Note: BunkerClient doesn't have a Close method currently
		}
	}

	changed := c.connected || c.bunker != nil
	c.connected = false
	c.bunker = nil
	c.lifetime = nil
	if changed {
		close(c.stateChanged)
		c.stateChanged = make(chan struct{})
	}
	c.mu.Unlock()

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

	if err := nostrutil.SignEventWithHexKey(event, secretKey); err != nil {
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

func (c *Client) managementParamsForPubkey(pubkey string) map[string]interface{} {
	params := map[string]interface{}{"pubkey": pubkey}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, identity := range c.agents {
		if identity.Pubkey == pubkey {
			params["agent_id"] = identity.AgentID
			return params
		}
	}
	return params
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

func bunkerLogDetails(rawURI string) (string, []string) {
	pubkey, relays, _, err := ParseBunkerURI(rawURI)
	if err != nil {
		return "invalid", nil
	}
	hosts := make([]string, 0, len(relays))
	for _, relay := range relays {
		parsed, err := url.Parse(relay)
		if err != nil || parsed.Host == "" {
			continue
		}
		hosts = append(hosts, parsed.Host)
	}
	return redactPubkey(pubkey), hosts
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
