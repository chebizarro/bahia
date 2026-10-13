package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func (m *mockEncryptedPublisher) PublishProjection(ctx context.Context, ev nostr.Event, _ string, _ *uuid.UUID) error {
	_, err := m.Publish(ctx, ev)
	return err
}

func TestRelaySettingsFactsRetryAgainstCurrentServiceWriteSet(t *testing.T) {
	first := censusRelay(t)
	refusing, relay := censusRelayHandle(t)
	relay.OnEvent = func(context.Context, nostr.Event) (bool, string) { return true, "error: try again" }
	replacement := censusRelay(t)
	pool := nostradapter.NewRelayPool([]string{first, refusing}, zap.NewNop())
	defer pool.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	pool.Connect(ctx)
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	events, err := localstore.Open(filepath.Join(t.TempDir(), "events.bolt"))
	require.NoError(t, err)
	defer events.Close()
	signer, err := NewPrivateKeySigner(testServiceKey)
	require.NoError(t, err)
	writeRelays := []string{first, refusing}
	publisher := nostradapter.NewPublisher(config.NostrConfig{PublishEnabled: true, PublishQuorum: 2}, pool, nil, zap.NewNop(),
		nostradapter.WithPublisherSigner(signer), nostradapter.WithLocalOutbox(outbox, events),
		nostradapter.WithPublisherWriteRelays(func() []string { return append([]string(nil), writeRelays...) }))
	static := config.NostrConfig{PublishEnabled: true, PublishQuorum: 2, ControlledRelays: []string{first, refusing, replacement}}
	h := NewRelaySettingsHandlers(RelaySettingsHandlerConfig{Config: &config.Config{Nostr: static}, ProjectionStore: &memoryRelayPolicyProjectionStore{}, ServicePubkey: testNostrPubKeyHexFromPrivateKey(t, testServiceKey)})
	h.SetPublisher(publisher, signer)
	req := ContextVMRequest{Event: &nostr.Event{PubKey: testNostrPubKeyFromPrivateKey(t, testRequesterKey)}}
	params, err := json.Marshal(RelayPolicyState{Schema: RelaySettingsSchema, ServiceRelays: []string{first, replacement}, DMRelayLists: []RelayPolicyDMRelayList{{Enabled: true, Feature: config.DMRelayListFeatureNotifications, Identity: config.DMRelayListIdentityService, Relays: []string{replacement}}}})
	require.NoError(t, err)
	req.RPC.Params = params
	_, err = h.applyPolicyDirect(ctx, req)
	require.ErrorIs(t, err, nostradapter.ErrPublishIncomplete)
	pending, err := outbox.ListEntries([]string{localstore.OutboxPending}, 10)
	require.NoError(t, err)
	require.Len(t, pending, 3)
	seen := map[nostr.Kind]bool{}
	for _, entry := range pending {
		seen[entry.Event.Kind] = true
		require.Equal(t, repository.NostrPublishTargetDefault, entry.Target)
		require.False(t, entry.Delivered, "one accepted OK must not meet quorum")
		require.True(t, entry.Relays[first].Accepted, "outbox entry: %#v", entry)
	}
	require.True(t, seen[kinds.CASControlState] && seen[kinds.CASAudit] && seen[kinds.NIP51DMRelayList])
	pool.ReconfigureRelayURLsContext(ctx, []string{first, replacement})
	writeRelays = []string{first, replacement}
	delivered := make(chan nostr.Event, 3)
	publisher.OnDelivered(func(ev nostr.Event) { delivered <- ev })
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- publisher.Run(runCtx) }()
	for range 3 {
		select {
		case <-delivered:
		case <-ctx.Done():
			t.Fatal("queued settings facts did not reach the new service relay quorum")
		}
	}
	stop()
	select {
	case err := <-done:
		require.True(t, err == nil || errors.Is(err, context.Canceled), "%v", err)
	case <-ctx.Done():
		t.Fatal("outbox runner did not stop")
	}
	for _, prior := range pending {
		entry, found, err := outbox.Get(prior.Event.ID)
		require.NoError(t, err)
		require.True(t, found)
		require.True(t, entry.Delivered)
		require.Equal(t, 2, entry.Policy.Required)
		require.ElementsMatch(t, []string{first, replacement}, entry.Policy.WriteRelays)
	}
}
