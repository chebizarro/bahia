package nostr

import (
	"context"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// A live group's regular kinds resume from per-relay cursors in the local
// store (C-2, B-15): a relay that did not send EOSE keeps no cursor, so the
// next attempt's single REQ reaches back far enough to catch it up; once
// every relay has a cursor the REQ starts at the oldest one less the overlap.
func TestBootstrapperLiveGroupsKeepPerRelayCursorsInTheLocalStore(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	now := gonostr.Now()
	older := signedBootstrapEventBy(t, testNostrPrivateKey, KindCASAudit, "older", now-3000)
	newer := signedBootstrapEventBy(t, testNostrPrivateKey, KindCASAudit, "newer", now-2000)
	up := &bootstrapFakeRelay{url: "wss://up.example", store: []gonostr.Event{older, newer}}
	refusing := &bootstrapFakeRelay{url: "wss://refusing.example", store: []gonostr.Event{older}, closeWith: "error: shutting down"}
	pool := newBootstrapFakeRelayPool(t, up, refusing)
	store := openTestLocalStore(t, "")
	catalog := &KindCatalog{Version: "cursor-test", Groups: []ReplayGroup{
		{Name: "audit_live", Kinds: []int{KindCASAudit}, Tier: 0, Required: true, Authors: ReplayAuthorsAny},
	}}
	bootstrapper := NewBootstrapper(pool, catalog, store, &bootstrapApplyRecorder{}, zap.NewNop(), BootstrapConfig{
		RequestedTier:   0,
		SnapshotTimeout: time.Minute,
		CatchupTimeout:  time.Minute,
		Resume:          InboundSyncConfig{ResumeOverlap: time.Minute, RegularLookback: time.Hour},
	})
	hash := inboundFilterHash(gonostr.Filter{Kinds: []gonostr.Kind{KindCASAudit}})
	cursor := func(relay *bootstrapFakeRelay) gonostr.Timestamp {
		value, err := store.Cursor(gonostr.NormalizeURL(relay.url), hash)
		require.NoError(t, err)
		return value
	}
	lastSince := func(relay *bootstrapFakeRelay) gonostr.Timestamp {
		filters := relay.recordedFilters()
		require.NotEmpty(t, filters)
		return filters[len(filters)-1].Since
	}

	require.NoError(t, bootstrapper.attemptBootstrap(ctx))
	require.InDelta(t, float64(now-3600), float64(lastSince(up)), 5, "a fresh node starts at the lookback window, not now (C-3)")
	require.Equal(t, newer.CreatedAt, cursor(up))
	require.Zero(t, cursor(refusing), "a relay that CLOSED the REQ gets no cursor")

	refusing.closeWith = ""
	require.NoError(t, bootstrapper.attemptBootstrap(ctx))
	require.InDelta(t, float64(now-3600), float64(lastSince(up)), 5, "the REQ reaches back for the relay without a cursor")
	require.Equal(t, newer.CreatedAt, cursor(refusing))

	require.NoError(t, bootstrapper.attemptBootstrap(ctx))
	require.Equal(t, newer.CreatedAt-60, lastSince(up), "with every relay caught up the REQ resumes at the oldest cursor less the overlap")
	require.Equal(t, newer.CreatedAt-60, lastSince(refusing))
}
