package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"fiatjaf.com/nostr"
	canonicalnostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/nip44"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"go.uber.org/zap"
)

const (
	// Standard Nostr tag keys shared by encrypted/ContextVM response producers
	// and the recipient-scoped subscription filter + routing gates, so the
	// producer envelope and the consumer filter cannot drift.
	tagReplyEvent      = "e" // references the originating request event id
	tagRecipientPubkey = "p" // recipient (service) pubkey the reply is addressed to

	EncryptedRequestRoutingTag  = "encrypted"
	EncryptedRequestWireVersion = "bahia-encrypted-v1"
	ContextVMRoutingTag         = "contextvm"
	// ContextVMWireVersion is the historical Nostr routing discriminator. It
	// remains v1 for compatibility; discovery control_plane.wire_version carries
	// the JSON-RPC payload/ack contract version.
	ContextVMWireVersion                = "contextvm-jsonrpc-v1"
	ContextVMProgressAckCapability      = "encrypted_controlplane.progress_ack"
	ContextVMProgressAckWireVersion     = "contextvm-jsonrpc-v2"
	ContextVMProgressNotificationMethod = "notifications/progress"
	ContextVMProgressStatusProcessing   = "processing"
	KindContextVMMessage                = kinds.ContextVMMessage
	KindContextVMGiftWrap               = kinds.ContextVMGiftWrap
	KindContextVMEphemeralWrap          = kinds.ContextVMEphemeralGiftWrap
	// Deprecated compatibility aliases retained for callers/tests that still name
	// the old encrypted transport API. They resolve to canonical ContextVM kinds;
	// production subscriptions do not accept legacy Bahia encrypted events.
	KindEncryptedRequest = KindContextVMGiftWrap
	KindEncryptedResult  = KindContextVMMessage

	ContextVMMethodServiceCreate              = "service/create"
	ContextVMMethodServiceDeploy              = "service/deploy"
	ContextVMMethodPolicyCreate               = "policy/create"
	ContextVMMethodPolicyUpdate               = "policy/update"
	ContextVMMethodPolicyDelete               = "policy/delete"
	ContextVMMethodWorkerCleanup              = "worker/cleanup"
	ContextVMMethodWorkerCordon               = "worker/cordon"
	ContextVMMethodWorkerUncordon             = "worker/uncordon"
	ContextVMMethodWorkerDrain                = "worker/drain"
	ContextVMMethodWorkerUndrain              = "worker/undrain"
	ContextVMMethodWorkerMaintenanceEnter     = "worker/maintenance-enter"
	ContextVMMethodWorkerMaintenanceExit      = "worker/maintenance-exit"
	ContextVMMethodWorkerLabelsUpdate         = "worker/labels-update"
	ContextVMMethodPackagePromote             = "package/promote"
	ContextVMMethodDNSZoneCreate              = "dns/zone-create"
	ContextVMMethodDNSPolicyApply             = "dns/policy-apply"
	ContextVMMethodDNSRecordSet               = "dns/record-set"
	ContextVMMethodDNSDriftRemediate          = "dns/drift-remediate"
	ContextVMMethodBackupRepositoryRegister   = "backup/repository-register"
	ContextVMMethodBackupPolicyApply          = "backup/policy-apply"
	ContextVMMethodBackupRecipeApply          = "backup/recipe-apply"
	ContextVMMethodBackupDefinitionApply      = "backup/definition-apply"
	ContextVMMethodBackupRun                  = "backup/run"
	ContextVMMethodBackupVerification         = "backup/verification"
	ContextVMMethodBackupRestore              = "backup/restore"
	ContextVMMethodBackupRetention            = "backup/retention"
	ContextVMMethodBackupRepositoryProbe      = "backup/repository-probe"
	ContextVMMethodBackupRestoreApprovalAlias = "approval/backup-restore-approve"
	ContextVMMethodMLRecipeRun                = "ml/recipe-run"
	ContextVMMethodLoomSubmit                 = "loom/submit"
	ContextVMMethodLoomCancel                 = "loom/cancel"
	ContextVMLoomSchema                       = "cascadia.loom.v1"
	ContextVMMethodApprovalApprove            = "approval/approve"
	ContextVMMethodToolsCall                  = "tools/call"

	encryptedRequestReplayLookback = 2 * time.Minute
)

// EncryptedRequestSubscriber is the relay subscription contract used by the
// encrypted request/result event runtime. RelayPool satisfies this interface.
type EncryptedRequestSubscriber interface {
	SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*nostrpool.MergedSubscription, error)
	AuthenticateRelay(ctx context.Context, relayURL string) error
}

// EncryptedRequestEnvelope is the deprecated encrypted request payload shape.
type EncryptedRequestEnvelope struct {
	Version         string          `json:"version"`
	Operation       string          `json:"operation"`
	RequesterPubkey string          `json:"requester_pubkey"`
	Payload         json.RawMessage `json:"payload"`
}

// EncryptedResultEnvelope is the deprecated encrypted result payload shape.
type EncryptedResultEnvelope struct {
	Version        string       `json:"version"`
	RequestEventID string       `json:"request_event_id"`
	Status         string       `json:"status"`
	Payload        any          `json:"payload,omitempty"`
	Error          *ResultError `json:"error,omitempty"`
}

// ResultError is the encrypted terminal error shape for encrypted requests.
type ResultError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type ContextVMJSONRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type ContextVMJSONRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *JSONRPCError   `json:"error,omitempty"`
}

type ContextVMJSONRPCNotification struct {
	JSONRPC string `json:"jsonrpc"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

type ContextVMProgressParams struct {
	RequestID string `json:"requestId"`
	Status    string `json:"status"`
}

type JSONRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type ContextVMRequest struct {
	Event         *nostr.Event
	OuterEvent    *nostr.Event
	RPC           ContextVMJSONRPCRequest
	ProgressToken string
}

type ContextVMHandler func(ctx context.Context, request ContextVMRequest) (any, error)

// EncryptedRequest is passed to operation handlers after auth and decryption.
type EncryptedRequest struct {
	Event    *nostr.Event
	Envelope EncryptedRequestEnvelope
}

// EncryptedRequestHandler handles a decrypted encrypted request.
type EncryptedRequestHandler func(ctx context.Context, request EncryptedRequest) (any, error)

// EncryptedResponder decrypts incoming encrypted requests and publishes encrypted
// correlated results back to the requester.
type EncryptedResponder struct {
	publisher     NostrEventPublisher
	signer        canonicalnostr.Signer
	privateKey    string
	servicePubkey string
	logger        *zap.Logger
}

func NewEncryptedResponder(publisher NostrEventPublisher, signer canonicalnostr.Signer, privateKeyHex string, logger *zap.Logger) *EncryptedResponder {
	if logger == nil {
		logger = zap.NewNop()
	}
	privateKeyHex = strings.TrimSpace(privateKeyHex)
	servicePubkey := ""
	if privateKeyHex != "" {
		if secret, err := nostr.SecretKeyFromHex(privateKeyHex); err == nil {
			servicePubkey = secret.Public().Hex()
		}
	}
	return &EncryptedResponder{publisher: publisher, signer: signer, privateKey: privateKeyHex, servicePubkey: servicePubkey, logger: logger.Named("encrypted-responder")}
}

func (r *EncryptedResponder) ServicePubkey() string {
	if r == nil {
		return ""
	}
	return r.servicePubkey
}

func (r *EncryptedResponder) DecryptRequestContent(event *nostr.Event) ([]byte, error) {
	if event == nil {
		return nil, fmt.Errorf("request event is nil")
	}
	if event.PubKey == (nostr.PubKey{}) {
		return nil, fmt.Errorf("request event pubkey is required")
	}
	if strings.TrimSpace(event.Content) == "" {
		return nil, fmt.Errorf("request event content is empty")
	}
	conversationKey, err := r.conversationKey(event.PubKey.Hex())
	if err != nil {
		return nil, err
	}
	plaintext, err := nip44.Decrypt(event.Content, conversationKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt encrypted request content: %w", err)
	}
	return []byte(plaintext), nil
}

func (r *EncryptedResponder) PublishEncryptedResult(ctx context.Context, requestEvent *nostr.Event, status string, payload any, resultErr *ResultError) error {
	if requestEvent == nil {
		return fmt.Errorf("request event is nil")
	}
	if requestEvent.ID == (nostr.ID{}) || requestEvent.PubKey == (nostr.PubKey{}) {
		return fmt.Errorf("request event id and pubkey are required")
	}
	if status == "" {
		status = "ok"
	}
	envelope := EncryptedResultEnvelope{
		Version:        EncryptedRequestWireVersion,
		RequestEventID: requestEvent.ID.Hex(),
		Status:         status,
		Payload:        payload,
		Error:          resultErr,
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal encrypted result envelope: %w", err)
	}
	conversationKey, err := r.conversationKey(requestEvent.PubKey.Hex())
	if err != nil {
		return err
	}
	ciphertext, err := nip44.Encrypt(string(content), conversationKey)
	if err != nil {
		return fmt.Errorf("encrypt encrypted result content: %w", err)
	}
	event := &nostr.Event{
		Kind:      KindEncryptedResult,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{tagReplyEvent, requestEvent.ID.Hex(), "", "reply"},
			{tagRecipientPubkey, requestEvent.PubKey.Hex()},
			{EncryptedRequestRoutingTag, EncryptedRequestWireVersion},
			{"status", status},
		},
		Content: ciphertext,
	}
	if err := SignGoNostrEvent(ctx, r.signer, event); err != nil {
		return fmt.Errorf("sign encrypted result event: %w", err)
	}
	published, err := r.publisher.Publish(ctx, *event)
	if err != nil {
		return fmt.Errorf("publish encrypted result event: %w", err)
	}
	if published == 0 {
		return fmt.Errorf("publish encrypted result event: no relay accepted event")
	}
	return nil
}

func (r *EncryptedResponder) conversationKey(counterpartyPubkey string) ([32]byte, error) {
	if r == nil || strings.TrimSpace(r.privateKey) == "" {
		return [32]byte{}, fmt.Errorf("encrypted request NIP-44 key is not configured")
	}
	if counterpartyPubkey == "" {
		return [32]byte{}, fmt.Errorf("counterparty pubkey is required")
	}
	counterparty, err := nostr.PubKeyFromHex(counterpartyPubkey)
	if err != nil {
		return [32]byte{}, fmt.Errorf("decode counterparty pubkey: %w", err)
	}
	secret, err := nostr.SecretKeyFromHex(r.privateKey)
	if err != nil {
		return [32]byte{}, fmt.Errorf("decode encrypted request private key: %w", err)
	}
	conversationKey, err := nip44.GenerateConversationKey(counterparty, secret)
	if err != nil {
		return [32]byte{}, fmt.Errorf("generate encrypted request conversation key: %w", err)
	}
	return conversationKey, nil
}

// contextVMDedupDefaultLimit is the default maximum number of cached ContextVM
// idempotency responses. When the limit is reached, the oldest entry is evicted.
const contextVMDedupDefaultLimit = 4096

// contextVMDedupCache is a bounded LRU cache for ContextVM command idempotency.
// It evicts the oldest entry when the configured limit is reached.
type contextVMDedupCache struct {
	entries map[string]ContextVMJSONRPCResponse
	order   []string // insertion order for LRU eviction
	limit   int
}

func newContextVMDedupCache(limit int) *contextVMDedupCache {
	if limit <= 0 {
		limit = contextVMDedupDefaultLimit
	}
	return &contextVMDedupCache{
		entries: make(map[string]ContextVMJSONRPCResponse, limit),
		order:   make([]string, 0, limit),
		limit:   limit,
	}
}

func (c *contextVMDedupCache) get(key string) (ContextVMJSONRPCResponse, bool) {
	resp, ok := c.entries[key]
	return resp, ok
}

func (c *contextVMDedupCache) put(key string, response ContextVMJSONRPCResponse) {
	if _, exists := c.entries[key]; exists {
		c.entries[key] = response
		return
	}
	if len(c.order) >= c.limit {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.entries[key] = response
	c.order = append(c.order, key)
}

func (c *contextVMDedupCache) len() int {
	return len(c.entries)
}

// EncryptedRequestTransport subscribes to encrypted request events and dispatches
// them to operation handlers. It never publishes sensitive payloads as public
// sidecar projections.
type EncryptedRequestTransport struct {
	subscriber        EncryptedRequestSubscriber
	responder         *EncryptedResponder
	authorizedPubkeys []string
	handlers          map[string]EncryptedRequestHandler
	contextVMHandlers map[string]ContextVMHandler
	contextVMDedup    *contextVMDedupCache
	contextVMMu       sync.Mutex
	dedup             *nostrpool.EventDeduplicator
	logger            *zap.Logger
}

func NewEncryptedRequestTransport(subscriber EncryptedRequestSubscriber, responder *EncryptedResponder, authorizedPubkeys []string, logger *zap.Logger) *EncryptedRequestTransport {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &EncryptedRequestTransport{
		subscriber:        subscriber,
		responder:         responder,
		authorizedPubkeys: append([]string(nil), authorizedPubkeys...),
		handlers:          make(map[string]EncryptedRequestHandler),
		contextVMHandlers: make(map[string]ContextVMHandler),
		contextVMDedup:    newContextVMDedupCache(contextVMDedupDefaultLimit),
		dedup:             nostrpool.NewEventDeduplicator(10000),
		logger:            logger.Named("encrypted-request-result-events"),
	}
}

func (t *EncryptedRequestTransport) RegisterHandler(operation string, handler EncryptedRequestHandler) {
	operation = strings.TrimSpace(operation)
	if operation == "" || handler == nil {
		return
	}
	t.handlers[operation] = handler
}

func (t *EncryptedRequestTransport) RegisterContextVMHandler(method string, handler ContextVMHandler) {
	method = strings.TrimSpace(method)
	if method == "" || handler == nil {
		return
	}
	t.contextVMHandlers[method] = handler
}

func (t *EncryptedRequestTransport) Run(ctx context.Context) error {
	if t.subscriber == nil {
		return fmt.Errorf("encrypted request subscriber is not configured")
	}
	since := nostr.Timestamp(time.Now().Add(-encryptedRequestReplayLookback).Unix())
	filter := nostr.Filter{Kinds: []nostr.Kind{KindContextVMMessage, KindContextVMGiftWrap, KindContextVMEphemeralWrap}, Since: since}
	if servicePubkey := t.responder.ServicePubkey(); servicePubkey != "" {
		filter.Tags = nostr.TagMap{tagRecipientPubkey: []string{servicePubkey}}
	}
	t.logger.Info("subscribing to ContextVM encrypted request events",
		zap.Any("kinds", filter.Kinds),
		zap.Time("since", time.Unix(int64(since), 0).UTC()),
		zap.Duration("lookback", encryptedRequestReplayLookback),
		zap.Strings("p_tags", filter.Tags["p"]),
	)
	subscribe := func() (*nostrpool.MergedSubscription, error) {
		return t.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	}
	merged, err := subscribe()
	if err != nil {
		return fmt.Errorf("subscribe to encrypted request/result events: %w", err)
	}
	defer func() { merged.Close() }()
	authAttempted := make(map[string]struct{})
	t.logger.Info("subscribed to ContextVM encrypted request events")
	for {
		select {
		case <-ctx.Done():
			t.logger.Info("ContextVM encrypted request transport shutting down")
			return ctx.Err()
		case eose, ok := <-merged.RelayEOSE:
			if ok {
				t.logger.Debug("relay sent ContextVM encrypted request EOSE", zap.String("relay", eose.RelayURL), zap.String("subscription_id", eose.SubscriptionID))
			} else {
				merged.RelayEOSE = nil
			}
		case closed, ok := <-merged.Closed:
			if ok {
				t.logger.Warn("relay closed ContextVM encrypted request subscription",
					zap.String("relay", closed.RelayURL),
					zap.String("subscription_id", closed.SubscriptionID),
					zap.String("reason", closed.Reason),
				)
				if nostrpool.IsAuthRequiredReason(closed.Reason) && closed.RelayURL != "" {
					if _, attempted := authAttempted[closed.RelayURL]; attempted {
						continue
					}
					authAttempted[closed.RelayURL] = struct{}{}
					if err := t.subscriber.AuthenticateRelay(ctx, closed.RelayURL); err != nil {
						t.logger.Warn("relay ContextVM encrypted request subscription auth failed",
							zap.String("relay", closed.RelayURL),
							zap.String("reason", closed.Reason),
							zap.Error(err),
						)
						continue
					}
					merged.Close()
					next, err := subscribe()
					if err != nil {
						t.logger.Warn("relay ContextVM encrypted request subscription resubscribe after auth failed",
							zap.String("relay", closed.RelayURL),
							zap.String("reason", closed.Reason),
							zap.Error(err),
						)
						continue
					}
					merged = next
					t.logger.Info("relay ContextVM encrypted request subscription authenticated and resubscribed",
						zap.String("relay", closed.RelayURL),
						zap.String("reason", closed.Reason),
					)
				}
			} else {
				merged.Closed = nil
			}
		case _, ok := <-merged.EndOfStoredEvents:
			if !ok {
				merged.EndOfStoredEvents = nil
			} else {
				t.logger.Debug("ContextVM encrypted request subscription caught up with stored events")
			}
		case ev, ok := <-merged.Events:
			if !ok {
				t.logger.Warn("ContextVM encrypted request subscription events channel closed")
				return nil
			}
			t.logger.Debug("received ContextVM encrypted request event",
				zap.String("event_id", ev.ID.Hex()),
				zap.Int("kind", int(ev.Kind)),
				zap.String("pubkey", ev.PubKey.Hex()),
			)
			t.HandleEvent(ctx, ev)
		}
	}
}

func (t *EncryptedRequestTransport) HandleEvent(ctx context.Context, event *nostr.Event) {
	if event == nil {
		return
	}
	if event.Kind == KindContextVMMessage || event.Kind == KindContextVMGiftWrap || event.Kind == KindContextVMEphemeralWrap {
		t.HandleContextVMEvent(ctx, event)
		return
	}
	// Legacy Bahia encrypted request/result events are no longer accepted by
	// production runtime. ContextVM message and wrapper kinds above
	// are the only active encrypted control-plane transport.
	return
}

func (t *EncryptedRequestTransport) HandleContextVMEvent(ctx context.Context, outer *nostr.Event) {
	inner := outer
	encrypted := outer.Kind == KindContextVMGiftWrap || outer.Kind == KindContextVMEphemeralWrap
	if encrypted {
		if err := nostrpool.ValidateInboundEvent(outer, time.Now().UTC(), nostrpool.InboundEventMaxFutureSkew); err != nil {
			t.logger.Warn("invalid ContextVM gift wrap event", zap.String("event_id", outer.ID.Hex()), zap.Error(err))
			return
		}
		if !t.matchesContextVMWrapperRouting(outer) {
			t.logger.Debug("ContextVM gift wrap not addressed to this service", zap.String("event_id", outer.ID.Hex()), zap.String("service_pubkey", t.responder.ServicePubkey()))
			return
		}
		unwrapped, err := t.unwrapContextVMEvent(outer)
		if err != nil {
			t.logger.Warn("failed to unwrap ContextVM event", zap.String("event_id", outer.ID.Hex()), zap.Error(err))
			return
		}
		inner = unwrapped
		if !t.validContextVMWrapperPubkey(outer, inner) {
			t.logger.Warn("invalid ContextVM gift wrap provenance", zap.String("event_id", outer.ID.Hex()), zap.String("wrapper_pubkey", outer.PubKey.Hex()), zap.String("inner_pubkey", inner.PubKey.Hex()))
			return
		}
	}
	if inner == nil || inner.Kind != KindContextVMMessage {
		t.logger.Debug("ContextVM wrapper did not contain a ContextVM message", zap.String("event_id", outer.ID.Hex()))
		return
	}
	if err := nostrpool.ValidateInboundEvent(inner, time.Now().UTC(), nostrpool.InboundEventMaxFutureSkew); err != nil {
		t.logger.Warn("invalid ContextVM event", zap.String("event_id", inner.ID.Hex()), zap.Error(err))
		return
	}
	innerID := inner.ID.Hex()
	innerPubkey := inner.PubKey.Hex()
	if t.dedup.IsDuplicate(innerID) {
		t.logger.Debug("duplicate ContextVM event ignored", zap.String("event_id", innerID))
		return
	}
	t.dedup.MarkSeen(innerID)
	if !t.matchesContextVMRouting(inner) {
		t.logger.Debug("ContextVM event not routed to this service", zap.String("event_id", innerID), zap.String("service_pubkey", t.responder.ServicePubkey()))
		return
	}
	if !t.authorized(innerPubkey) {
		t.logger.Warn("unauthorized ContextVM requester", zap.String("event_id", innerID), zap.String("requester_pubkey", innerPubkey))
		t.publishContextVMResponse(ctx, outer, inner, encrypted, ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &JSONRPCError{Code: -32001, Message: "requester is not authorized for ContextVM Bahia requests"}})
		return
	}
	var rpc ContextVMJSONRPCRequest
	if err := json.Unmarshal([]byte(inner.Content), &rpc); err != nil {
		t.publishContextVMResponse(ctx, outer, inner, encrypted, ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &JSONRPCError{Code: -32700, Message: "parse error"}})
		return
	}
	if rpc.JSONRPC != "2.0" || strings.TrimSpace(rpc.Method) == "" {
		t.publishContextVMResponse(ctx, outer, inner, encrypted, ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: contextVMResponseID(rpc.ID), Error: &JSONRPCError{Code: -32600, Message: "invalid request"}})
		return
	}
	progressToken := contextVMProgressToken(rpc.Params)
	if progressToken != "" {
		if cached, ok := t.cachedContextVMResponse(innerPubkey, progressToken); ok {
			cached.ID = contextVMResponseID(rpc.ID)
			t.publishContextVMResponse(ctx, outer, inner, encrypted, cached)
			return
		}
	}
	handler := t.contextVMHandlers[rpc.Method]
	if handler == nil {
		t.logger.Warn("ContextVM method not found", zap.String("event_id", innerID), zap.String("method", rpc.Method))
		response := ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: contextVMResponseID(rpc.ID), Error: &JSONRPCError{Code: -32601, Message: "method not found"}}
		t.cacheContextVMResponse(innerPubkey, progressToken, response)
		t.publishContextVMResponse(ctx, outer, inner, encrypted, response)
		return
	}
	t.logger.Info("dispatching ContextVM request", zap.String("event_id", innerID), zap.String("method", rpc.Method), zap.String("requester_pubkey", innerPubkey))
	t.publishContextVMProgressAck(ctx, outer, inner, encrypted)
	result, err := handler(ctx, ContextVMRequest{Event: inner, OuterEvent: outer, RPC: rpc, ProgressToken: progressToken})
	response := ContextVMJSONRPCResponse{JSONRPC: "2.0", ID: contextVMResponseID(rpc.ID), Result: result}
	if err != nil {
		response.Result = nil
		response.Error = &JSONRPCError{Code: -32000, Message: err.Error()}
	}
	t.cacheContextVMResponse(innerPubkey, progressToken, response)
	t.publishContextVMResponse(ctx, outer, inner, encrypted, response)
}

func (t *EncryptedRequestTransport) unwrapContextVMEvent(event *nostr.Event) (*nostr.Event, error) {
	if t.responder == nil {
		return nil, fmt.Errorf("ContextVM responder is not configured")
	}
	conversationKey, err := t.responder.conversationKey(event.PubKey.Hex())
	if err != nil {
		return nil, err
	}
	plaintext, err := nip44.Decrypt(event.Content, conversationKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt ContextVM gift wrap: %w", err)
	}
	var inner nostr.Event
	if err := json.Unmarshal([]byte(plaintext), &inner); err != nil {
		return nil, fmt.Errorf("decode ContextVM inner event: %w", err)
	}
	return &inner, nil
}

func (t *EncryptedRequestTransport) publishContextVMProgressAck(ctx context.Context, outer, request *nostr.Event, encrypted bool) {
	requestID := request.ID.Hex()
	if encrypted && outer != nil && outer.ID != (nostr.ID{}) {
		requestID = outer.ID.Hex()
	}
	notification := ContextVMJSONRPCNotification{
		JSONRPC: "2.0",
		Method:  ContextVMProgressNotificationMethod,
		Params: ContextVMProgressParams{
			RequestID: requestID,
			Status:    ContextVMProgressStatusProcessing,
		},
	}
	t.publishContextVMPayload(ctx, outer, request, encrypted, notification, "progress ack")
}

func (t *EncryptedRequestTransport) publishContextVMResponse(ctx context.Context, outer, request *nostr.Event, encrypted bool, response ContextVMJSONRPCResponse) {
	t.publishContextVMPayload(ctx, outer, request, encrypted, response, "response")
}

func (t *EncryptedRequestTransport) publishContextVMPayload(ctx context.Context, outer, request *nostr.Event, encrypted bool, payload any, label string) {
	requestID := request.ID.Hex()
	requestPubkey := request.PubKey.Hex()
	if t.responder == nil || t.responder.publisher == nil {
		t.logger.Warn("ContextVM responder unavailable", zap.String("event_id", requestID))
		return
	}
	content, err := json.Marshal(payload)
	if err != nil {
		t.logger.Error("marshal ContextVM "+label+" failed", zap.String("event_id", requestID), zap.Error(err))
		return
	}
	responseEvent := &nostr.Event{Kind: KindContextVMMessage, CreatedAt: nostr.Now(), Tags: nostr.Tags{{tagReplyEvent, requestID, "", "reply"}, {tagRecipientPubkey, requestPubkey}, {ContextVMRoutingTag, ContextVMWireVersion}}, Content: string(content)}
	if err := SignGoNostrEvent(ctx, t.responder.signer, responseEvent); err != nil {
		t.logger.Error("sign ContextVM "+label+" failed", zap.String("event_id", requestID), zap.Error(err))
		return
	}
	publishEvent := responseEvent
	if encrypted {
		wrapped, err := t.wrapContextVMResponse(ctx, outer, request, responseEvent)
		if err != nil {
			t.logger.Error("wrap ContextVM "+label+" failed", zap.String("event_id", requestID), zap.Error(err))
			return
		}
		publishEvent = wrapped
	}
	published, err := t.responder.publisher.Publish(ctx, *publishEvent)
	if err != nil {
		t.logger.Error("publish ContextVM "+label+" failed", zap.String("event_id", requestID), zap.Error(err))
		return
	}
	if published == 0 {
		t.logger.Error("publish ContextVM "+label+" failed", zap.String("event_id", requestID), zap.String("error", "no relay accepted event"))
	}
}

func (t *EncryptedRequestTransport) wrapContextVMResponse(_ context.Context, outer, request, response *nostr.Event) (*nostr.Event, error) {
	content, err := json.Marshal(response)
	if err != nil {
		return nil, fmt.Errorf("marshal ContextVM inner response: %w", err)
	}
	wrapperPrivateKey := nostr.Generate()
	wrapperPubkey := wrapperPrivateKey.Public()
	conversationKey, err := nip44.GenerateConversationKey(request.PubKey, wrapperPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("generate ContextVM response wrapper conversation key: %w", err)
	}
	ciphertext, err := nip44.Encrypt(string(content), conversationKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt ContextVM response: %w", err)
	}
	kind := KindContextVMGiftWrap
	if outer != nil && outer.Kind == KindContextVMEphemeralWrap {
		kind = KindContextVMEphemeralWrap
	}
	wrapped := &nostr.Event{Kind: nostr.Kind(kind), PubKey: wrapperPubkey, CreatedAt: nostr.Now(), Tags: nostr.Tags{{tagReplyEvent, outer.ID.Hex(), "", "reply"}, {tagRecipientPubkey, request.PubKey.Hex()}}, Content: ciphertext}
	if err := wrapped.Sign(wrapperPrivateKey); err != nil {
		return nil, fmt.Errorf("sign ContextVM gift wrap response: %w", err)
	}
	return wrapped, nil
}

func (t *EncryptedRequestTransport) cachedContextVMResponse(pubkey, progressToken string) (ContextVMJSONRPCResponse, bool) {
	if progressToken == "" {
		return ContextVMJSONRPCResponse{}, false
	}
	t.contextVMMu.Lock()
	defer t.contextVMMu.Unlock()
	response, ok := t.contextVMDedup.get(pubkey + ":" + progressToken)
	return response, ok
}

func (t *EncryptedRequestTransport) cacheContextVMResponse(pubkey, progressToken string, response ContextVMJSONRPCResponse) {
	if progressToken == "" {
		return
	}
	t.contextVMMu.Lock()
	defer t.contextVMMu.Unlock()
	t.contextVMDedup.put(pubkey+":"+progressToken, response)
}

func (t *EncryptedRequestTransport) matchesContextVMRouting(event *nostr.Event) bool {
	if t.responder == nil {
		return false
	}
	servicePubkey := t.responder.ServicePubkey()
	return servicePubkey == "" || tagContains(event.Tags, tagRecipientPubkey, servicePubkey)
}

func (t *EncryptedRequestTransport) matchesContextVMWrapperRouting(event *nostr.Event) bool {
	if t.responder == nil {
		return false
	}
	servicePubkey := t.responder.ServicePubkey()
	return servicePubkey != "" && tagContains(event.Tags, tagRecipientPubkey, servicePubkey)
}

func (t *EncryptedRequestTransport) validContextVMWrapperPubkey(outer, inner *nostr.Event) bool {
	if t.responder == nil || outer == nil || inner == nil || outer.PubKey == (nostr.PubKey{}) {
		return false
	}
	servicePubkey := t.responder.ServicePubkey()
	outerPubkey := outer.PubKey.Hex()
	return outer.PubKey != inner.PubKey && outerPubkey != servicePubkey
}

func contextVMResponseID(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

func contextVMProgressToken(params json.RawMessage) string {
	if len(params) == 0 {
		return ""
	}
	var decoded map[string]any
	if err := json.Unmarshal(params, &decoded); err != nil {
		return ""
	}
	meta, ok := decoded["_meta"].(map[string]any)
	if !ok {
		return ""
	}
	token, _ := meta["progressToken"].(string)
	return strings.TrimSpace(token)
}

func (t *EncryptedRequestTransport) authorized(pubkey string) bool {
	return len(t.authorizedPubkeys) == 0 || slices.Contains(t.authorizedPubkeys, pubkey)
}

func (t *EncryptedRequestTransport) matchesRoutingTags(event *nostr.Event) bool {
	if event == nil || !tagContains(event.Tags, EncryptedRequestRoutingTag, EncryptedRequestWireVersion) {
		return false
	}
	servicePubkey := t.responder.ServicePubkey()
	return servicePubkey != "" && tagContains(event.Tags, tagRecipientPubkey, servicePubkey)
}

func tagContains(tags nostr.Tags, name, value string) bool {
	for _, tag := range tags {
		if len(tag) >= 2 && tag[0] == name && tag[1] == value {
			return true
		}
	}
	return false
}

func (t *EncryptedRequestTransport) publishError(ctx context.Context, event *nostr.Event, code, message string) {
	if t.responder == nil {
		t.logger.Warn("encrypted request responder unavailable", zap.String("event_id", event.ID.Hex()), zap.String("code", code))
		return
	}
	if err := t.responder.PublishEncryptedResult(ctx, event, "error", nil, &ResultError{Code: code, Message: message}); err != nil {
		t.logger.Error("publish encrypted error result failed", zap.String("event_id", event.ID.Hex()), zap.String("code", code), zap.Error(err))
	}
}
