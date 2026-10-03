package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// --- F1 acceptance test (a): level-triggered reconcile ---
// An update intent without a prior create produces the correct state.
func TestIntentProcessor_LevelTriggeredReconcile(t *testing.T) {
	store := openTestStore(t)
	handler := &testDomainHandler{}
	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey}),
	)

	proc := NewIntentProcessor(ts, store, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("test", handler)

	// Publish an update intent without a prior create. Level-triggered
	// processing should handle this: the full desired state is in the content.
	intent := testIntent(t, "update", "entity-123")

	err := proc.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, handler.handled, 1)
	assert.Equal(t, "update", handler.handled[0].Op)
	assert.Equal(t, "entity-123", handler.handled[0].Coordinate)

	// The content is the full desired state.
	assert.Equal(t, "test-service", handler.handled[0].Content["name"])
}

// --- F1 acceptance test (b): bounded status ---
// Two intents for the same entity from one requester produce only one status
// event on the relay.
func TestIntentProcessor_BoundedStatus(t *testing.T) {
	store := openTestStore(t)
	published := &statusCollector{}
	signer := &testSigner{}

	statusPub := NewIntentStatusPublisher(published.publish, signer, zap.NewNop())

	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey}),
	)

	proc := NewIntentProcessor(ts, store, statusPub,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("test", &testDomainHandler{})

	// First intent.
	intent1 := testIntent(t, "create", "entity-456")
	intent1.IntentID = "intent-aaa"
	err := proc.ProcessInProcess(context.Background(), intent1)
	require.NoError(t, err)

	// Second intent for the same entity and requester.
	intent2 := testIntent(t, "update", "entity-456")
	intent2.IntentID = "intent-bbb"
	err = proc.ProcessInProcess(context.Background(), intent2)
	require.NoError(t, err)

	// Both intents were accepted.
	require.Len(t, published.events, 2)

	// Both status events have the same d-tag (bounded per requester + entity).
	dTag1 := extractDTag(published.events[0])
	dTag2 := extractDTag(published.events[1])
	assert.Equal(t, dTag1, dTag2, "d-tags should be identical for same requester and entity")
	assert.Contains(t, dTag1, "intent-status:")
	assert.Contains(t, dTag1, "entity-456")
}

// --- F1 acceptance test (c): in-process path shares idempotency ---
func TestIntentProcessor_InProcessSharesIdempotency(t *testing.T) {
	store := openTestStore(t)
	handler := &testDomainHandler{}
	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey}),
	)

	proc := NewIntentProcessor(ts, store, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("test", handler)

	// Process an intent via in-process path.
	intent := testIntent(t, "create", "entity-789")
	intent.IntentID = "shared-idempotency-key"

	err := proc.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err)
	require.Len(t, handler.handled, 1)

	// Process the same intent again (simulating relay delivery of the same
	// intent). The idempotency store should deduplicate.
	err = proc.ProcessRelayIntent(context.Background(), makeIntentEvent(t, "create", "entity-789", "shared-idempotency-key"))
	require.NoError(t, err)

	// Handler should not have been called again.
	assert.Len(t, handler.handled, 1, "second delivery must be deduplicated")
}

// --- F1 acceptance test (d): author-scoped subscription and silent drop ---
func TestIntentProcessor_SilentDropUntrustedAuthor(t *testing.T) {
	store := openTestStore(t)
	handler := &testDomainHandler{}

	// TrustSet only knows testPubkey.
	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey}),
	)

	published := &statusCollector{}
	signer := &testSigner{}
	statusPub := NewIntentStatusPublisher(published.publish, signer, zap.NewNop())

	proc := NewIntentProcessor(ts, store, statusPub,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("test", handler)

	// Intent from untrusted author.
	intent := testIntent(t, "create", "entity-untrusted")
	intent.Actor = "unknown_pubkey_aaaa000000000000000000000000000000000000000000000000"

	err := proc.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err) // silent drop, no error

	// Handler should not have been called.
	assert.Empty(t, handler.handled)

	// No rejection status published (silent drop for unknown authors).
	assert.Empty(t, published.events)
}

func TestIntentProcessor_KnownPrincipalInsufficientPermission(t *testing.T) {
	store := openTestStore(t)
	handler := &testDomainHandler{}

	// TrustSet: fleet operator (not an org member).
	fleetPK := "abcd000000000000000000000000000000000000000000000000000000000099"
	ts := NewTrustSet([]string{fleetPK}, zap.NewNop())

	published := &statusCollector{}
	signer := &testSigner{}
	statusPub := NewIntentStatusPublisher(published.publish, signer, zap.NewNop())

	proc := NewIntentProcessor(ts, store, statusPub,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("test", handler)

	// Intent from fleet operator (known but not an org member).
	intent := testIntent(t, "create", "entity-noauth")
	intent.Actor = fleetPK

	err := proc.ProcessInProcess(context.Background(), intent)
	require.Error(t, err) // authorization failure

	// Handler not called.
	assert.Empty(t, handler.handled)

	// Rejection status was published (known principal).
	require.Len(t, published.events, 1)
	assert.Contains(t, published.events[0].Content, "rejected")
}

func TestIntentProcessor_DisabledDomainIgnored(t *testing.T) {
	store := openTestStore(t)
	handler := &testDomainHandler{}
	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey}),
	)

	proc := NewIntentProcessor(ts, store, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"other": true}},
		zap.NewNop(),
	)
	proc.RegisterHandler("test", handler)

	intent := testIntent(t, "create", "entity-disabled")
	err := proc.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err)
	assert.Empty(t, handler.handled)
}

func TestIntentProcessor_NoHandlerRegistered(t *testing.T) {
	store := openTestStore(t)
	ts := NewTrustSet(nil, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey}),
	)

	proc := NewIntentProcessor(ts, store, nil,
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true}},
		zap.NewNop(),
	)
	// No handler registered for "test".

	intent := testIntent(t, "create", "entity-nohandler")
	err := proc.ProcessInProcess(context.Background(), intent)
	require.NoError(t, err) // silently ignored
}

func TestParseIntent_Valid(t *testing.T) {
	ev := makeIntentEvent(t, "create", "svc-123", "intent-001")
	intent, err := ParseIntent(ev)
	require.NoError(t, err)
	assert.Equal(t, "test", intent.Domain)
	assert.Equal(t, "create", intent.Op)
	assert.Equal(t, "svc-123", intent.Coordinate)
	assert.Equal(t, "intent-001", intent.IntentID)
	assert.NotEqual(t, uuid.Nil, intent.OrgID)
}

func TestParseIntent_StringCanonicalRevision(t *testing.T) {
	ev := makeIntentEvent(t, "update", "svc-123", "intent-string-revision")
	ev.Content = `{"id":"svc-123","expected_updated_at":"2026-10-03T09:12:13.123456Z"}`
	intent, err := ParseIntent(ev)
	require.NoError(t, err)
	require.NotNil(t, intent.ExpectedUpdatedAt)
	assert.Equal(t, time.Date(2026, 10, 3, 9, 12, 13, 123456000, time.UTC), *intent.ExpectedUpdatedAt)
}

func TestParseIntent_RejectsInvalidRevision(t *testing.T) {
	for _, raw := range []string{`42`, `42.5`, `null`, `"2026-10-03"`, `"not-a-time"`} {
		t.Run(raw, func(t *testing.T) {
			ev := makeIntentEvent(t, "update", "svc-123", "intent-invalid-revision")
			ev.Content = `{"expected_updated_at":` + raw + `}`
			_, err := ParseIntent(ev)
			require.ErrorContains(t, err, "invalid expected_updated_at")
		})
	}
}

func TestIntentProcessor_InvalidRevisionPublishesBoundedRejection(t *testing.T) {
	statuses := &statusCollector{}
	_, ownerPub := testNostrKeypair()
	trust := NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{testOrgID().String(): ownerPub}))
	processor := NewIntentProcessor(trust, openTestStore(t), NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"test": true}}, zap.NewNop())
	processor.RegisterHandler("test", &testDomainHandler{})
	event := makeIntentEvent(t, "update", "svc-123", "intent-invalid-revision-status")
	event.PubKey = testNostrPubKeyFromHex(t, ownerPub)
	event.Content = `{"id":"svc-123","expected_updated_at":42}`
	event.ID = event.GetID()
	require.ErrorContains(t, processor.ProcessRelayIntent(context.Background(), event), "invalid expected_updated_at")
	require.Len(t, statuses.events, 1)
	assert.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
	assert.Equal(t, "intent-status:"+ownerPub+":svc-123", extractDTag(statuses.events[0]))
}

func TestSignedUnwrappedIntent_InvalidRevisionPublishesBoundedRejection(t *testing.T) {
	privateKey, actor := testNostrKeypair()
	statuses := &statusCollector{}
	processor := NewIntentProcessor(NewTrustSet([]string{actor}, zap.NewNop()), openTestStore(t),
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"secret": true}}, zap.NewNop())
	ingress := NewIntentGiftWrapIngress(IntentGiftWrapIngressConfig{Processor: processor, SensitiveDomains: []string{"secret"}, Logger: zap.NewNop()})
	event := buildSignedIntentEvent(t, privateKey, testOrgID(), "invalid-secret-revision", "secret", "update",
		"bahia.intent.secret.v1", `{"id":"`+testOrgID().String()+`","expected_updated_at":42}`)
	require.ErrorContains(t, ingress.ProcessUnwrappedIntent(context.Background(), event), "invalid expected_updated_at")
	require.Len(t, statuses.events, 1)
	assert.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
	assert.Equal(t, "intent-status:"+actor+":"+testOrgID().String(), extractDTag(statuses.events[0]))
}

func TestIntentRevisionMatchesCanonicalPrecision(t *testing.T) {
	record := time.Date(2026, 10, 3, 9, 12, 13, 123456789, time.UTC)
	expected := time.Date(2026, 10, 3, 9, 12, 13, 123456000, time.UTC)
	intent := &Intent{ExpectedUpdatedAt: &expected}
	assert.True(t, intent.RevisionMatches(record))
	older := expected.Add(-time.Microsecond)
	intent.ExpectedUpdatedAt = &older
	assert.False(t, intent.RevisionMatches(record))
}

func TestRegisteredDomainIntentRevisionWireContract(t *testing.T) {
	canonical := time.Date(2026, 10, 3, 9, 12, 13, 123456000, time.UTC)
	for _, name := range []string{"backup", "deployment", "dns", "environment", "llm", "ml", "notification", "org", "package", "policy", "runtime", "secret", "service", "worker"} {
		t.Run(name, func(t *testing.T) {
			event := makeIntentEvent(t, "update", "entity-1", "revision-"+name)
			event.Tags[1][1] = name
			event.Tags[2][1] = "bahia.intent." + name + ".v1"
			event.Content = `{"expected_updated_at":"` + canonical.Format(time.RFC3339Nano) + `"}`
			intent, err := ParseIntent(event)
			require.NoError(t, err)
			assert.True(t, intent.RevisionMatches(canonical), "canonical record revision must match")
			event.Content = `{"expected_updated_at":"` + canonical.Add(-time.Second).Format(time.RFC3339Nano) + `"}`
			stale, err := ParseIntent(event)
			require.NoError(t, err)
			assert.False(t, stale.RevisionMatches(canonical), "older canonical revision must conflict")
		})
	}
}

func TestParseIntent_MissingBahiaIntentTag(t *testing.T) {
	ev := &nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", "svc-123"},
			{"domain", "test"},
			{"intent_id", "intent-001"},
			{"org", testOrgID().String()},
		},
		Content: `{"name":"test"}`,
	}
	_, err := ParseIntent(ev)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "bahia-intent")
}

func TestParseIntent_WrongKind(t *testing.T) {
	ev := &nostr.Event{Kind: 1, Tags: nostr.Tags{{"t", "bahia-intent"}}}
	_, err := ParseIntent(ev)
	assert.Error(t, err)
}

func TestBuildEnabledDomains(t *testing.T) {
	m := BuildEnabledDomains([]string{"Service", "environment"})
	assert.True(t, m["service"])
	assert.True(t, m["environment"])
	assert.False(t, m["policy"])
}

// --- test helpers ---

const testPubkey = "abcd000000000000000000000000000000000000000000000000000000000001"

var testOrgIDValue = uuid.MustParse("11111111-1111-1111-1111-111111111111")

func testOrgID() uuid.UUID { return testOrgIDValue }

func testIntent(t *testing.T, op, coordinate string) *Intent {
	t.Helper()
	content := map[string]interface{}{
		"id":   "entity-id-" + coordinate,
		"name": "test-service",
	}
	return &Intent{
		Domain:     "test",
		Op:         op,
		Schema:     "bahia.intent.test.v1",
		OrgID:      testOrgID(),
		IntentID:   fmt.Sprintf("intent-%s-%s", op, coordinate),
		Coordinate: coordinate,
		Content:    content,
		Actor:      testPubkey,
	}
}

func makeIntentEvent(t *testing.T, op, coordinate, intentID string) *nostr.Event {
	t.Helper()
	content, _ := json.Marshal(map[string]interface{}{
		"id":   "entity-id-" + coordinate,
		"name": "test-service",
	})
	ev := &nostr.Event{
		Kind:      30900,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", coordinate},
			{"domain", "test"},
			{"schema", "bahia.intent.test.v1"},
			{"t", "bahia-intent"},
			{"op", op},
			{"org", testOrgID().String()},
			{"intent_id", intentID},
		},
		Content: string(content),
	}
	ev.ID = ev.GetID()
	return ev
}

func openTestStore(t *testing.T) *localstore.Store {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test-local.db")
	store, err := localstore.Open(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		if err := store.Close(); err != nil && !os.IsNotExist(err) {
			t.Logf("close test store: %v", err)
		}
	})
	return store
}

type testDomainHandler struct {
	mu      sync.Mutex
	handled []*Intent
}

func (h *testDomainHandler) HandleIntent(_ context.Context, intent *Intent) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.handled = append(h.handled, intent)
	return nil
}

func (h *testDomainHandler) PermissionFor(op string) domain.Permission {
	switch op {
	case "delete":
		return domain.PermWriteServices
	default:
		return domain.PermWriteServices
	}
}

type statusCollector struct {
	mu     sync.Mutex
	events []nostr.Event
}

func (c *statusCollector) publish(_ context.Context, ev nostr.Event) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return nil
}

type testSigner struct{}

func (s *testSigner) GetPublicKey(_ context.Context) (nostr.PubKey, error) {
	return nostr.ZeroPK, nil
}

func (s *testSigner) SignEvent(_ context.Context, ev *nostr.Event) error {
	ev.ID = ev.GetID()
	return nil
}

func extractDTag(ev nostr.Event) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "d" {
			return tag[1]
		}
	}
	return ""
}
