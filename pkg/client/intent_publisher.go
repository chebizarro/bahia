package client

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip59"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
)

const (
	// DefaultIntentResultTimeout is the default time to wait for a 30315
	// intent-status event from the daemon.
	DefaultIntentResultTimeout = 30 * time.Second

	// ExitCodeAccepted indicates the intent was accepted by the daemon.
	ExitCodeAccepted = 0
	// ExitCodeRejected indicates the intent was rejected, conflicted, or superseded.
	ExitCodeRejected = 1
	// ExitCodeTimeout indicates no 30315 status was received within the timeout.
	ExitCodeTimeout = 2
	// ExitCodeNoRelay indicates no relay accepted the published event.
	ExitCodeNoRelay = 3
)

// IntentPublisherConfig configures the IntentPublisher.
type IntentPublisherConfig struct {
	// Relays is the set of relay URLs to publish to and subscribe on.
	Relays []string
	// Signer signs the inner 30900 intent event (nsec or NIP-46).
	Signer nostr.Signer
	// PrivateKey is the operator's 64-character hex private key (alternative to Signer).
	// If both Signer and PrivateKey are set, an error is returned.
	PrivateKey string
	// Pubkey is the operator's 64-character hex public key. Required when Signer is set.
	Pubkey string
	// ServicePubkey is the daemon's 64-character hex public key. Required for
	// status subscription and gift-wrapping sensitive domains.
	ServicePubkey string
	// ResultTimeout is how long to wait for a 30315 status event.
	// Defaults to DefaultIntentResultTimeout.
	ResultTimeout time.Duration
	// SensitiveDomains lists domains that require gift-wrapped delivery (NIP-59).
	// Defaults to ["org", "secret", "notification"] if empty.
	SensitiveDomains []string
	// CloseSigner is called when the publisher is closed.
	CloseSigner func() error
	// Transport overrides the default relay pool transport (for testing).
	Transport ContextVMRelayTransport
}

// IntentPublisher builds, signs, publishes, and tracks 30900 intent events.
// It waits for the daemon's bounded 30315 intent-status and maps the outcome
// to exit codes per Phase 5 §2.2.
type IntentPublisher struct {
	signer           nostr.Signer
	cipher           contextVMCipherSigner // non-nil when signer supports NIP-44
	pubkey           string
	servicePubkey    string
	transport        ContextVMRelayTransport
	relays           []string
	resultTimeout    time.Duration
	sensitiveDomains map[string]bool
	closeSigner      func() error
	ownsTransport    bool // true if we created the transport
}

// NewIntentPublisher creates an IntentPublisher from the given config.
func NewIntentPublisher(cfg IntentPublisherConfig) (*IntentPublisher, error) {
	privateKey := strings.TrimSpace(cfg.PrivateKey)
	signer := cfg.Signer
	var cipherSigner contextVMCipherSigner
	pubkey := strings.TrimSpace(cfg.Pubkey)
	var poolOptions []nostrpool.RelayPoolOption

	if signer != nil {
		if privateKey != "" {
			return nil, fmt.Errorf("configure either a signer or a private key, not both")
		}
		if len(pubkey) != 64 {
			return nil, fmt.Errorf("signer pubkey must be a 64-character hex pubkey")
		}
		// Check if signer supports NIP-44 (for gift-wrapping).
		cipherSigner, _ = signer.(contextVMCipherSigner)
		poolOptions = append(poolOptions, nostrpool.WithAuthSigner(signer))
	} else {
		var err error
		privateKey, err = NormalizeNostrPrivateKey(privateKey)
		if err != nil {
			return nil, err
		}
		secret, err := nostr.SecretKeyFromHex(privateKey)
		if err != nil {
			return nil, fmt.Errorf("parse Nostr private key: %w", err)
		}
		localKeyer := keyer.NewPlainKeySigner(secret)
		signer = localKeyer
		cipherSigner = localKeyer
		pubkey = secret.Public().Hex()
		poolOptions = append(poolOptions, nostrpool.WithPrivateKey(privateKey))
	}

	relays := normalizeOperatorRelays(cfg.Relays)
	if len(relays) == 0 {
		return nil, fmt.Errorf("at least one relay is required")
	}

	servicePubkey := strings.TrimSpace(cfg.ServicePubkey)
	if servicePubkey == "" || len(servicePubkey) != 64 {
		return nil, fmt.Errorf("service pubkey must be a 64-character hex pubkey")
	}

	resultTimeout := cfg.ResultTimeout
	if resultTimeout == 0 {
		resultTimeout = DefaultIntentResultTimeout
	}
	if resultTimeout < 0 {
		return nil, fmt.Errorf("result timeout must be positive")
	}

	// Default sensitive domains.
	sensitiveDomains := make(map[string]bool)
	domains := cfg.SensitiveDomains
	if len(domains) == 0 {
		domains = []string{"org", "secret", "notification"}
	}
	for _, d := range domains {
		sensitiveDomains[strings.ToLower(d)] = true
	}

	transport := cfg.Transport
	ownsTransport := false
	if transport == nil {
		pool := nostrpool.NewRelayPool(relays, nil, poolOptions...)
		transport = &relayPoolOperatorTransport{pool: pool}
		ownsTransport = true
	}

	return &IntentPublisher{
		signer:           signer,
		cipher:           cipherSigner,
		pubkey:           pubkey,
		servicePubkey:    servicePubkey,
		transport:        transport,
		relays:           relays,
		resultTimeout:    resultTimeout,
		sensitiveDomains: sensitiveDomains,
		closeSigner:      cfg.CloseSigner,
		ownsTransport:    ownsTransport,
	}, nil
}

// Close releases relay resources owned by the publisher.
func (p *IntentPublisher) Close() {
	if p.ownsTransport && p.transport != nil {
		p.transport.Close()
	}
	if p.closeSigner != nil {
		_ = p.closeSigner()
	}
}

// PublishIntentRequest describes the intent to publish.
type PublishIntentRequest struct {
	// Domain is the domain family (e.g. "service", "environment", "secret").
	Domain string
	// Op is the advisory operation: "create", "update", or "delete".
	Op string
	// Coordinate is the d-tag value (entity coordinate).
	Coordinate string
	// Schema is the intent schema (e.g. "bahia.intent.service.v1").
	Schema string
	// OrgID is the org UUID.
	OrgID string
	// Content is the full desired state as a JSON-serializable map.
	Content map[string]interface{}
	// IntentID is the idempotency key (UUIDv7). Reuse for retries.
	IntentID string
	// ExpectedUpdatedAt is the optional revision-check timestamp for updates.
	ExpectedUpdatedAt *time.Time
}

// PreparedIntent is a signed (and optionally gift-wrapped) intent ready for
// publishing. Reuse the same PreparedIntent for idempotent retries.
type PreparedIntent struct {
	// Event is the event to publish (30900 or 1059 gift-wrap).
	Event nostr.Event
	// InnerEvent is the signed 30900 event (same as Event for non-sensitive
	// domains; the unwrapped inner for gift-wrapped intents).
	InnerEvent nostr.Event
	// IntentID is the idempotency key.
	IntentID string
	// EventID is the inner event's hex ID.
	EventID string
	// Domain is the intent's domain family.
	Domain string
}

// PublishIntentResult is the outcome of publishing an intent.
type PublishIntentResult struct {
	// IntentID is the idempotency key.
	IntentID string
	// EventID is the published event's hex ID.
	EventID string
	// Status is the daemon's response: "accepted", "rejected", "conflict",
	// "superseded", or "timeout".
	Status string
	// Reason is the rejection/conflict reason (empty on success or timeout).
	Reason string
	// ExitCode maps the status to a CLI exit code.
	ExitCode int
	// PublishResults are the per-relay publish outcomes.
	PublishResults []OperatorPublishResult
}

// BuildIntentEvent constructs the inner kind-30900 intent event exactly as
// the daemon's ParseIntent expects. The event is unsigned; call PrepareIntent
// to sign (and optionally gift-wrap) it.
func (p *IntentPublisher) BuildIntentEvent(req PublishIntentRequest) (nostr.Event, error) {
	if req.Domain == "" {
		return nostr.Event{}, fmt.Errorf("domain is required")
	}
	if req.Coordinate == "" {
		return nostr.Event{}, fmt.Errorf("coordinate (d-tag) is required")
	}
	if req.IntentID == "" {
		return nostr.Event{}, fmt.Errorf("intent_id is required")
	}
	if req.OrgID == "" {
		return nostr.Event{}, fmt.Errorf("org_id is required")
	}
	if req.Op == "" {
		req.Op = "update"
	}
	if req.Schema == "" {
		req.Schema = "bahia.intent." + req.Domain + ".v1"
	}

	// Build content.
	content := req.Content
	if content == nil {
		content = map[string]interface{}{}
	}
	if req.ExpectedUpdatedAt != nil {
		content["expected_updated_at"] = req.ExpectedUpdatedAt.Format(time.RFC3339Nano)
	}
	if raw, ok := content["expected_updated_at"]; ok {
		revision, ok := raw.(string)
		if !ok {
			return nostr.Event{}, fmt.Errorf("invalid expected_updated_at: must be an RFC3339 timestamp string")
		}
		if _, err := time.Parse(time.RFC3339Nano, revision); err != nil {
			return nostr.Event{}, fmt.Errorf("invalid expected_updated_at: %w", err)
		}
	}
	contentJSON, err := json.Marshal(content)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("marshal intent content: %w", err)
	}

	// Topic tag: the domain with underscores replaced by hyphens.
	topicTag := req.Domain
	if strings.Contains(topicTag, "_") {
		topicTag = strings.ReplaceAll(topicTag, "_", "-")
	}

	ev := nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", req.Coordinate},
			{"domain", req.Domain},
			{"schema", req.Schema},
			{"t", "bahia-intent"},
			{"t", topicTag},
			{"op", req.Op},
			{"org", req.OrgID},
			{"intent_id", req.IntentID},
		},
		Content: string(contentJSON),
	}

	return ev, nil
}

// PrepareIntent builds, signs, and optionally gift-wraps an intent event.
// The returned PreparedIntent can be passed to PublishAndWait, and reused
// for idempotent retries.
func (p *IntentPublisher) PrepareIntent(ctx context.Context, req PublishIntentRequest) (*PreparedIntent, error) {
	ev, err := p.BuildIntentEvent(req)
	if err != nil {
		return nil, err
	}

	// Sign the inner 30900 event.
	if err := p.signer.SignEvent(ctx, &ev); err != nil {
		return nil, fmt.Errorf("sign intent event: %w", err)
	}

	innerEvent := ev
	publishEvent := ev

	// Gift-wrap for sensitive domains.
	if p.sensitiveDomains[strings.ToLower(req.Domain)] {
		if p.cipher == nil {
			return nil, fmt.Errorf("domain %q requires gift-wrapped delivery but signer does not support NIP-44 encrypt/decrypt", req.Domain)
		}

		recipientPK, err := nostr.PubKeyFromHex(p.servicePubkey)
		if err != nil {
			return nil, fmt.Errorf("parse service pubkey for gift-wrap: %w", err)
		}

		wrapped, err := nip59.GiftWrap(
			ev,
			recipientPK,
			func(plaintext string) (string, error) {
				return p.cipher.Encrypt(ctx, plaintext, recipientPK)
			},
			func(sealEvent *nostr.Event) error {
				return p.signer.SignEvent(ctx, sealEvent)
			},
			nil, // no modify function
		)
		if err != nil {
			return nil, fmt.Errorf("gift-wrap intent: %w", err)
		}
		publishEvent = wrapped
	}

	return &PreparedIntent{
		Event:      publishEvent,
		InnerEvent: innerEvent,
		IntentID:   req.IntentID,
		EventID:    innerEvent.ID.Hex(),
		Domain:     req.Domain,
	}, nil
}

// PublishAndWait publishes the prepared intent and waits for the daemon's
// bounded 30315 intent-status event. Returns the result with the appropriate
// exit code.
func (p *IntentPublisher) PublishAndWait(ctx context.Context, prepared *PreparedIntent) (*PublishIntentResult, error) {
	if prepared == nil {
		return nil, fmt.Errorf("nil prepared intent")
	}

	// Subscribe for 30315 status BEFORE publishing to avoid a race.
	statusFilter := nostr.Filter{
		Kinds:   []nostr.Kind{30315},
		Authors: []nostr.PubKey{nostr.MustPubKeyFromHex(p.servicePubkey)},
		Tags: nostr.TagMap{
			"p":         {p.pubkey},
			"intent_id": {prepared.IntentID},
		},
	}

	subCtx, subCancel := context.WithCancel(ctx)
	defer subCancel()
	sub, err := p.transport.SubscribeOperator(subCtx, []nostr.Filter{statusFilter})
	if err != nil {
		return nil, fmt.Errorf("subscribe for intent status: %w", err)
	}
	defer sub.Close()

	// Publish.
	publishResults, publishErr := p.transport.PublishWithResults(ctx, prepared.Event)

	// Map per-relay results.
	var opResults []OperatorPublishResult
	accepted := 0
	for _, pr := range publishResults {
		r := OperatorPublishResult{
			RelayURL: pr.RelayURL,
			Accepted: pr.Accepted,
			Reason:   pr.Reason,
		}
		if pr.Error != nil {
			r.Error = pr.Error.Error()
		}
		opResults = append(opResults, r)
		if pr.Accepted {
			accepted++
		}
	}

	if accepted == 0 {
		errMsg := "no relay accepted the event"
		if publishErr != nil {
			errMsg = publishErr.Error()
		}
		return &PublishIntentResult{
			IntentID:       prepared.IntentID,
			EventID:        prepared.EventID,
			Status:         "no_relay",
			Reason:         errMsg,
			ExitCode:       ExitCodeNoRelay,
			PublishResults: opResults,
		}, nil
	}
	statusCtx, statusCancel := context.WithTimeout(ctx, p.resultTimeout)
	defer statusCancel()

	// Wait for 30315 status.
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				// Channel closed without a status event — treat as timeout.
				return &PublishIntentResult{
					IntentID:       prepared.IntentID,
					EventID:        prepared.EventID,
					Status:         "timeout",
					ExitCode:       ExitCodeTimeout,
					PublishResults: opResults,
				}, nil
			}
			if ev == nil {
				continue
			}
			return p.parseStatusEvent(ev, prepared, opResults), nil

		case <-statusCtx.Done():
			return &PublishIntentResult{
				IntentID:       prepared.IntentID,
				EventID:        prepared.EventID,
				Status:         "timeout",
				ExitCode:       ExitCodeTimeout,
				PublishResults: opResults,
			}, nil
		}
	}
}

// parseStatusEvent extracts the status from a 30315 event.
func (p *IntentPublisher) parseStatusEvent(ev *nostr.Event, prepared *PreparedIntent, publishResults []OperatorPublishResult) *PublishIntentResult {
	result := &PublishIntentResult{
		IntentID:       prepared.IntentID,
		EventID:        prepared.EventID,
		PublishResults: publishResults,
	}

	// Extract status from tags.
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "status" {
			result.Status = tag[1]
			break
		}
	}

	// Parse content for reason.
	if ev.Content != "" {
		var content map[string]interface{}
		if err := json.Unmarshal([]byte(ev.Content), &content); err == nil {
			if reason, ok := content["reason"].(string); ok {
				result.Reason = reason
			}
			if r, ok := content["result"].(string); ok && result.Status == "" {
				result.Status = r
			}
		}
	}

	// Map status to exit code.
	switch result.Status {
	case "accepted":
		result.ExitCode = ExitCodeAccepted
	case "rejected", "conflict", "superseded":
		result.ExitCode = ExitCodeRejected
	default:
		result.ExitCode = ExitCodeRejected // unknown status treated as rejection
	}

	return result
}

// IsSensitiveDomain reports whether a domain requires gift-wrapped delivery.
func (p *IntentPublisher) IsSensitiveDomain(domain string) bool {
	return p.sensitiveDomains[strings.ToLower(domain)]
}
