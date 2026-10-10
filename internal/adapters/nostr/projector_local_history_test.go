package nostr

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// the projector's "already published" memory hydrates from the daemon's
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
	publisher := newKeyedTestPublisher(projectorTestConfig(), NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		WithPublishTarget(repository.NostrPublishTargetControlPlane), WithLocalOutbox(outbox, store))
	publisher.publishFn = script.publish
	publisher.relayURLs = func() []string { return []string{cpRelayA, cpRelayB} }
	servicePubkey, err := publicKeyHexFromPrivateKeyHex(projectorTestConfig().PrivateKey)
	require.NoError(t, err)
	history := NewLocalEventRepository(store, nil).Authored(servicePubkey)
	projector := newKeyedTestProjector(projectorTestConfig(), newFakeProjectionSource(), publisher, history, zap.NewNop())
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
	require.NoError(t, first.projector.publishStateForTest(ctx, &state))
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
	require.NoError(t, restarted.projector.publishStateForTest(ctx, &state))
	require.Equal(t, 2, script.totalCalls(), "unchanged state is not re-signed after a restart")
	require.Equal(t, int64(1), restarted.projector.ProjectionMetrics()["service/state"].Deduped)
	require.Equal(t, published, script.sent(cpRelayA)[0])
}

func TestRuntimeStateOutboxDedupeUsesCanonicalObservationAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	serviceID, envID := uuid.New(), uuid.New()
	obsID := uuid.New()
	state := &domain.EnvironmentServiceState{ServiceID: serviceID, EnvironmentID: envID, DesiredHash: "canonical-hash", DriftStatus: domain.DriftStatusInSync, CurrentObservationID: &obsID}
	observation := &domain.RuntimeObservation{ID: obsID, ServiceID: serviceID, EnvironmentID: envID,
		ObservedHost: "10.0.0.10", ObservedContainerID: "container-a", NormalizedHash: "canonical-hash",
		HealthStatus: domain.HealthStatusHealthy, Source: "runtime", ObservedAt: time.Unix(100, 0).UTC()}
	first := startLocalHistoryDaemon(t, dir, script)
	publisher := NewRelayFirstStatePublisher(first.projector, first.publisher)
	require.NoError(t, publisher.PublishState(ctx, state, observation))
	require.Equal(t, 2, script.totalCalls())
	require.NoError(t, publisher.PublishState(ctx, state, observation))
	require.Equal(t, 2, script.totalCalls(), "repeat state must not enqueue another signed event")
	first.close()
	restarted := startLocalHistoryDaemon(t, dir, script)
	publisher = NewRelayFirstStatePublisher(restarted.projector, restarted.publisher)
	require.NoError(t, publisher.PublishState(ctx, state, observation))
	require.Equal(t, 2, script.totalCalls(), "restart must dedupe against retained local canonical state")
}

// A projection abandoned after it was queued stays in the local store flagged
// undelivered: the history reports it as failed, so a restarted
// projector re-signs that content instead of treating it as published, and
// the replacement is strictly newer than the flagged event and clears its
// marker. (An abandonment in the caller's own round is returned to the caller
// and the event dropped; see TestLocalOutboxWithoutPostgresPermanentRejectionAndAbandonment.)
func TestProjectorRepublishesContentWhoseDeliveryWasAbandoned(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	script := newRelayScript()
	script.setDown(cpRelayA, true)
	script.setDown(cpRelayB, true)
	serviceID, envID := uuid.New(), uuid.New()
	state := dedupeTestState(serviceID, envID, time.Now().UTC())

	first := startLocalHistoryDaemon(t, dir, script)
	abandonedCh := make(chan gonostr.Event, 1)
	first.publisher.OnDeliveryAbandoned(func(ev gonostr.Event) { abandonedCh <- ev })
	require.NoError(t, first.projector.publishStateForTest(ctx, &state), "queued while the relays are down")
	stop := runOutbox(t, first, 2)
	abandoned := receive(t, abandonedCh, "abandonment after the attempt budget")
	stop()
	var held []gonostr.Event
	for ev := range first.store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{KindCASControlState}}) {
		held = append(held, ev)
	}
	require.Len(t, held, 1, "the abandoned event stays the daemon's committed state")
	require.Equal(t, abandoned.ID, held[0].ID)
	marker, found, err := first.store.Undelivered(abandoned)
	require.NoError(t, err)
	require.True(t, found, "the abandoned coordinate is marked undelivered")
	require.Equal(t, abandoned.ID, marker.EventID)
	records, err := first.projector.history.ListByKind(ctx, KindCASControlState, 10)
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Equal(t, repository.NostrPublishStateFailed, records[0].PublishState, "readers see the record as delivery-failed")
	first.close()

	script.setDown(cpRelayA, false)
	script.setDown(cpRelayB, false)
	calls := script.totalCalls()
	restarted := startLocalHistoryDaemon(t, dir, script)
	require.NoError(t, restarted.projector.publishStateForTest(ctx, &state))
	require.Equal(t, calls+2, script.totalCalls(), "the abandoned content is signed and published again")
	held = nil
	for ev := range restarted.store.QueryEvents(gonostr.Filter{Kinds: []gonostr.Kind{KindCASControlState}}) {
		held = append(held, ev)
	}
	require.Len(t, held, 1)
	replacement := held[0]
	require.NotEqual(t, abandoned.ID, replacement.ID)
	require.Greater(t, replacement.CreatedAt, abandoned.CreatedAt, "the replacement is strictly newer than the flagged event")
	_, found, err = restarted.store.Undelivered(replacement)
	require.NoError(t, err)
	require.False(t, found, "a delivered publish of the coordinate clears the marker")
	markers, err := restarted.store.ListUndelivered()
	require.NoError(t, err)
	require.Empty(t, markers)
}
