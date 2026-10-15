package signet

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
)

func TestExternalSignetReplyEnvelopeMatchesRecipientFilter(t *testing.T) {
	data, err := os.ReadFile("../../../web/tests/fixtures/external-go-envelopes.json")
	require.NoError(t, err)
	var fixtures map[string]struct {
		Kind int        `json:"kind"`
		Tags nostr.Tags `json:"tags"`
	}
	require.NoError(t, json.Unmarshal(data, &fixtures))
	fixture := fixtures["signet_reply"]
	require.Equal(t, nostr.Tags{{"p", strings.Repeat("a", 64)}}, fixture.Tags)
	recipient := nostr.MustPubKeyFromHex(strings.Repeat("a", 64))
	filter := signetReplyFilter(recipient, 0)
	event := nostr.Event{Kind: nostr.Kind(fixture.Kind), Tags: fixture.Tags}
	require.True(t, filter.Matches(event))
	filter.Tags["p"] = []string{strings.Repeat("b", 64)}
	require.False(t, filter.Matches(event))
}
