package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"fiatjaf.com/nostr"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"go.uber.org/zap"
)

// ContextVMRequestConfig configures a generic outbound ContextVM request client.
// Configure exactly one of Relays or Transport. A relay-backed client owns the
// pool it creates; an injected Transport remains owned by the caller.
type ContextVMRequestConfig struct {
	Relays          []string
	Transport       ContextVMRelayTransport
	Signer          nostr.Signer
	SenderPubkey    string
	RecipientPubkey string
	Encrypted       bool
	ResultTimeout   time.Duration
	ResultRetries   *int
}

// ContextVMRequestOption customizes optional request-client integrations.
type ContextVMRequestOption func(*contextVMRequestOptions)

type contextVMRequestOptions struct {
	logger *zap.Logger
}

// WithContextVMRequestLogger surfaces relay warnings through the supplied logger.
// The library default remains quiet.
func WithContextVMRequestLogger(logger *zap.Logger) ContextVMRequestOption {
	return func(options *contextVMRequestOptions) {
		if logger != nil {
			options.logger = logger
		}
	}
}

// ContextVMRequestClient publishes signed JSON-RPC 2.0 ContextVM requests and
// waits for correlated terminal responses.
type ContextVMRequestClient struct {
	relays            []string
	signer            nostr.Signer
	cipher            contextVMCipherSigner
	pubkey            string
	transport         ContextVMRelayTransport
	servicePubkey     string
	encrypted         bool
	resultTimeout     time.Duration
	resultRetries     int
	activationTimeout time.Duration
	ownsTransport     bool
}

// NewContextVMRequestClient constructs a generic ContextVM request client.
// When configured with Relays, Close closes the internally-created relay pool.
// When configured with Transport, Close does not close the injected transport.
func NewContextVMRequestClient(cfg ContextVMRequestConfig, clientOptions ...ContextVMRequestOption) (*ContextVMRequestClient, error) {
	options := contextVMRequestOptions{logger: zap.NewNop()}
	for _, apply := range clientOptions {
		if apply != nil {
			apply(&options)
		}
	}
	if cfg.Signer == nil {
		return nil, fmt.Errorf("ContextVM request signer is required")
	}
	senderPubkey := strings.TrimSpace(cfg.SenderPubkey)
	if len(senderPubkey) != 64 {
		return nil, fmt.Errorf("ContextVM sender pubkey must be a 64-character hex pubkey")
	}
	if _, err := nostr.PubKeyFromHex(senderPubkey); err != nil {
		return nil, fmt.Errorf("parse ContextVM sender pubkey: %w", err)
	}
	recipientPubkey := strings.TrimSpace(cfg.RecipientPubkey)
	if len(recipientPubkey) != 64 {
		return nil, fmt.Errorf("ContextVM recipient pubkey must be a 64-character hex pubkey")
	}
	if _, err := nostr.PubKeyFromHex(recipientPubkey); err != nil {
		return nil, fmt.Errorf("parse ContextVM recipient pubkey: %w", err)
	}
	var cipherSigner contextVMCipherSigner
	if cfg.Encrypted {
		var ok bool
		cipherSigner, ok = cfg.Signer.(contextVMCipherSigner)
		if !ok {
			return nil, fmt.Errorf("encrypted ContextVM requests require a signer with NIP-44 encrypt and decrypt support")
		}
	}
	relays := normalizeOperatorRelays(cfg.Relays)
	if cfg.Transport != nil && len(relays) > 0 {
		return nil, fmt.Errorf("configure either ContextVM relays or an injected transport, not both")
	}
	if cfg.Transport == nil && len(relays) == 0 {
		return nil, fmt.Errorf("ContextVM relays or an injected transport are required")
	}
	resultTimeout := cfg.ResultTimeout
	if resultTimeout == 0 {
		resultTimeout = DefaultOperatorResultTimeout
	}
	if resultTimeout < 0 {
		return nil, fmt.Errorf("ContextVM result timeout must be positive")
	}
	resultRetries := DefaultOperatorResultRetries
	if cfg.ResultRetries != nil {
		resultRetries = *cfg.ResultRetries
	}
	if resultRetries < 0 {
		return nil, fmt.Errorf("ContextVM result retries cannot be negative")
	}
	transport := cfg.Transport
	ownsTransport := false
	if transport == nil {
		pool := nostrpool.NewRelayPool(relays, options.logger, nostrpool.WithAuthSigner(cfg.Signer))
		transport = &relayPoolOperatorTransport{pool: pool}
		ownsTransport = true
	}
	return &ContextVMRequestClient{
		relays:            relays,
		signer:            cfg.Signer,
		cipher:            cipherSigner,
		pubkey:            senderPubkey,
		transport:         transport,
		servicePubkey:     recipientPubkey,
		encrypted:         cfg.Encrypted,
		resultTimeout:     resultTimeout,
		resultRetries:     resultRetries,
		activationTimeout: operatorActivationTimeout,
		ownsTransport:     ownsTransport,
	}, nil
}

// Close releases an internally-created relay pool. Injected transports are not
// closed because their lifecycle remains owned by the caller.
func (c *ContextVMRequestClient) Close() {
	if c != nil && c.ownsTransport && c.transport != nil {
		c.transport.Close()
	}
}

// Request publishes a signed ContextVM request and waits for its correlated
// terminal result. Progress results are delivered to onStatus.
func (c *ContextVMRequestClient) Request(ctx context.Context, method string, params any, tags nostr.Tags, onStatus func(OperatorStatusEvent)) (*nostr.Event, error) {
	if c == nil || c.transport == nil || c.signer == nil || c.pubkey == "" {
		return nil, &ControlPlaneRequestError{Phase: "configure operator control-plane client", RequestAccepted: false, Cause: fmt.Errorf("ContextVM request client is not configured")}
	}
	method = strings.TrimSpace(method)
	if method == "" {
		return nil, &ControlPlaneRequestError{Phase: "encode operator ContextVM request", RequestAccepted: false, Cause: fmt.Errorf("ContextVM method is required")}
	}
	if c.encrypted && (c.cipher == nil || c.servicePubkey == "") {
		return nil, &ControlPlaneRequestError{Phase: "configure encrypted operator control-plane client", RequestAccepted: false, Cause: fmt.Errorf("encrypted ContextVM requests require recipient pubkey and NIP-44 signer support")}
	}
	payloadContent, err := json.Marshal(params)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "encode operator ContextVM params", RequestAccepted: false, Cause: err}
	}
	tags = append(nostr.Tags(nil), tags...)
	requestID := firstTagValue(tags, "d")
	if requestID == "" {
		requestID = deterministicOperatorIdempotencyKey(method, tags, payloadContent)
		tags = append(nostr.Tags{{"d", requestID}}, tags...)
	}
	rpcParams, err := contextVMParams(params, requestID)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "encode operator ContextVM params", RequestAccepted: false, Cause: err}
	}
	tags = append(tags, nostr.Tag{"method", method}, nostr.Tag{controlplane.ContextVMRoutingTag, controlplane.ContextVMWireVersion})
	if c.servicePubkey != "" {
		tags = append(tags, nostr.Tag{"p", c.servicePubkey})
	}
	rpc := contextVMRPCRequest{JSONRPC: "2.0", ID: requestID, Method: method, Params: rpcParams}
	content, err := json.Marshal(rpc)
	if err != nil {
		return nil, &ControlPlaneRequestError{Phase: "encode operator ContextVM request", RequestAccepted: false, Cause: err}
	}
	inner := &nostr.Event{Kind: nostr.Kind(controlplane.KindContextVMMessage), CreatedAt: nostr.Now(), Tags: tags, Content: string(content)}
	if err := controlplane.SignGoNostrEvent(ctx, c.signer, inner); err != nil {
		return nil, &ControlPlaneRequestError{Phase: "sign operator ContextVM request", RequestAccepted: false, Cause: err}
	}

	attempts := c.resultRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	resultTimeout := c.resultTimeout
	if resultTimeout <= 0 {
		resultTimeout = DefaultOperatorResultTimeout
	}
	activationTimeout := c.activationTimeout
	if activationTimeout <= 0 {
		activationTimeout = operatorActivationTimeout
	}
	var (
		everAccepted    bool
		publishedRelays int
		publishResults  []OperatorPublishResult
		subscribed      []string
		failed          []string
		requestEventID  = inner.ID.Hex()
		outerRequestIDs []string
	)
	requestError := func(phase string, attempt int, cause error) *ControlPlaneRequestError {
		return &ControlPlaneRequestError{
			Phase:               phase,
			RequestAccepted:     everAccepted,
			PublishedRelays:     publishedRelays,
			ConfiguredRelays:    append([]string(nil), c.relays...),
			SubscribedRelays:    append([]string(nil), subscribed...),
			FailedSubscriptions: append([]string(nil), failed...),
			RequestEventID:      requestEventID,
			RequestDTag:         requestID,
			RequestMethod:       method,
			AttemptsMade:        attempt,
			PublishResults:      append([]OperatorPublishResult(nil), publishResults...),
			Cause:               cause,
		}
	}

	for attempt := 1; attempt <= attempts; attempt++ {
		publishEvent, filters, attemptOuterIDs, prepareErr := c.prepareOperatorAttempt(ctx, inner, outerRequestIDs)
		if prepareErr != nil {
			phase := "prepare operator ContextVM request"
			if c.encrypted {
				phase = "wrap encrypted operator ContextVM request"
			}
			return nil, requestError(phase, attempt, prepareErr)
		}
		outerRequestIDs = attemptOuterIDs
		requestEventID = publishEvent.ID.Hex()
		sub, subErr := c.transport.SubscribeOperator(ctx, filters)
		if subErr != nil {
			subscribed = nil
			failed = append([]string(nil), c.relays...)
			return nil, requestError("subscribe for operator ContextVM replies", attempt, subErr)
		}
		subscribed = sub.RelayURLs()
		failed = operatorFailedSubscriptions(c.relays, subscribed)
		if len(subscribed) == 0 {
			sub.Close()
			return nil, requestError("subscribe for operator ContextVM replies", attempt, fmt.Errorf("no configured relay established a reply subscription"))
		}
		activatedSub, activationErr := c.waitForOperatorSubscriptionActivation(ctx, sub, filters, activationTimeout)
		if activationErr != nil {
			activatedSub.Close()
			return nil, requestError("activate operator ContextVM reply subscription", attempt, activationErr)
		}
		sub = activatedSub
		subscribed = sub.RelayURLs()
		failed = operatorFailedSubscriptions(c.relays, subscribed)

		published, attemptResults, publishErr := c.publishOperatorEvent(ctx, *publishEvent)
		publishedRelays = published
		publishResults = append(publishResults, attemptResults...)
		if published == 0 {
			sub.Close()
			if publishErr == nil {
				publishErr = fmt.Errorf("request was not accepted by any relay")
			}
			return nil, requestError("publish operator ContextVM request", attempt, publishErr)
		}
		everAccepted = true

		attemptCtx, cancelAttempt := context.WithTimeout(ctx, resultTimeout)
		result, awaitErr := c.awaitOperatorResult(attemptCtx, sub, filters, inner, outerRequestIDs, requestID, onStatus)
		cancelAttempt()
		sub.Close()
		if awaitErr == nil {
			return result, nil
		}
		if errors.Is(awaitErr, context.DeadlineExceeded) && ctx.Err() == nil && attempt < attempts {
			continue
		}
		return nil, requestError("await operator ContextVM result", attempt, awaitErr)
	}
	return nil, requestError("await operator ContextVM result", attempts, fmt.Errorf("result retry attempts exhausted"))
}
