package controlplane

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestEffectiveControlPlaneRelaysCanonicalListsGovern(t *testing.T) {
	static := config.NostrConfig{
		ServiceRelays:   []string{"wss://relay.sharegap.net", "wss://static-only.example"},
		ContextVMRelays: []string{"wss://static-contextvm.example"},
		BrowserRelays:   []string{"wss://static-browser.example"},
	}
	got := EffectiveControlPlaneRelays(static, RelayPolicyState{
		ServiceRelays: []string{"wss://relay.sharegap.net", "wss://bahia.sharegap.net/relay"},
		BrowserRelays: []string{"wss://bahia.sharegap.net/relay"},
	})
	require.Equal(t, []string{"wss://relay.sharegap.net", "wss://bahia.sharegap.net/relay"}, got.Service)
	require.Equal(t, []string{"wss://bahia.sharegap.net/relay"}, got.ContextVM, "ContextVM falls back to the canonical browser relays")
	require.Equal(t, []string{"wss://bahia.sharegap.net/relay"}, got.Browser)

	got = EffectiveControlPlaneRelays(static, RelayPolicyState{})
	require.Equal(t, static.ServiceRelays, got.Service, "an empty canonical list leaves the bootstrap list in force")
	require.Equal(t, static.ContextVMRelays, got.ContextVM)
	require.Equal(t, static.BrowserRelays, got.Browser)
}

// Without PostgreSQL the canonical relay settings still govern: the hydrator
// promotes the latest event after EOSE into the local projection record, and
// a restarted daemon restores it before any relay answers.
func TestRelaySettingsHydratorGovernsWithoutPostgresAcrossRestart(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	records, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = records.Close() })

	older := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, ServiceRelays: []string{"wss://relay.sharegap.net", "wss://relay.damus.example"},
	})
	latest := signedRelaySettingsStateEvent(t, time.Unix(1_500, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, ServiceRelays: []string{"wss://relay.sharegap.net", "wss://bahia.sharegap.net/relay"},
	})

	var applied []RelayPolicyState
	caughtUp := 0
	newHydrator := func() *RelaySettingsHydrator {
		return NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
			Pool:              nostradapter.NewRelayPool([]string{"wss://relay.example"}, zap.NewNop()),
			ServicePubkey:     servicePubkey,
			ProjectionStore:   NewLocalRelayPolicyProjectionStore(records),
			Logger:            zap.NewNop(),
			Now:               func() time.Time { return time.Unix(2_000, 0).UTC() },
			OnSnapshotApplied: func(_ context.Context, state RelayPolicyState) error { applied = append(applied, state); return nil },
			OnCaughtUp:        func(context.Context) { caughtUp++ },
		})
	}

	h := newHydrator()
	require.NoError(t, h.LoadProjection(t.Context()))
	_, ok := h.Snapshot()
	require.False(t, ok, "an empty local store has no canonical relay settings")

	drain := make(chan time.Time, 1)
	drainStarted := make(chan struct{})
	h.newDrainTimer = func(time.Duration) (<-chan time.Time, func()) {
		close(drainStarted)
		return drain, func() {}
	}
	eventCh := make(chan *nostr.Event)
	allEOSE := make(chan struct{})
	relayEOSE := make(chan nostradapter.RelayEOSE, 1)
	closed := make(chan nostradapter.RelayClosed)
	setRelaySettingsSubscription(t, func(context.Context) *nostradapter.MergedSubscription {
		return &nostradapter.MergedSubscription{Events: eventCh, EndOfStoredEvents: allEOSE, RelayEOSE: relayEOSE, Closed: closed}
	})
	errCh := make(chan error, 1)
	go func() { errCh <- h.subscribe(context.Background()) }()
	eventCh <- latest
	eventCh <- older // arrives later, is older: latest-wins keeps the newer head
	relayEOSE <- nostradapter.RelayEOSE{RelayURL: "wss://relay.example", SubscriptionID: "policy"}
	close(relayEOSE)
	close(allEOSE)
	<-drainStarted
	require.Zero(t, caughtUp, "catch-up waits for the post-EOSE drain")
	drain <- time.Unix(2_001, 0)
	close(eventCh)
	close(closed)
	require.NoError(t, <-errCh)

	require.Equal(t, 1, caughtUp)
	snapshot, ok := h.Snapshot()
	require.True(t, ok)
	require.Equal(t, latest.Content, mustRelayPolicyContent(t, snapshot))
	require.Len(t, applied, 1, "the older event must not be applied")

	// Restart: a new hydrator over the same local record store restores the
	// latest head before any relay answers and applies it.
	applied = nil
	restarted := newHydrator()
	require.NoError(t, restarted.LoadProjection(t.Context()))
	snapshot, ok = restarted.Snapshot()
	require.True(t, ok)
	require.True(t, slices.Equal(snapshot.ServiceRelays, []string{"wss://relay.sharegap.net", "wss://bahia.sharegap.net/relay"}), "restored %v", snapshot.ServiceRelays)
	require.Len(t, applied, 1)
	projection, ok := restarted.Projection()
	require.True(t, ok)
	require.Equal(t, latest.ID.Hex(), projection.EventID)
	require.Nil(t, projection.RelayConfirmedAt, "a restored head is not relay-confirmed until a relay serves it again")
}

func mustRelayPolicyContent(t *testing.T, state RelayPolicyState) string {
	t.Helper()
	payload, _, err := canonicalRelayPolicyPayload(state)
	require.NoError(t, err)
	return string(payload)
}

// A canonical relay-settings event the daemon refuses never becomes the
// head: it is not promoted, not stored, and a restart restores the last
// settings the daemon accepted. Older candidates are never validated.
func TestRelaySettingsHydratorRefusedCandidateNeverBecomesHead(t *testing.T) {
	servicePubkey := testNostrPubKeyHexFromPrivateKey(t, testServiceKey)
	records, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = records.Close() })
	static := config.NostrConfig{
		PublishEnabled:   true,
		PrivateKey:       testServiceKey,
		ServiceRelays:    []string{"wss://relay.sharegap.net"},
		ControlledRelays: []string{"wss://relay.sharegap.net", "wss://bahia.sharegap.net/relay"},
	}
	var validated []string
	validate := func(state RelayPolicyState) error {
		validated = append(validated, strings.Join(state.ServiceRelays, ","))
		return ValidateRelayPolicy(static, state)
	}
	accepted := signedRelaySettingsStateEvent(t, time.Unix(1_000, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, ServiceRelays: []string{"wss://relay.sharegap.net", "wss://bahia.sharegap.net/relay"},
	})
	refused := signedRelaySettingsStateEvent(t, time.Unix(1_500, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, ServiceRelays: []string{"wss://relay.sharegap.net", "wss://relay.damus.io"},
	})
	older := signedRelaySettingsStateEvent(t, time.Unix(500, 0), RelayPolicyState{
		Schema: RelaySettingsSchema, ServiceRelays: []string{"wss://uncontrolled.example"},
	})
	newHydrator := func() *RelaySettingsHydrator {
		return NewRelaySettingsHydrator(RelaySettingsHydratorConfig{
			Pool:            nostradapter.NewRelayPool([]string{"wss://relay.example"}, zap.NewNop()),
			ServicePubkey:   servicePubkey,
			ProjectionStore: NewLocalRelayPolicyProjectionStore(records),
			Validate:        validate,
			Logger:          zap.NewNop(),
			Now:             func() time.Time { return time.Unix(2_000, 0).UTC() },
		})
	}

	h := newHydrator()
	require.NoError(t, h.LoadProjection(t.Context()))
	require.True(t, h.handleEvent(t.Context(), accepted))
	require.False(t, h.handleEvent(t.Context(), refused), "a refused candidate must not be promoted")
	require.False(t, h.handleEvent(t.Context(), older))
	require.Equal(t, []string{"wss://relay.sharegap.net,wss://bahia.sharegap.net/relay", "wss://relay.sharegap.net,wss://relay.damus.io"}, validated,
		"only candidates that would become the head are validated")
	projection, ok := h.Projection()
	require.True(t, ok)
	require.Equal(t, accepted.ID.Hex(), projection.EventID)

	// The refused event stays the newest on the relays; the restart still
	// restores the last accepted head and does not apply the refused one.
	restarted := newHydrator()
	require.NoError(t, restarted.LoadProjection(t.Context()))
	projection, ok = restarted.Projection()
	require.True(t, ok)
	require.Equal(t, accepted.ID.Hex(), projection.EventID, "a restart restores the last accepted settings")
	require.False(t, restarted.handleEvent(t.Context(), refused), "the relays still serve the refused event; it stays refused")
	projection, _ = restarted.Projection()
	require.Equal(t, accepted.ID.Hex(), projection.EventID)
}

func TestValidateRelayPolicyChecksCanonicalNIP34AndDMRelayLists(t *testing.T) {
	static := config.NostrConfig{ServiceRelays: []string{"wss://relay.sharegap.net"}, ControlledRelays: []string{"wss://relay.sharegap.net"}}
	err := ValidateRelayPolicy(static, RelayPolicyState{NIP34Relays: []string{"wss://relay.damus.io"}})
	require.ErrorContains(t, err, "canonical relay settings nip34_relays lists public relay wss://relay.damus.io")
	err = ValidateRelayPolicy(static, RelayPolicyState{DMRelayLists: []RelayPolicyDMRelayList{{Enabled: true, Relays: []string{"wss://nos.lol"}}}})
	require.ErrorContains(t, err, "dm_relay_lists[0].relays lists public relay wss://nos.lol")
	static.AllowedPublicRelays = []string{"wss://relay.damus.io"}
	require.NoError(t, ValidateRelayPolicy(static, RelayPolicyState{NIP34Relays: []string{"wss://relay.damus.io"}}))
}
