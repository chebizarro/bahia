package nostr

import (
	"fmt"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestScanStoredPagesOverlapsSameSecondAndProvesEmptyPage(t *testing.T) {
	relay := newMaxLimitRelay(t, 5)
	secret := nostr.Generate()
	newest := nostr.Now() - 60
	for i := range 14 {
		ev := nostr.Event{Kind: 1, CreatedAt: newest - nostr.Timestamp(i/2), Content: fmt.Sprintf("history %d", i)}
		require.NoError(t, ev.Sign(secret))
		_, err := relay.relay.AddEvent(t.Context(), ev)
		require.NoError(t, err)
	}
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	defer pool.Close()
	ctx := testDeadlineContext(t)
	pool.Connect(ctx)
	events, proof, err := pool.ScanStoredPages(ctx, relay.url, nostr.Filter{Kinds: []nostr.Kind{1}})
	require.NoError(t, err)
	require.Len(t, events, 14)
	require.Equal(t, 14, proof.UniqueIDs)
	require.Greater(t, proof.Pages, 3)
	require.True(t, proof.FinalEmptyPage)
	require.Equal(t, 5, proof.NIP11MaxLimit)
	require.Equal(t, 5, proof.PageLimit)
	require.Equal(t, int64(newest), proof.MaxCreatedAt)
	require.Equal(t, int64(newest-6), proof.MinCreatedAt)
	require.Len(t, proof.Windows, proof.Pages)
	require.Zero(t, proof.Windows[len(proof.Windows)-1].Events)
	overlapped := false
	for _, page := range proof.Windows {
		overlapped = overlapped || page.NewIDs < page.Events
	}
	require.True(t, overlapped, "inclusive until must deduplicate a boundary second")
	for _, req := range relay.requests() {
		require.LessOrEqual(t, req.Limit, 5)
	}
}

func TestScanStoredPagesFailsClosedOnSaturatedSecond(t *testing.T) {
	relay := newMaxLimitRelay(t, 5)
	secret := nostr.Generate()
	for i := range 7 {
		ev := nostr.Event{Kind: 1, CreatedAt: nostr.Now() - 60, Content: fmt.Sprintf("same second %d", i)}
		require.NoError(t, ev.Sign(secret))
		_, err := relay.relay.AddEvent(t.Context(), ev)
		require.NoError(t, err)
	}
	pool := NewRelayPool([]string{relay.url}, zap.NewNop())
	defer pool.Close()
	ctx := testDeadlineContext(t)
	pool.Connect(ctx)
	_, proof, err := pool.ScanStoredPages(ctx, relay.url, nostr.Filter{Kinds: []nostr.Kind{1}})
	require.ErrorContains(t, err, "timestamp boundary")
	require.False(t, proof.FinalEmptyPage)
}
