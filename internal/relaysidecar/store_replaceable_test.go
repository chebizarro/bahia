package relaysidecar

import (
	"errors"
	"fmt"
	"testing"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore"
	"github.com/stretchr/testify/require"
)

func TestSQLiteReplaceNIP01ArrivalOrderAndRestart(t *testing.T) {
	for _, kind := range []nostr.Kind{0, 3, 10002, 11316, 30315, 30900, 30078} {
		for _, reverse := range []bool{false, true} {
			t.Run(fmt.Sprint(kind)+map[bool]string{false: "/low-first", true: "/high-first"}[reverse], func(t *testing.T) {
				dir := t.TempDir()
				store, err := newSQLiteStore(dir)
				require.NoError(t, err)
				first := nostr.Event{Kind: kind, CreatedAt: 100, Tags: nostr.Tags{{"d", "same"}}, Content: `{"deleted":true}`}
				second := first
				second.Content = `{}`
				secret := nostr.SecretKey{1}
				require.NoError(t, first.Sign(secret))
				require.NoError(t, second.Sign(secret))
				low, high := first, second
				if low.ID.Hex() > high.ID.Hex() {
					low, high = high, low
				}
				order := []nostr.Event{low, high}
				if reverse {
					order = []nostr.Event{high, low}
				}
				for _, ev := range order {
					err := store.Replace(t.Context(), ev)
					require.True(t, err == nil || errors.Is(err, eventstore.ErrDupEvent), "%v", err)
				}
				require.ErrorIs(t, store.Replace(t.Context(), low), eventstore.ErrDupEvent)
				require.ErrorIs(t, store.Replace(t.Context(), high), eventstore.ErrDupEvent)
				require.NoError(t, store.Close())
				store, err = newSQLiteStore(dir)
				require.NoError(t, err)
				defer store.Close()
				var events []nostr.Event
				for ev := range store.Query(t.Context(), nostr.Filter{Kinds: []nostr.Kind{kind}}, 10) {
					events = append(events, ev)
				}
				require.Len(t, events, 1)
				require.Equal(t, low.ID, events[0].ID)
				newer := high
				newer.CreatedAt++
				require.NoError(t, newer.Sign(secret))
				require.NoError(t, store.Replace(t.Context(), newer))
				require.ErrorIs(t, store.Replace(t.Context(), low), eventstore.ErrDupEvent)
			})
		}
	}
}
