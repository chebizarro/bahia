package controlplane

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func TestRelaySettingsSerializedEnvelopeMatchesReaderFilter(t *testing.T) {
	data, err := os.ReadFile("../../web/tests/fixtures/relay-settings-envelope.json")
	require.NoError(t, err)
	var fixture struct {
		Kind int        `json:"kind"`
		Tags nostr.Tags `json:"tags"`
	}
	require.NoError(t, json.Unmarshal(data, &fixture))
	tags := relaySettingsStateTags("applied", strings.Repeat("f", 64))
	actual, err := json.Marshal(tags)
	require.NoError(t, err)
	expected, err := json.Marshal(fixture.Tags)
	require.NoError(t, err)
	require.Equal(t, string(expected), string(actual))
	event := nostr.Event{Kind: nostr.Kind(fixture.Kind), Tags: tags, PubKey: nostr.MustPubKeyFromHex(strings.Repeat("d", 64))}
	filter := nostr.Filter{Kinds: []nostr.Kind{kinds.CASControlState}, Authors: []nostr.PubKey{event.PubKey}, Tags: nostr.TagMap{kinds.CASControlStateTagD: {kinds.RelaySettingsCoordinate}}}
	require.True(t, filter.Matches(event))
	filter.Tags[kinds.CASControlStateTagD] = []string{"wrong"}
	require.False(t, filter.Matches(event))
}
