package gitea

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func TestExternalLoomJobEnvelopeMatchesGiteaConfirmationFilter(t *testing.T) {
	data, err := os.ReadFile("../../../web/tests/fixtures/external-go-envelopes.json")
	require.NoError(t, err)
	var fixtures map[string]struct {
		Kind int        `json:"kind"`
		Tags nostr.Tags `json:"tags"`
	}
	require.NoError(t, json.Unmarshal(data, &fixtures))
	fixture := fixtures["gitea_loom_job"]
	require.Equal(t, kinds.LoomJobRequest, fixture.Kind)
	require.Equal(t, "e", fixture.Tags[0][0])
	require.Equal(t, strings.Repeat("c", 64), fixture.Tags[0][1])
	author := nostr.MustPubKeyFromHex(strings.Repeat("a", 64))
	filter := loomJobConfirmationFilter(author, strings.Repeat("c", 64))
	event := nostr.Event{Kind: nostr.Kind(fixture.Kind), PubKey: author, Tags: fixture.Tags}
	require.True(t, filter.Matches(event))
	filter.Tags["e"] = []string{strings.Repeat("d", 64)}
	require.False(t, filter.Matches(event))
}
