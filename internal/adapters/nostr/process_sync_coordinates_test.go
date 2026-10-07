package nostr

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/stretchr/testify/require"
)

// ProcessSync over coordinates the bolt eventstore does not index
// (bahia-irsry.51): an empty d, a d over 100 bytes, and `a` coordinates over
// 100 bytes. Each case restarts the process against a second relay that holds
// a different version, as a lagging relay does, and checks what reaches Apply
// and what the store replays to a consumer hydrating on start.

func storedSyncIDs(t *testing.T, path string, filter gonostr.Filter) []gonostr.ID {
	t.Helper()
	store, err := localstore.Open(path)
	require.NoError(t, err)
	defer store.Close()
	return appliedIDs(slices.Collect(store.QueryEvents(filter)))
}

// syncFromRelayHolding runs ProcessSync once against a fresh relay holding
// events and returns what it applied.
func syncFromRelayHolding(t *testing.T, path string, filter gonostr.Filter, events ...gonostr.Event) []gonostr.ID {
	t.Helper()
	relay := startSyncTestRelay(t, syncTestRelayOptions{negentropy: true})
	relay.add(t, events...)
	run := startProcessSync(t, path, filter, nil, relay)
	run.waitReady(t)
	return appliedIDs(run.stop(t))
}

func TestProcessSyncKeepsTheLatestVersionOfUnindexedCoordinates(t *testing.T) {
	for _, tc := range []struct {
		name string
		tags gonostr.Tags
	}{
		{name: "empty d", tags: gonostr.Tags{{"d", ""}}},
		{name: "d over 100 bytes", tags: gonostr.Tags{{"d", strings.Repeat("d", 120)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sk := gonostr.Generate()
			now := gonostr.Now()
			filter := gonostr.Filter{Kinds: []gonostr.Kind{syncTestStateKind}, Authors: []gonostr.PubKey{sk.Public()}}
			older := syncTestEvent(t, sk, syncTestStateKind, now-600, tc.tags, "older")
			newer := syncTestEvent(t, sk, syncTestStateKind, now-60, tc.tags, "newer")

			t.Run("lagging relay after a restart", func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "process.bolt")
				require.Equal(t, []gonostr.ID{newer.ID}, syncFromRelayHolding(t, path, filter, newer))
				require.Empty(t, syncFromRelayHolding(t, path, filter, older), "an older version is not applied over the held one")
				require.Equal(t, []gonostr.ID{newer.ID}, storedSyncIDs(t, path, filter), "hydration replays only the latest version")
			})
			t.Run("newer version after a restart", func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "process.bolt")
				require.Equal(t, []gonostr.ID{older.ID}, syncFromRelayHolding(t, path, filter, older))
				require.Equal(t, []gonostr.ID{newer.ID}, syncFromRelayHolding(t, path, filter, newer))
				require.Equal(t, []gonostr.ID{newer.ID}, storedSyncIDs(t, path, filter), "the newer version replaces the older one")
			})
		})
	}
}

func TestProcessSyncHonoursDeletionOfLongCoordinatesInBothOrders(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    string
	}{
		{name: "coordinate over 100 bytes", d: strings.Repeat("c", 40)},
		{name: "d over 100 bytes", d: strings.Repeat("d", 120)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sk := gonostr.Generate()
			now := gonostr.Now()
			address := fmt.Sprintf("%d:%s:%s", syncTestStateKind, sk.Public().Hex(), tc.d)
			require.Greater(t, len(address), 100)
			filter := gonostr.Filter{Kinds: []gonostr.Kind{syncTestStateKind, gonostr.KindDeletion}, Authors: []gonostr.PubKey{sk.Public()}}
			target := syncTestEvent(t, sk, syncTestStateKind, now-600, gonostr.Tags{{"d", tc.d}}, "live")
			request := syncTestEvent(t, sk, gonostr.KindDeletion, now-300, gonostr.Tags{{"a", address}}, "")

			t.Run("deletion after the event", func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "process.bolt")
				require.Equal(t, []gonostr.ID{target.ID}, syncFromRelayHolding(t, path, filter, target))
				require.Equal(t, []gonostr.ID{request.ID}, syncFromRelayHolding(t, path, filter, request))
				require.Equal(t, []gonostr.ID{request.ID}, storedSyncIDs(t, path, filter), "hydration replays the tombstone, not the deleted event")
				require.Empty(t, syncFromRelayHolding(t, path, filter, target), "a lagging relay's copy of the deleted event is not applied")
			})
			t.Run("deletion before the event", func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "process.bolt")
				require.Equal(t, []gonostr.ID{request.ID}, syncFromRelayHolding(t, path, filter, request))
				require.Empty(t, syncFromRelayHolding(t, path, filter, target), "an event deleted before it arrived is not applied")
				require.Equal(t, []gonostr.ID{request.ID}, storedSyncIDs(t, path, filter))
			})
		})
	}
}
