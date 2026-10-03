package client

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"fiatjaf.com/nostr/nip59"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- Round-trip test: IntentPublisher → ParseIntent ---

func TestIntentPublisher_ParseIntentRoundTrip(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://test.relay"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: time.Second,
		Transport:     &mockIntentTransport{},
	})
	require.NoError(t, err)
	defer pub.Close()

	orgID := uuid.New().String()
	req := PublishIntentRequest{
		Domain:     "service",
		Op:         "create",
		Coordinate: "svc-abc-123",
		Schema:     "bahia.intent.service.v1",
		OrgID:      orgID,
		Content: map[string]interface{}{
			"id":     "svc-abc-123",
			"name":   "my-service",
			"org_id": orgID,
		},
		IntentID: uuid.New().String(),
	}

	prepared, err := pub.PrepareIntent(context.Background(), req)
	require.NoError(t, err)

	// The prepared event should be kind 30900 (not sensitive domain).
	assert.Equal(t, nostr.Kind(30900), prepared.Event.Kind)
	assert.Equal(t, prepared.Event.ID, prepared.InnerEvent.ID,
		"non-sensitive domain: Event and InnerEvent should be identical")

	// Verify signature.
	assert.True(t, prepared.Event.VerifySignature(), "event must have valid signature")

	// Round-trip through daemon's ParseIntent.
	intent, err := controlplane.ParseIntent(&prepared.Event)
	require.NoError(t, err)

	assert.Equal(t, "service", intent.Domain)
	assert.Equal(t, "create", intent.Op)
	assert.Equal(t, "svc-abc-123", intent.Coordinate)
	assert.Equal(t, "bahia.intent.service.v1", intent.Schema)
	assert.Equal(t, req.IntentID, intent.IntentID)
	assert.Equal(t, orgID, intent.OrgID.String())
	assert.Equal(t, "my-service", intent.Content["name"])
}

func TestIntentPublisher_ParseIntentRoundTrip_WithExpectedUpdatedAt(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://test.relay"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: time.Second,
		Transport:     &mockIntentTransport{},
	})
	require.NoError(t, err)
	defer pub.Close()

	ts := int64(1727740800)
	req := PublishIntentRequest{
		Domain:            "environment",
		Op:                "update",
		Coordinate:        "env-xyz-789",
		OrgID:             uuid.New().String(),
		IntentID:          uuid.New().String(),
		ExpectedUpdatedAt: &ts,
		Content: map[string]interface{}{
			"id":   "env-xyz-789",
			"name": "production",
		},
	}

	prepared, err := pub.PrepareIntent(context.Background(), req)
	require.NoError(t, err)

	intent, err := controlplane.ParseIntent(&prepared.Event)
	require.NoError(t, err)

	require.NotNil(t, intent.ExpectedUpdatedAt, "expected_updated_at should be parsed")
	assert.Equal(t, ts, *intent.ExpectedUpdatedAt)
}

// --- Round-trip test: IntentPublisher gift-wrap → nip59.GiftUnwrap → ParseIntent ---

func TestIntentPublisher_GiftWrapRoundTrip(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)
	serviceSigner := keyer.NewPlainKeySigner(serviceKey)

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://test.relay"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: time.Second,
		Transport:     &mockIntentTransport{},
	})
	require.NoError(t, err)
	defer pub.Close()

	orgID := uuid.New().String()
	req := PublishIntentRequest{
		Domain:     "secret",
		Op:         "create",
		Coordinate: "secret-key-001",
		OrgID:      orgID,
		IntentID:   uuid.New().String(),
		Content: map[string]interface{}{
			"id":    "secret-key-001",
			"name":  "db-password",
			"scope": "environment",
		},
	}

	prepared, err := pub.PrepareIntent(context.Background(), req)
	require.NoError(t, err)

	// The prepared event should be a kind 1059 gift-wrap.
	assert.Equal(t, nostr.KindGiftWrap, prepared.Event.Kind,
		"sensitive domain must produce a gift-wrapped event")
	// Inner event is the 30900 intent.
	assert.Equal(t, nostr.Kind(30900), prepared.InnerEvent.Kind)
	assert.NotEqual(t, prepared.Event.ID, prepared.InnerEvent.ID,
		"gift-wrapped: outer and inner IDs must differ")

	// The #p tag should route to the service pubkey.
	var pTag string
	for _, tag := range prepared.Event.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			pTag = tag[1]
			break
		}
	}
	assert.Equal(t, serviceKey.Public().Hex(), pTag,
		"gift-wrap #p must be the service pubkey")

	// Round-trip: unwrap using the service's key (as the daemon would).
	rumor, err := nip59.GiftUnwrap(prepared.Event, func(sender nostr.PubKey, ciphertext string) (string, error) {
		return serviceSigner.Decrypt(context.Background(), ciphertext, sender)
	})
	require.NoError(t, err)

	// The rumor's PubKey is set to the seal signer (operator).
	assert.Equal(t, operatorKey.Public().Hex(), rumor.PubKey.Hex(),
		"unwrapped rumor pubkey must be the operator")

	// Set the ID so ParseIntent can work with it.
	rumor.ID = rumor.GetID()

	// Parse the unwrapped inner event through the daemon's ParseIntent.
	intent, err := controlplane.ParseIntent(&rumor)
	require.NoError(t, err)

	assert.Equal(t, "secret", intent.Domain)
	assert.Equal(t, "create", intent.Op)
	assert.Equal(t, "secret-key-001", intent.Coordinate)
	assert.Equal(t, req.IntentID, intent.IntentID)
	assert.Equal(t, orgID, intent.OrgID.String())
	assert.Equal(t, "db-password", intent.Content["name"])
}

// --- Round-trip test: gift-wrap → unwrap → ParseIntent → ProcessInProcess ---
// This exercises the full daemon processing chain: the client's gift-wrap
// is unwrapped, parsed by the daemon's ParseIntent, and fed through the
// IntentProcessor pipeline with authorization and idempotency.

func TestIntentPublisher_GiftWrapRoundTrip_DaemonProcessor(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)
	serviceSigner := keyer.NewPlainKeySigner(serviceKey)

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://test.relay"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: time.Second,
		Transport:     &mockIntentTransport{},
	})
	require.NoError(t, err)
	defer pub.Close()

	orgID := uuid.New().String()
	req := PublishIntentRequest{
		Domain:     "org",
		Op:         "create",
		Coordinate: "org:" + orgID,
		OrgID:      orgID,
		IntentID:   uuid.New().String(),
		Content: map[string]interface{}{
			"id":   orgID,
			"name": "test-org",
		},
	}

	prepared, err := pub.PrepareIntent(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, nostr.KindGiftWrap, prepared.Event.Kind)

	// Step 1: Unwrap the gift-wrap using the service key (as the daemon would).
	rumor, err := nip59.GiftUnwrap(prepared.Event, func(sender nostr.PubKey, ciphertext string) (string, error) {
		return serviceSigner.Decrypt(context.Background(), ciphertext, sender)
	})
	require.NoError(t, err)
	assert.Equal(t, operatorKey.Public().Hex(), rumor.PubKey.Hex(),
		"unwrapped rumor pubkey must be the operator")

	// Step 2: Parse through daemon's ParseIntent.
	rumor.ID = rumor.GetID()
	intent, err := controlplane.ParseIntent(&rumor)
	require.NoError(t, err)
	intent.Actor = rumor.PubKey.Hex() // as the transport sets it

	// Step 3: Process through the daemon's IntentProcessor pipeline.
	store := openIntentTestStore(t)
	ts := controlplane.NewTrustSet(nil, zap.NewNop(),
		controlplane.WithBootstrapOwners(map[string]string{orgID: operatorKey.Public().Hex()}),
	)
	handler := &captureHandler{}
	proc := controlplane.NewIntentProcessor(ts, store, nil,
		controlplane.IntentProcessorConfig{EnabledDomains: map[string]bool{"org": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("org", handler)

	err = proc.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err)

	// The handler should have received the intent.
	require.Len(t, handler.intents, 1, "processor should hand off exactly one intent")
	handled := handler.intents[0]
	assert.Equal(t, "org", handled.Domain)
	assert.Equal(t, "create", handled.Op)
	assert.Equal(t, req.IntentID, handled.IntentID)
	assert.Equal(t, operatorKey.Public().Hex(), handled.Actor)
	assert.Equal(t, "test-org", handled.Content["name"])
}

// --- (a) intent published and status received → exit 0 ---

func TestIntentPublisher_AcceptedStatus_ExitCode0(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)
	serviceSigner := keyer.NewPlainKeySigner(serviceKey)

	intentID := uuid.New().String()

	transport := &mockIntentTransport{
		publishFunc: func(_ context.Context, ev nostr.Event) ([]ContextVMPublishResult, error) {
			return []ContextVMPublishResult{
				{RelayURL: "wss://relay1", Accepted: true},
			}, nil
		},
		subscribeFunc: func(_ context.Context, _ []nostr.Filter) (*ContextVMSubscription, error) {
			events := make(chan *nostr.Event, 1)
			// Simulate daemon publishing an accepted status.
			go func() {
				status := buildTestStatusEvent(t, serviceSigner, operatorKey.Public().Hex(),
					intentID, "svc-test", "accepted", "applied", "")
				events <- &status
			}()
			return &ContextVMSubscription{
				Events:            events,
				EndOfStoredEvents: make(chan struct{}),
				closeFn:           func() {},
			}, nil
		},
	}

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://relay1"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: 5 * time.Second,
		Transport:     transport,
	})
	require.NoError(t, err)
	defer pub.Close()

	prepared, err := pub.PrepareIntent(context.Background(), PublishIntentRequest{
		Domain:     "service",
		Op:         "create",
		Coordinate: "svc-test",
		OrgID:      uuid.New().String(),
		IntentID:   intentID,
		Content:    map[string]interface{}{"name": "test"},
	})
	require.NoError(t, err)

	result, err := pub.PublishAndWait(context.Background(), prepared)
	require.NoError(t, err)
	assert.Equal(t, "accepted", result.Status)
	assert.Equal(t, ExitCodeAccepted, result.ExitCode)
	assert.Equal(t, intentID, result.IntentID)
	assert.NotEmpty(t, result.EventID)
}

// --- (b) rejection status → exit 1 ---

func TestIntentPublisher_RejectedStatus_ExitCode1(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)
	serviceSigner := keyer.NewPlainKeySigner(serviceKey)

	intentID := uuid.New().String()

	transport := &mockIntentTransport{
		publishFunc: func(_ context.Context, ev nostr.Event) ([]ContextVMPublishResult, error) {
			return []ContextVMPublishResult{
				{RelayURL: "wss://relay1", Accepted: true},
			}, nil
		},
		subscribeFunc: func(_ context.Context, _ []nostr.Filter) (*ContextVMSubscription, error) {
			events := make(chan *nostr.Event, 1)
			go func() {
				status := buildTestStatusEvent(t, serviceSigner, operatorKey.Public().Hex(),
					intentID, "svc-test", "rejected", "rejected", "insufficient permission: services:write")
				events <- &status
			}()
			return &ContextVMSubscription{
				Events:            events,
				EndOfStoredEvents: make(chan struct{}),
				closeFn:           func() {},
			}, nil
		},
	}

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://relay1"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: 5 * time.Second,
		Transport:     transport,
	})
	require.NoError(t, err)
	defer pub.Close()

	prepared, err := pub.PrepareIntent(context.Background(), PublishIntentRequest{
		Domain:     "service",
		Op:         "create",
		Coordinate: "svc-test",
		OrgID:      uuid.New().String(),
		IntentID:   intentID,
		Content:    map[string]interface{}{"name": "test"},
	})
	require.NoError(t, err)

	result, err := pub.PublishAndWait(context.Background(), prepared)
	require.NoError(t, err)
	assert.Equal(t, "rejected", result.Status)
	assert.Equal(t, ExitCodeRejected, result.ExitCode)
	assert.Contains(t, result.Reason, "insufficient permission")
}

// --- (c) timeout → exit 2 ---

func TestIntentPublisher_Timeout_ExitCode2(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)

	transport := &mockIntentTransport{
		publishFunc: func(_ context.Context, ev nostr.Event) ([]ContextVMPublishResult, error) {
			return []ContextVMPublishResult{
				{RelayURL: "wss://relay1", Accepted: true},
			}, nil
		},
		subscribeFunc: func(_ context.Context, _ []nostr.Filter) (*ContextVMSubscription, error) {
			events := make(chan *nostr.Event) // never delivers
			return &ContextVMSubscription{
				Events:            events,
				EndOfStoredEvents: make(chan struct{}),
				closeFn:           func() {},
			}, nil
		},
	}

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://relay1"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: 100 * time.Millisecond, // short timeout for test
		Transport:     transport,
	})
	require.NoError(t, err)
	defer pub.Close()

	prepared, err := pub.PrepareIntent(context.Background(), PublishIntentRequest{
		Domain:     "service",
		Op:         "create",
		Coordinate: "svc-timeout",
		OrgID:      uuid.New().String(),
		IntentID:   uuid.New().String(),
		Content:    map[string]interface{}{"name": "test"},
	})
	require.NoError(t, err)

	result, err := pub.PublishAndWait(context.Background(), prepared)
	require.NoError(t, err)
	assert.Equal(t, "timeout", result.Status)
	assert.Equal(t, ExitCodeTimeout, result.ExitCode)
	assert.NotEmpty(t, result.IntentID)
	assert.NotEmpty(t, result.EventID)
}

// --- (d) idempotent retry with same intent_id ---

func TestIntentPublisher_IdempotentRetry(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)
	serviceSigner := keyer.NewPlainKeySigner(serviceKey)

	intentID := uuid.New().String()

	var mu sync.Mutex
	publishCount := 0
	var publishedEvents []nostr.Event

	transport := &mockIntentTransport{
		publishFunc: func(_ context.Context, ev nostr.Event) ([]ContextVMPublishResult, error) {
			mu.Lock()
			publishCount++
			publishedEvents = append(publishedEvents, ev)
			mu.Unlock()
			return []ContextVMPublishResult{
				{RelayURL: "wss://relay1", Accepted: true},
			}, nil
		},
		subscribeFunc: func(_ context.Context, _ []nostr.Filter) (*ContextVMSubscription, error) {
			events := make(chan *nostr.Event, 1)
			go func() {
				status := buildTestStatusEvent(t, serviceSigner, operatorKey.Public().Hex(),
					intentID, "svc-idempotent", "accepted", "applied", "")
				events <- &status
			}()
			return &ContextVMSubscription{
				Events:            events,
				EndOfStoredEvents: make(chan struct{}),
				closeFn:           func() {},
			}, nil
		},
	}

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://relay1"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: 5 * time.Second,
		Transport:     transport,
	})
	require.NoError(t, err)
	defer pub.Close()

	req := PublishIntentRequest{
		Domain:     "service",
		Op:         "create",
		Coordinate: "svc-idempotent",
		OrgID:      uuid.New().String(),
		IntentID:   intentID,
		Content:    map[string]interface{}{"name": "test"},
	}

	// Prepare once, publish twice (idempotent retry).
	prepared, err := pub.PrepareIntent(context.Background(), req)
	require.NoError(t, err)

	result1, err := pub.PublishAndWait(context.Background(), prepared)
	require.NoError(t, err)
	assert.Equal(t, ExitCodeAccepted, result1.ExitCode)

	result2, err := pub.PublishAndWait(context.Background(), prepared)
	require.NoError(t, err)
	assert.Equal(t, ExitCodeAccepted, result2.ExitCode)

	// Both publishes used the same signed event.
	mu.Lock()
	require.Equal(t, 2, publishCount, "should have published twice")
	assert.Equal(t, publishedEvents[0].ID, publishedEvents[1].ID,
		"idempotent retry must reuse the same signed event")
	assert.Equal(t, publishedEvents[0].Sig, publishedEvents[1].Sig,
		"idempotent retry must reuse the same signature")
	mu.Unlock()
}

// --- (e) gift-wrapped intent for secret domain ---

func TestIntentPublisher_GiftWrappedSecretDomain(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)

	var publishedEvent nostr.Event
	transport := &mockIntentTransport{
		publishFunc: func(_ context.Context, ev nostr.Event) ([]ContextVMPublishResult, error) {
			publishedEvent = ev
			return []ContextVMPublishResult{
				{RelayURL: "wss://relay1", Accepted: true},
			}, nil
		},
		subscribeFunc: func(_ context.Context, _ []nostr.Filter) (*ContextVMSubscription, error) {
			events := make(chan *nostr.Event) // no status needed for this test
			return &ContextVMSubscription{
				Events:            events,
				EndOfStoredEvents: make(chan struct{}),
				closeFn:           func() {},
			}, nil
		},
	}

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://relay1"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: 100 * time.Millisecond,
		Transport:     transport,
	})
	require.NoError(t, err)
	defer pub.Close()

	orgID := uuid.New().String()
	req := PublishIntentRequest{
		Domain:     "secret",
		Op:         "create",
		Coordinate: "secret-db-pw",
		OrgID:      orgID,
		IntentID:   uuid.New().String(),
		Content: map[string]interface{}{
			"id":    "secret-db-pw",
			"name":  "DB_PASSWORD",
			"scope": "environment",
		},
	}

	prepared, err := pub.PrepareIntent(context.Background(), req)
	require.NoError(t, err)

	// The prepared event is a gift-wrap.
	assert.Equal(t, nostr.KindGiftWrap, prepared.Event.Kind,
		"secret domain must be gift-wrapped")

	// Publish it (will timeout, but we want to verify what was published).
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	result, err := pub.PublishAndWait(ctx, prepared)
	require.NoError(t, err)
	assert.Equal(t, ExitCodeTimeout, result.ExitCode)

	// The published event is a kind 1059 gift-wrap.
	assert.Equal(t, nostr.KindGiftWrap, publishedEvent.Kind)

	// The content of the outer event is encrypted (not plaintext).
	var raw map[string]interface{}
	err = json.Unmarshal([]byte(publishedEvent.Content), &raw)
	assert.Error(t, err, "gift-wrap content should not be valid JSON (it's encrypted)")

	// Verify #p tag routes to service pubkey.
	var pTag string
	for _, tag := range publishedEvent.Tags {
		if len(tag) >= 2 && tag[0] == "p" {
			pTag = tag[1]
		}
	}
	assert.Equal(t, serviceKey.Public().Hex(), pTag)
}

// --- No relay accepted → exit 3 ---

func TestIntentPublisher_NoRelayAccepted_ExitCode3(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)

	transport := &mockIntentTransport{
		publishFunc: func(_ context.Context, ev nostr.Event) ([]ContextVMPublishResult, error) {
			return []ContextVMPublishResult{
				{RelayURL: "wss://relay1", Accepted: false, Reason: "auth-required"},
			}, fmt.Errorf("no relay accepted the event")
		},
		subscribeFunc: func(_ context.Context, _ []nostr.Filter) (*ContextVMSubscription, error) {
			events := make(chan *nostr.Event)
			return &ContextVMSubscription{
				Events:            events,
				EndOfStoredEvents: make(chan struct{}),
				closeFn:           func() {},
			}, nil
		},
	}

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://relay1"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: time.Second,
		Transport:     transport,
	})
	require.NoError(t, err)
	defer pub.Close()

	prepared, err := pub.PrepareIntent(context.Background(), PublishIntentRequest{
		Domain:     "service",
		Op:         "create",
		Coordinate: "svc-norelay",
		OrgID:      uuid.New().String(),
		IntentID:   uuid.New().String(),
		Content:    map[string]interface{}{"name": "test"},
	})
	require.NoError(t, err)

	result, err := pub.PublishAndWait(context.Background(), prepared)
	require.NoError(t, err)
	assert.Equal(t, ExitCodeNoRelay, result.ExitCode)
	assert.Equal(t, "no_relay", result.Status)
}

// --- Conflict status → exit 1 ---

func TestIntentPublisher_ConflictStatus_ExitCode1(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)
	serviceSigner := keyer.NewPlainKeySigner(serviceKey)

	intentID := uuid.New().String()

	transport := &mockIntentTransport{
		publishFunc: func(_ context.Context, ev nostr.Event) ([]ContextVMPublishResult, error) {
			return []ContextVMPublishResult{
				{RelayURL: "wss://relay1", Accepted: true},
			}, nil
		},
		subscribeFunc: func(_ context.Context, _ []nostr.Filter) (*ContextVMSubscription, error) {
			events := make(chan *nostr.Event, 1)
			go func() {
				status := buildTestStatusEvent(t, serviceSigner, operatorKey.Public().Hex(),
					intentID, "svc-conflict", "conflict", "revision_conflict", "stale expected_updated_at")
				events <- &status
			}()
			return &ContextVMSubscription{
				Events:            events,
				EndOfStoredEvents: make(chan struct{}),
				closeFn:           func() {},
			}, nil
		},
	}

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://relay1"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: 5 * time.Second,
		Transport:     transport,
	})
	require.NoError(t, err)
	defer pub.Close()

	prepared, err := pub.PrepareIntent(context.Background(), PublishIntentRequest{
		Domain:     "service",
		Op:         "update",
		Coordinate: "svc-conflict",
		OrgID:      uuid.New().String(),
		IntentID:   intentID,
		Content:    map[string]interface{}{"name": "test"},
	})
	require.NoError(t, err)

	result, err := pub.PublishAndWait(context.Background(), prepared)
	require.NoError(t, err)
	assert.Equal(t, "conflict", result.Status)
	assert.Equal(t, ExitCodeRejected, result.ExitCode)
	assert.Contains(t, result.Reason, "stale expected_updated_at")
}

// --- Sensitive domain detection ---

func TestIntentPublisher_SensitiveDomains(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://test.relay"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: time.Second,
		Transport:     &mockIntentTransport{},
	})
	require.NoError(t, err)
	defer pub.Close()

	assert.True(t, pub.IsSensitiveDomain("org"))
	assert.True(t, pub.IsSensitiveDomain("secret"))
	assert.True(t, pub.IsSensitiveDomain("notification"))
	assert.False(t, pub.IsSensitiveDomain("service"))
	assert.False(t, pub.IsSensitiveDomain("environment"))
}

// --- Config validation ---

func TestIntentPublisher_ConfigValidation(t *testing.T) {
	operatorKey := nostr.Generate()

	tests := []struct {
		name    string
		cfg     IntentPublisherConfig
		wantErr string
	}{
		{
			name: "no relays",
			cfg: IntentPublisherConfig{
				PrivateKey:    operatorKey.Hex(),
				ServicePubkey: nostr.Generate().Public().Hex(),
			},
			wantErr: "at least one relay",
		},
		{
			name: "no service pubkey",
			cfg: IntentPublisherConfig{
				Relays:     []string{"wss://relay"},
				PrivateKey: operatorKey.Hex(),
			},
			wantErr: "service pubkey",
		},
		{
			name: "negative timeout",
			cfg: IntentPublisherConfig{
				Relays:        []string{"wss://relay"},
				PrivateKey:    operatorKey.Hex(),
				ServicePubkey: nostr.Generate().Public().Hex(),
				ResultTimeout: -1 * time.Second,
			},
			wantErr: "result timeout must be positive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.cfg.Transport = &mockIntentTransport{} // prevent real pool creation
			_, err := NewIntentPublisher(tt.cfg)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// --- BuildIntentEvent validation ---

func TestBuildIntentEvent_Validation(t *testing.T) {
	operatorKey := nostr.Generate()
	serviceKey := nostr.Generate()
	operatorSigner := keyer.NewPlainKeySigner(operatorKey)

	pub, err := NewIntentPublisher(IntentPublisherConfig{
		Relays:        []string{"wss://test.relay"},
		Signer:        operatorSigner,
		Pubkey:        operatorKey.Public().Hex(),
		ServicePubkey: serviceKey.Public().Hex(),
		ResultTimeout: time.Second,
		Transport:     &mockIntentTransport{},
	})
	require.NoError(t, err)
	defer pub.Close()

	tests := []struct {
		name    string
		req     PublishIntentRequest
		wantErr string
	}{
		{name: "missing domain", req: PublishIntentRequest{Coordinate: "x", IntentID: "y", OrgID: "z"}, wantErr: "domain is required"},
		{name: "missing coordinate", req: PublishIntentRequest{Domain: "x", IntentID: "y", OrgID: "z"}, wantErr: "coordinate"},
		{name: "missing intent_id", req: PublishIntentRequest{Domain: "x", Coordinate: "y", OrgID: "z"}, wantErr: "intent_id"},
		{name: "missing org_id", req: PublishIntentRequest{Domain: "x", Coordinate: "y", IntentID: "z"}, wantErr: "org_id"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pub.BuildIntentEvent(tt.req)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

// --- test helpers ---

type mockIntentTransport struct {
	publishFunc   func(context.Context, nostr.Event) ([]ContextVMPublishResult, error)
	subscribeFunc func(context.Context, []nostr.Filter) (*ContextVMSubscription, error)
}

func (m *mockIntentTransport) Publish(ctx context.Context, ev nostr.Event) (int, error) {
	if m.publishFunc == nil {
		return 0, nil
	}
	results, err := m.publishFunc(ctx, ev)
	accepted := 0
	for _, r := range results {
		if r.Accepted {
			accepted++
		}
	}
	return accepted, err
}

func (m *mockIntentTransport) PublishWithResults(ctx context.Context, ev nostr.Event) ([]ContextVMPublishResult, error) {
	if m.publishFunc == nil {
		return nil, nil
	}
	return m.publishFunc(ctx, ev)
}

func (m *mockIntentTransport) SubscribeOperator(ctx context.Context, filters []nostr.Filter) (*ContextVMSubscription, error) {
	if m.subscribeFunc == nil {
		events := make(chan *nostr.Event)
		return &ContextVMSubscription{
			Events:            events,
			EndOfStoredEvents: make(chan struct{}),
			closeFn:           func() {},
		}, nil
	}
	return m.subscribeFunc(ctx, filters)
}

func (m *mockIntentTransport) Close() {}

// buildTestStatusEvent creates a 30315 status event matching the daemon's
// IntentStatusPublisher format.
func buildTestStatusEvent(t *testing.T, signer keyer.KeySigner, actor, intentID, coordinate, status, result, reason string) nostr.Event {
	t.Helper()

	dTag := fmt.Sprintf("intent-status:%s:%s", actor, coordinate)

	content := map[string]interface{}{
		"intent_id":  intentID,
		"coordinate": coordinate,
		"result":     result,
	}
	if reason != "" {
		content["reason"] = reason
	}
	contentJSON, _ := json.Marshal(content)

	ev := nostr.Event{
		Kind:      30315,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", dTag},
			{"domain", "intent"},
			{"status", status},
			{"t", "intent-status"},
			{"p", actor},
			{"intent_id", intentID},
		},
		Content: string(contentJSON),
	}

	err := signer.SignEvent(context.Background(), &ev)
	require.NoError(t, err)
	return ev
}

// captureHandler is a domain handler that captures intents for verification.
type captureHandler struct {
	mu      sync.Mutex
	intents []*controlplane.Intent
}

func (h *captureHandler) HandleIntent(_ context.Context, intent *controlplane.Intent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.intents = append(h.intents, intent)
	return nil
}

func (h *captureHandler) PermissionFor(_ string) domain.Permission {
	return domain.PermWriteServices
}

func openIntentTestStore(t *testing.T) *localstore.Store {
	t.Helper()
	dir := t.TempDir()
	store, err := localstore.Open(dir + "/test.db")
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	return store
}
