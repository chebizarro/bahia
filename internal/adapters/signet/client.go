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
	"fiatjaf.com/nostr/nip59"
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

	signetKindContextVM = cascadia.CAS_INTENT
	signetKindGiftWrap  = cascadia.NIP59_GIFT_WRAP
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

	mu        sync.Mutex
	bunker    *nip46.BunkerClient // Active NIP-46 connection
	agents    map[string]*AgentIdentity
	connected bool
	lifetime  context.Context
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
	ConnectTimeout  time.Duration // Deprecated: caller context controls connection lifetime
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
		if c.requireReal || !c.allowMock {
			return ErrNoBunkerConfigured
		}
		c.logger.Warn("no bunker URI configured, running in explicit dev/mock mode")
		c.connected = true
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

	c.bunker = bunker
	c.connected = true
	c.lifetime = ctx

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
	if method == "agent/provision" {
		return c.callNativeManagement(ctx, 8000, params, out)
	}

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
	clientSK, err := nostr.SecretKeyFromHex(c.clientSecretKey)
	if err != nil {
		return fmt.Errorf("decode Signet client secret key: %w", err)
	}
	clientPK := clientSK.Public()
	bunkerPK, err := nostr.PubKeyFromHex(bunkerPubkey)
	if err != nil {
		return fmt.Errorf("decode Signet bunker pubkey: %w", err)
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
		Kind:      signetKindContextVM,
		Content:   string(body),
		Tags:      nostr.Tags{nostr.Tag{"p", bunkerPK.Hex()}},
		CreatedAt: nostr.Now(),
		PubKey:    clientPK,
	}
	rumor.ID = rumor.GetID()
	gift, err := nip59.GiftWrap(
		rumor,
		bunkerPK,
		func(plaintext string) (string, error) {
			conversationKey, err := nip44.GenerateConversationKey(bunkerPK, clientSK)
			if err != nil {
				return "", err
			}
			return nip44.Encrypt(plaintext, conversationKey)
		},
		func(event *nostr.Event) error { return event.Sign(clientSK) },
		nil,
	)
	if err != nil {
		return fmt.Errorf("gift-wrap Signet management request: %w", err)
	}

	// Subscribe before publishing. Signet can answer immediately, and creating
	// the response subscription afterwards loses that reply on fast relays.
	responses := c.pool.SubscribeMany(ctx, relayURLs, nostr.Filter{
		Kinds: []nostr.Kind{signetKindGiftWrap},
		Tags:  nostr.TagMap{"p": []string{clientPK.Hex()}},
		// NIP-59 deliberately backdates gift-wrap timestamps (go-nostr uses a
		// window of up to ten hours). Correlation by the private JSON-RPC id
		// below makes a wider history window safe and prevents a freshly
		// published Signet response from being filtered out as "old".
		Since: nostr.Now() - 12*60*60,
	}, nostr.SubscriptionOptions{Label: "signet-mgmt"})

	publishCtx, cancelPublish := context.WithCancel(ctx)
	defer cancelPublish()
	published := 0
	var publishErr error
	for result := range c.pool.PublishMany(publishCtx, relayURLs, gift) {
		if result.Error != nil {
			publishErr = result.Error
			continue
		}
		published++
		cancelPublish()
		break
	}
	if published == 0 {
		if publishErr != nil {
			return fmt.Errorf("publish Signet management intent: %w", publishErr)
		}
		return fmt.Errorf("publish Signet management intent: no relay accepted event")
	}

	for relayEvent := range responses {
		rumor, err := nip59.GiftUnwrap(relayEvent.Event, func(otherPubkey nostr.PubKey, ciphertext string) (string, error) {
			conversationKey, err := nip44.GenerateConversationKey(otherPubkey, clientSK)
			if err != nil {
				return "", err
			}
			return nip44.Decrypt(ciphertext, conversationKey)
		})
		if err != nil || rumor.PubKey != bunkerPK {
			continue
		}
		var resp signetJSONRPCResponse
		if err := json.Unmarshal([]byte(rumor.Content), &resp); err != nil || resp.ID != requestID {
			continue
		}
		if resp.Error != nil {
			return fmt.Errorf("Signet management error %d: %s", resp.Error.Code, resp.Error.Message)
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

// callNativeManagement speaks Signet's relay-native management protocol:
// encrypted, provisioner-signed command events and encrypted kind-8090 acks.
// This is deliberately subscription-driven; the caller's lifecycle context,
// not an arbitrary operation deadline, controls cancellation.
func (c *Client) callNativeManagement(ctx context.Context, kind nostr.Kind, params map[string]interface{}, out interface{}) error {
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

	c.mu.Lock()
	bunker := c.bunker
	c.mu.Unlock()
	if bunker == nil {
		return ErrNotConnected
	}

	bunkerPK, err := nostr.PubKeyFromHex(bunkerPubkey)
	if err != nil {
		return fmt.Errorf("decode Signet bunker pubkey: %w", err)
	}
	provisionerPK, err := bunker.GetPublicKey(ctx)
	if err != nil {
		return fmt.Errorf("get Signet provisioner pubkey: %w", err)
	}

	requestID := nostrutil.GeneratePrivateKeyHex()[:16]
	body := make(map[string]interface{}, len(params)+1)
	for key, value := range params {
		body[key] = value
	}
	body["request_id"] = requestID
	plaintext, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("encode Signet management request: %w", err)
	}
	ciphertext, err := bunker.NIP44Encrypt(ctx, bunkerPK, string(plaintext))
	if err != nil {
		return fmt.Errorf("encrypt Signet management request: %w", err)
	}

	event := nostr.Event{
		Kind:      kind,
		Content:   ciphertext,
		Tags:      nostr.Tags{nostr.Tag{"p", bunkerPK.Hex()}},
		CreatedAt: nostr.Now(),
	}
	if err := bunker.SignEvent(ctx, &event); err != nil {
		return fmt.Errorf("sign Signet management request: %w", err)
	}

	responses := c.pool.SubscribeMany(ctx, relayURLs, nostr.Filter{
		Kinds: []nostr.Kind{8090},
		Tags:  nostr.TagMap{"p": []string{provisionerPK.Hex()}},
		Since: nostr.Now() - 60,
	}, nostr.SubscriptionOptions{Label: "signet-native-mgmt"})

	publishCtx, cancelPublish := context.WithCancel(ctx)
	defer cancelPublish()
	published := false
	var publishErr error
	for result := range c.pool.PublishMany(publishCtx, relayURLs, event) {
		if result.Error != nil {
			publishErr = result.Error
			continue
		}
		published = true
		cancelPublish()
		break
	}
	if !published {
		if publishErr != nil {
			return fmt.Errorf("publish Signet management command: %w", publishErr)
		}
		return fmt.Errorf("publish Signet management command: no relay accepted event")
	}

	for relayEvent := range responses {
		if relayEvent.Event.PubKey != bunkerPK {
			continue
		}
		decrypted, err := bunker.NIP44Decrypt(ctx, bunkerPK, relayEvent.Event.Content)
		if err != nil {
			continue
		}
		var ack struct {
			OK        bool            `json:"ok"`
			RequestID string          `json:"request_id"`
			Code      string          `json:"code"`
			Message   string          `json:"message"`
			Result    json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal([]byte(decrypted), &ack); err != nil || ack.RequestID != requestID {
			continue
		}
		if !ack.OK {
			return fmt.Errorf("Signet management error %s: %s", ack.Code, ack.Message)
		}
		if out != nil && len(ack.Result) > 0 && string(ack.Result) != "null" {
			if err := json.Unmarshal(ack.Result, out); err != nil {
				return fmt.Errorf("parse Signet management result: %w", err)
			}
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
