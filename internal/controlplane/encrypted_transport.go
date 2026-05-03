package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	canonicalnostr "fiatjaf.com/nostr"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/nip44"
	nostrpool "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"go.uber.org/zap"
)

const (
	EncryptedRequestRoutingTag  = "encrypted"
	EncryptedRequestWireVersion = "bahia-encrypted-v1"
	KindEncryptedRequest        = 5980 // Browser → Bahia encrypted request
	KindEncryptedResult         = 7980 // Bahia → Browser encrypted result
)

// EncryptedRequestSubscriber is the relay subscription contract used by the
// encrypted request/result event runtime. RelayPool satisfies this interface.
type EncryptedRequestSubscriber interface {
	SubscribeAllWithEOSE(ctx context.Context, filters []nostr.Filter) (*nostrpool.MergedSubscription, error)
}

// EncryptedRequestEnvelope is encrypted inside kind:5980 request content.
type EncryptedRequestEnvelope struct {
	Version         string          `json:"version"`
	Operation       string          `json:"operation"`
	RequesterPubkey string          `json:"requester_pubkey"`
	Payload         json.RawMessage `json:"payload"`
}

// EncryptedResultEnvelope is encrypted inside kind:7980 result content.
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
		servicePubkey, _ = nostr.GetPublicKey(privateKeyHex)
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
	if event.PubKey == "" {
		return nil, fmt.Errorf("request event pubkey is required")
	}
	if strings.TrimSpace(event.Content) == "" {
		return nil, fmt.Errorf("request event content is empty")
	}
	conversationKey, err := r.conversationKey(event.PubKey)
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
	if requestEvent.ID == "" || requestEvent.PubKey == "" {
		return fmt.Errorf("request event id and pubkey are required")
	}
	if status == "" {
		status = "ok"
	}
	envelope := EncryptedResultEnvelope{
		Version:        EncryptedRequestWireVersion,
		RequestEventID: requestEvent.ID,
		Status:         status,
		Payload:        payload,
		Error:          resultErr,
	}
	content, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal encrypted result envelope: %w", err)
	}
	conversationKey, err := r.conversationKey(requestEvent.PubKey)
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
			{"e", requestEvent.ID, "", "reply"},
			{"p", requestEvent.PubKey},
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
	conversationKey, err := nip44.GenerateConversationKey(counterpartyPubkey, r.privateKey)
	if err != nil {
		return [32]byte{}, fmt.Errorf("generate encrypted request conversation key: %w", err)
	}
	return conversationKey, nil
}

// EncryptedRequestTransport subscribes to encrypted request events and dispatches
// them to operation handlers. It never publishes sensitive payloads as public
// sidecar projections.
type EncryptedRequestTransport struct {
	subscriber        EncryptedRequestSubscriber
	responder         *EncryptedResponder
	authorizedPubkeys []string
	handlers          map[string]EncryptedRequestHandler
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

func (t *EncryptedRequestTransport) Run(ctx context.Context) error {
	if t.subscriber == nil {
		return fmt.Errorf("encrypted request subscriber is not configured")
	}
	now := nostr.Now()
	filter := nostr.Filter{Kinds: []int{KindEncryptedRequest}, Since: &now, Tags: nostr.TagMap{EncryptedRequestRoutingTag: {EncryptedRequestWireVersion}}}
	if servicePubkey := t.responder.ServicePubkey(); servicePubkey != "" {
		filter.Tags["p"] = []string{servicePubkey}
	}
	merged, err := t.subscriber.SubscribeAllWithEOSE(ctx, []nostr.Filter{filter})
	if err != nil {
		return fmt.Errorf("subscribe to encrypted request/result events: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case _, ok := <-merged.EndOfStoredEvents:
			if !ok {
				merged.EndOfStoredEvents = nil
			}
		case ev, ok := <-merged.Events:
			if !ok {
				return nil
			}
			t.HandleEvent(ctx, ev)
		}
	}
}

func (t *EncryptedRequestTransport) HandleEvent(ctx context.Context, event *nostr.Event) {
	if event == nil || event.Kind != KindEncryptedRequest {
		return
	}
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		t.logger.Warn("invalid encrypted request signature", zap.String("event_id", event.ID), zap.Error(err))
		return
	}
	if t.dedup.IsDuplicate(event.ID) {
		return
	}
	t.dedup.MarkSeen(event.ID)
	if !t.matchesRoutingTags(event) {
		return
	}

	if !t.authorized(event.PubKey) {
		t.publishError(ctx, event, "unauthorized", "requester is not authorized for encrypted Bahia requests")
		return
	}

	plaintext, err := t.responder.DecryptRequestContent(event)
	if err != nil {
		t.logger.Warn("failed to decrypt encrypted request", zap.String("event_id", event.ID), zap.Error(err))
		t.publishError(ctx, event, "decrypt_failed", "encrypted request content could not be decrypted")
		return
	}

	var envelope EncryptedRequestEnvelope
	if err := json.Unmarshal(plaintext, &envelope); err != nil {
		t.publishError(ctx, event, "invalid_payload", "encrypted request payload is not valid JSON")
		return
	}
	if envelope.Version != "" && envelope.Version != EncryptedRequestWireVersion {
		t.publishError(ctx, event, "unsupported_version", "encrypted request version is not supported")
		return
	}
	if envelope.RequesterPubkey != "" && envelope.RequesterPubkey != event.PubKey {
		t.publishError(ctx, event, "requester_mismatch", "encrypted requester does not match event pubkey")
		return
	}
	operation := strings.TrimSpace(envelope.Operation)
	if operation == "" {
		t.publishError(ctx, event, "missing_operation", "encrypted request operation is required")
		return
	}
	handler := t.handlers[operation]
	if handler == nil {
		t.publishError(ctx, event, "unknown_operation", "encrypted request operation is not registered")
		return
	}
	payload, err := handler(ctx, EncryptedRequest{Event: event, Envelope: envelope})
	if err != nil {
		t.publishError(ctx, event, "handler_failed", err.Error())
		return
	}
	if err := t.responder.PublishEncryptedResult(ctx, event, "ok", payload, nil); err != nil {
		t.logger.Error("publish encrypted result failed", zap.String("event_id", event.ID), zap.Error(err))
	}
}

func (t *EncryptedRequestTransport) authorized(pubkey string) bool {
	return len(t.authorizedPubkeys) == 0 || slices.Contains(t.authorizedPubkeys, pubkey)
}

func (t *EncryptedRequestTransport) matchesRoutingTags(event *nostr.Event) bool {
	if event == nil || !tagContains(event.Tags, EncryptedRequestRoutingTag, EncryptedRequestWireVersion) {
		return false
	}
	servicePubkey := t.responder.ServicePubkey()
	return servicePubkey != "" && tagContains(event.Tags, "p", servicePubkey)
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
		t.logger.Warn("encrypted request responder unavailable", zap.String("event_id", event.ID), zap.String("code", code))
		return
	}
	if err := t.responder.PublishEncryptedResult(ctx, event, "error", nil, &ResultError{Code: code, Message: message}); err != nil {
		t.logger.Error("publish encrypted error result failed", zap.String("event_id", event.ID), zap.String("code", code), zap.Error(err))
	}
}
