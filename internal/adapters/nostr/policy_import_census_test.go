package nostr

import (
	"context"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/stretchr/testify/require"
)

func TestPolicyCoordinateCensusRelayWinsWithColdCache(t *testing.T) {
	first := startSyncTestRelay(t, syncTestRelayOptions{})
	second := startSyncTestRelay(t, syncTestRelayOptions{})
	pool := newSyncTestPool(first, second)
	defer pool.Close()
	key := gonostr.Generate()
	id := uuid.New()
	ev := syncTestEvent(t, key, gonostr.Kind(kinds.CASControlState), gonostr.Now(), gonostr.Tags{
		{"d", id.String()}, {"t", kinds.CPStateTopicPolicyRegistry}, {"domain", "policy"},
	}, `{"id":"relay-version"}`)
	second.add(t, ev)
	ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
	defer cancel()
	result, err := CensusPolicyCoordinate(ctx, pool, key.Public(), id)
	require.NoError(t, err)
	require.Equal(t, []string{ev.ID.Hex()}, result.EventIDs, "a cold local cache cannot override a relay-held coordinate")
	require.Len(t, result.Relays, 2)
	other, err := CensusPolicyCoordinate(ctx, pool, key.Public(), uuid.New())
	require.NoError(t, err)
	require.Empty(t, other.EventIDs)
}

func TestPolicyCoordinateCensusRefusesIncompleteRelayHistory(t *testing.T) {
	key := gonostr.Generate()
	id := uuid.New()
	for name, configure := range map[string]func(*syncTestRelay){
		"closed": func(r *syncTestRelay) {
			r.relay.OnRequest = func(context.Context, gonostr.Filter) (bool, string) {
				return true, "blocked: denied"
			}
		},
		"unavailable": func(r *syncTestRelay) { r.down.Store(true) },
	} {
		t.Run(name, func(t *testing.T) {
			up := startSyncTestRelay(t, syncTestRelayOptions{})
			bad := startSyncTestRelay(t, syncTestRelayOptions{})
			configure(bad)
			pool := newSyncTestPool(up, bad)
			defer pool.Close()
			ctx, cancel := context.WithTimeout(t.Context(), syncTestTimeout)
			defer cancel()
			_, err := CensusPolicyCoordinate(ctx, pool, key.Public(), id)
			require.Error(t, err)
		})
	}
}
