package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestIntentStatusPublisher_Accepted(t *testing.T) {
	published := &statusCollector{}
	signer := &testSigner{}
	pub := NewIntentStatusPublisher(published.publish, signer, zap.NewNop())

	intent := testIntent(t, "create", "svc-100")
	pub.PublishAccepted(context.Background(), intent)

	require.Len(t, published.events, 1)
	ev := published.events[0]

	assert.Equal(t, 30315, int(ev.Kind))

	// Check d-tag is bounded per requester + entity.
	dTag := extractDTag(ev)
	assert.Contains(t, dTag, "intent-status:")
	assert.Contains(t, dTag, intent.Actor)
	assert.Contains(t, dTag, intent.Coordinate)

	// Check status tag.
	var statusTag string
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "status" {
			statusTag = tag[1]
		}
	}
	assert.Equal(t, "accepted", statusTag)

	// Check content.
	var content map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(ev.Content), &content))
	assert.Equal(t, intent.IntentID, content["intent_id"])
	assert.Equal(t, "applied", content["result"])

	// Check NIP-40 expiration tag exists.
	var hasExpiration bool
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "expiration" {
			hasExpiration = true
		}
	}
	assert.True(t, hasExpiration, "status event must have NIP-40 expiration tag")
}

func TestIntentStatusPublisher_Rejection(t *testing.T) {
	published := &statusCollector{}
	signer := &testSigner{}
	pub := NewIntentStatusPublisher(published.publish, signer, zap.NewNop())

	intent := testIntent(t, "create", "svc-200")
	pub.PublishRejection(context.Background(), intent, "permission denied")

	require.Len(t, published.events, 1)
	ev := published.events[0]

	var statusTag string
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "status" {
			statusTag = tag[1]
		}
	}
	assert.Equal(t, "rejected", statusTag)

	var content map[string]interface{}
	require.NoError(t, json.Unmarshal([]byte(ev.Content), &content))
	assert.Equal(t, "rejected", content["result"])
	assert.Equal(t, "permission denied", content["reason"])
}

func TestSecretIntentStatusSignedEventExposesOnlyRequestCorrelation(t *testing.T) {
	published := &statusCollector{}
	pub := NewIntentStatusPublisher(published.publish, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
	serviceID := "11111111-2222-7333-8444-555555555555"
	secretID := "aaaaaaaa-bbbb-7ccc-8ddd-eeeeeeeeeeee"
	intent := testIntent(t, "update", secretID)
	intent.Domain = "secret"
	intent.IntentID = "request-correlation"
	intent.Coordinate = serviceID + ":" + secretID
	intent.Event = &nostr.Event{ID: testNostrID("secret-request")}
	intent.StatusData = map[string]any{"service_id": serviceID, "diagnostic": "signet transport detail"}

	pub.PublishRejection(t.Context(), intent, "signet transport detail for service "+serviceID)
	require.Len(t, published.events, 1)
	ev := published.events[0]
	require.True(t, ev.CheckID())
	require.True(t, ev.VerifySignature())
	wire, err := json.Marshal(ev)
	require.NoError(t, err)
	for _, private := range []string{serviceID, secretID, "signet transport detail"} {
		assert.NotContains(t, string(wire), private)
	}
	assert.Equal(t, "intent-status:"+intent.Actor+":secret:"+intent.IntentID, extractTag(ev, "d"))
	assert.Equal(t, intent.IntentID, extractTag(ev, "intent_id"))
	assert.Equal(t, intent.Event.ID.Hex(), extractTag(ev, "e"))
	var content map[string]any
	require.NoError(t, json.Unmarshal([]byte(ev.Content), &content))
	assert.Equal(t, "invalid_request", content["reason"])
	assert.NotContains(t, content, "coordinate")
	assert.NotContains(t, content, "data")
}

func TestSecretIntentStatusUsesEventIDWhenIntentIDIsMissing(t *testing.T) {
	pub := NewIntentStatusPublisher((&statusCollector{}).publish, keyer.NewPlainKeySigner(nostr.Generate()), zap.NewNop())
	intent := testIntent(t, "create", "service-private-coordinate")
	intent.Domain = "secret"
	intent.IntentID = ""
	intent.Event = &nostr.Event{ID: testNostrID("malformed-secret-request")}
	ev, err := pub.buildStatusEvent(t.Context(), intent, "rejected", "rejected", "malformed private request", nil)
	require.NoError(t, err)
	assert.Equal(t, "intent-status:"+intent.Actor+":secret:"+intent.Event.ID.Hex(), extractTag(ev, "d"))
	assert.Equal(t, intent.Event.ID.Hex(), extractTag(ev, "e"))
	intent.Event = nil
	_, err = pub.buildStatusEvent(t.Context(), intent, "rejected", "rejected", "malformed private request", nil)
	require.ErrorContains(t, err, "requires request correlation")
}

func TestIntentStatusPublisher_Conflict(t *testing.T) {
	published := &statusCollector{}
	signer := &testSigner{}
	pub := NewIntentStatusPublisher(published.publish, signer, zap.NewNop())

	intent := testIntent(t, "update", "svc-300")
	pub.PublishConflict(context.Background(), intent)

	require.Len(t, published.events, 1)
	ev := published.events[0]
	var statusTag string
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == "status" {
			statusTag = tag[1]
		}
	}
	assert.Equal(t, "conflict", statusTag)
}

func TestIntentStatusPublisher_BoundedDTag(t *testing.T) {
	published := &statusCollector{}
	signer := &testSigner{}
	pub := NewIntentStatusPublisher(published.publish, signer, zap.NewNop())

	// Two intents from the same actor for the same entity.
	intent1 := testIntent(t, "create", "svc-400")
	intent1.IntentID = "id-1"
	pub.PublishAccepted(context.Background(), intent1)

	intent2 := testIntent(t, "update", "svc-400")
	intent2.IntentID = "id-2"
	pub.PublishAccepted(context.Background(), intent2)

	require.Len(t, published.events, 2)

	// Both should have the same d-tag.
	d1 := extractDTag(published.events[0])
	d2 := extractDTag(published.events[1])
	assert.Equal(t, d1, d2, "d-tag must be bounded per (requester, entity)")

	// But different intent_id tags.
	intentID1 := extractTag(published.events[0], "intent_id")
	intentID2 := extractTag(published.events[1], "intent_id")
	assert.NotEqual(t, intentID1, intentID2)
}

func TestIntentStatusPublisher_NilPublishIsNoop(t *testing.T) {
	pub := NewIntentStatusPublisher(nil, nil, zap.NewNop())
	pub.PublishAccepted(context.Background(), testIntent(t, "create", "svc"))
	// No panic.
}

func extractTag(ev nostr.Event, key string) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}

func TestIntentStatusPublisher_OutcomeSizeBound(t *testing.T) {
	published := &statusCollector{}
	pub := NewIntentStatusPublisher(published.publish, &testSigner{}, zap.NewNop())
	intent := testIntent(t, "evaluate", "evaluation:artifact:environment")
	intent.Evaluation = &domain.PolicyEvaluation{Results: []domain.PolicyResult{{PolicyName: strings.Repeat("x", 17*1024)}}}
	err := pub.PublishAcceptedEvaluation(context.Background(), intent)
	require.ErrorContains(t, err, "16 KiB")
	require.Empty(t, published.events)
}
