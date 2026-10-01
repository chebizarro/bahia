package nostr

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// B-3: the projector's "already published" memory hydrates from the daemon's
// own events in the local event store, not from PostgreSQL. There is no
// nostr_events repository anywhere in these tests.

type localHistoryDaemon struct {
	projector *Projector
	publisher *Publisher
	store     *localstore.Store
	outbox    *localstore.Outbox
}

// startLocalHistoryDaemon builds a projector and control-plane publisher the
// way app.go does, over the outbox and event store files in dir.
func startLocalHistoryDaemon(t *testing.T, dir string, script *relayScript) *localHistoryDaemon {
	t.Helper()
	store, err := localstore.Open(filepath.Join(dir, "daemon.bolt"))
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = outbox.Close()
		_ = store.Close()
	})
	publisher := NewPublisher(projectorTestConfig(), NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		WithPublishTarget(repository.NostrPublishTargetControlPlane), WithLocalOutbox(outbox, store))
	publisher.publishFn = script.publish
	publisher.relayURLs = func() []string { return []string{cpRelayA, cpRelayB} }
	servicePubkey, err := publicKeyHexFromPrivateKeyHex(projectorTestConfig().PrivateKey)
	require.NoError(t, err)
	history := NewLocalEventRepository(store, nil).Authored(servicePubkey)
	projector := NewProjector(projectorTestConfig(), newFakeProjectionSource(), publisher, history, zap.NewNop())
	publisher.OnDeliveryAbandoned(projector.ForgetAbandonedProjection)
	return &localHistoryDaemon{projector: projector, publisher: publisher, store: store, outbox: outbox}
}

func (d *localHistoryDaemon) close() {
	_ = d.outbox.Close()
	_ = d.store.Close()
}

func TestProjectorHydratesDedupeFromTheLocalStoreAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	serviceID, envID := uuid.New(), uuid.New()
	state := dedupeTestState(serviceID, envID, time.Now().UTC())

	first := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, first.projector.publishState(ctx, &state))
	require.Equal(t, 2, script.totalCalls(), "published to both control-plane relays")
	published := script.sent(cpRelayA)[0]
	// Another author's newer event on the same coordinate (inbound from a
	// relay) must not stand in for the daemon's own output.
	var held gonostr.Event
	for ev := range first.store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{KindCASControlState}}) {
		held = ev
	}
	foreign := gonostr.Event{Kind: held.Kind, CreatedAt: held.CreatedAt + 10, Tags: held.Tags, Content: `{"other":"author"}`}
	require.NoError(t, foreign.Sign(gonostr.Generate()))
	_, err := first.store.SaveEvent(foreign)
	require.NoError(t, err)
	first.close()

	restarted := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, restarted.projector.publishState(ctx, &state))
	require.Equal(t, 2, script.totalCalls(), "unchanged state is not re-signed after a restart")
	require.Equal(t, int64(1), restarted.projector.ProjectionMetrics()["service/state"].Deduped)
	require.Equal(t, published, script.sent(cpRelayA)[0])
}

// An abandoned projection is dropped from the local store, so a restarted
// projector re-signs that content instead of treating it as published.
func TestProjectorRepublishesContentWhoseDeliveryWasAbandoned(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	script.reject[cpRelayA] = "blocked: maintenance"
	script.reject[cpRelayB] = "blocked: maintenance"
	serviceID, envID := uuid.New(), uuid.New()
	state := dedupeTestState(serviceID, envID, time.Now().UTC())

	first := startLocalHistoryDaemon(t, dir, script)
	require.ErrorIs(t, first.projector.publishState(ctx, &state), ErrPublishAbandoned)
	var held int
	for range first.store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{KindCASControlState}}) {
		held++
	}
	require.Zero(t, held, "the abandoned event is not kept as the daemon's output")
	first.close()

	script.mu.Lock()
	script.reject = map[string]string{}
	script.mu.Unlock()
	calls := script.totalCalls()
	restarted := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, restarted.projector.publishState(ctx, &state))
	require.Equal(t, calls+2, script.totalCalls(), "the abandoned content is signed and published again")
}
