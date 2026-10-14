package controlplane

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestIntentStatusSerializedEnvelopeMatchesReaderFilter(t *testing.T) {
	data, err := os.ReadFile("../../web/tests/fixtures/intent-status-envelopes.json")
	require.NoError(t, err)
	var fixtures []struct {
		Domain     string     `json:"domain"`
		Coordinate string     `json:"coordinate"`
		IntentID   string     `json:"intent_id"`
		Status     string     `json:"status"`
		Tags       nostr.Tags `json:"tags"`
	}
	require.NoError(t, json.Unmarshal(data, &fixtures))
	require.Len(t, fixtures, 4)
	const actor = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	publisher := NewIntentStatusPublisher(func(_ context.Context, _ nostr.Event) error { return nil }, &testSigner{}, zap.NewNop())
	for _, fixture := range fixtures {
		t.Run(fixture.Domain+"-"+fixture.Status, func(t *testing.T) {
			intent := testIntent(t, "create", fixture.Coordinate)
			intent.Domain, intent.Actor, intent.IntentID = fixture.Domain, actor, fixture.IntentID
			event, err := publisher.buildStatusEventWithExpiryAt(t.Context(), intent, fixture.Status, fixture.Status, "", nil, 0, 123)
			require.NoError(t, err)
			require.Equal(t, nostr.Kind(30315), event.Kind)
			wireTags, err := json.Marshal(event.Tags)
			require.NoError(t, err)
			expectedTags, err := json.Marshal(fixture.Tags)
			require.NoError(t, err)
			require.Equal(t, string(expectedTags), string(wireTags))
			filter := nostr.Filter{Kinds: []nostr.Kind{30315}, Tags: nostr.TagMap{
				"d": {fixture.Tags[0][1]}, "p": {actor}, "t": {kinds.IntentStatusTopic},
			}}
			require.True(t, filter.Matches(event))
			filter.Tags["d"] = []string{"intent-status:wrong"}
			require.False(t, filter.Matches(event))
		})
	}
}

func TestIntentRequestSerializedEnvelopeMatchesSubscriberFilter(t *testing.T) {
	data, err := os.ReadFile("../../web/tests/fixtures/intent-go.json")
	require.NoError(t, err)
	var event nostr.Event
	require.NoError(t, json.Unmarshal(data, &event))
	const actor = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	event.PubKey = nostr.MustPubKeyFromHex(actor)
	subscriber := &IntentSubscriber{trustSet: NewTrustSet(nil, zap.NewNop()), selfPubkey: actor}
	filter := subscriber.buildFilter()
	require.True(t, filter.Matches(event))
	require.Equal(t, nostr.TagMap{"t": {kinds.IntentTopic}}, filter.Tags)
	wireTags, err := json.Marshal(event.Tags)
	require.NoError(t, err)
	var fixture struct {
		Tags nostr.Tags `json:"tags"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	expectedTags, err := json.Marshal(fixture.Tags)
	require.NoError(t, err)
	require.Equal(t, string(expectedTags), string(wireTags))
	filter.Tags["t"] = []string{"wrong-intent-topic"}
	require.False(t, filter.Matches(event))
}
