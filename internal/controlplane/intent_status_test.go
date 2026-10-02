package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"fiatjaf.com/nostr"
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
