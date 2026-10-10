package controlplane

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestPolicyCensusEffectiveRelaysFollowSidecarAndCanonicalPrecedence(t *testing.T) {
	cfg := config.Defaults().Nostr
	cfg.Sidecar.Enabled = true
	cfg.Sidecar.BackendURL = "wss://sidecar-internal.example"
	cfg.Sidecar.PublicURL = "wss://sidecar-public.example"
	cfg.ContextVMRelays = []string{"wss://configured.example"}
	state := RelayPolicyState{Schema: RelaySettingsSchema, ContextVMRelays: []string{"wss://canonical-context.example"}, ServiceRelays: []string{"wss://canonical-service.example"}}
	bootstrap, err := PolicyCensusBootstrapRelays(cfg)
	require.NoError(t, err)
	require.Equal(t, []string{"wss://sidecar-internal.example"}, bootstrap)
	effective, err := PolicyCensusEffectiveRelays(cfg, state)
	require.NoError(t, err)
	require.Equal(t, bootstrap, effective, "sidecar backend has control-plane precedence")
	cfg.Sidecar.Enabled = false
	bootstrap, err = PolicyCensusBootstrapRelays(cfg)
	require.NoError(t, err)
	require.Equal(t, []string{"wss://configured.example"}, bootstrap)
	effective, err = PolicyCensusEffectiveRelays(cfg, state)
	require.NoError(t, err)
	require.Equal(t, []string{"wss://canonical-context.example"}, effective)
	_, err = VerifyPolicyCensusRelays(cfg, CanonicalRelayPolicyHead{EventID: "signed-head", State: state}, []string{"wss://configured.example"})
	require.ErrorContains(t, err, "do not match")
	bound, err := VerifyPolicyCensusRelays(cfg, CanonicalRelayPolicyHead{EventID: "signed-head", State: state}, []string{"wss://canonical-context.example"})
	require.NoError(t, err)
	require.Equal(t, effective, bound)
	state.ContextVMRelays = nil
	effective, err = PolicyCensusEffectiveRelays(cfg, state)
	require.NoError(t, err)
	require.Equal(t, []string{"wss://canonical-service.example"}, effective)
	state.ServiceRelays = nil
	_, err = PolicyCensusEffectiveRelays(cfg, state)
	require.ErrorContains(t, err, "no control-plane topology")
}

func censusRelay(t *testing.T, events ...gonostr.Event) string {
	t.Helper()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	require.NoError(t, store.Init())
	t.Cleanup(store.Close)
	relay.UseEventstore(store, 500)
	for _, ev := range events {
		_, err := relay.AddEvent(t.Context(), ev)
		require.NoError(t, err)
	}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return gonostr.NormalizeURL("ws" + strings.TrimPrefix(server.URL, "http"))
}

func TestReadCanonicalRelayPolicyHeadRequiresSameSignedHeadEverywhere(t *testing.T) {
	state := RelayPolicyState{Schema: RelaySettingsSchema, ContextVMRelays: []string{"wss://canonical.example"}}
	ev := signedRelaySettingsStateEvent(t, time.Now().UTC().Add(-time.Minute), state)
	first := censusRelay(t, *ev)
	second := censusRelay(t, *ev)
	pool := nostradapter.NewRelayPool([]string{first, second}, zap.NewNop())
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	author := ev.PubKey
	head, err := ReadCanonicalRelayPolicyHead(ctx, pool, author)
	require.NoError(t, err)
	require.Equal(t, ev.ID.Hex(), head.EventID)
	require.Equal(t, state.ContextVMRelays, head.State.ContextVMRelays)
}

func TestReadCanonicalRelayPolicyHeadRefusesMissingOrDisagreeingRelay(t *testing.T) {
	state := RelayPolicyState{Schema: RelaySettingsSchema, ContextVMRelays: []string{"wss://canonical.example"}}
	firstEvent := signedRelaySettingsStateEvent(t, time.Now().UTC().Add(-2*time.Minute), state)
	state.ContextVMRelays = []string{"wss://changed.example"}
	secondEvent := signedRelaySettingsStateEvent(t, time.Now().UTC().Add(-time.Minute), state)
	first := censusRelay(t, *firstEvent)
	for name, other := range map[string]string{
		"missing": censusRelay(t),
		"changed": censusRelay(t, *secondEvent),
	} {
		t.Run(name, func(t *testing.T) {
			pool := nostradapter.NewRelayPool([]string{first, other}, zap.NewNop())
			defer pool.Close()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			_, err := ReadCanonicalRelayPolicyHead(ctx, pool, firstEvent.PubKey)
			require.Error(t, err)
		})
	}
}
