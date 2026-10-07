package gitea

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

const journalDaemonKeyHex = "3333333333333333333333333333333333333333333333333333333333333333"

// journalDaemon is the initiation journal wired the way app.go wires it: one
// daemon's local event store and publish outbox in dir, a real projector and
// a real fleet-OCK encryptor (key envelopes retained in the same store) and
// no relay, so every publish is queued in the outbox and retained locally.
type journalDaemon struct {
	store     *localstore.Store
	outbox    *localstore.Outbox
	publisher *nostrAdapter.Publisher
	journal   *nostrAdapter.HiveCICanonicalPublisher
	pubkey    string
}

func startJournalDaemon(t *testing.T, dir string) *journalDaemon {
	t.Helper()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	cfg := config.NostrConfig{PrivateKey: journalDaemonKeyHex, PublishEnabled: true}
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(journalDaemonKeyHex)
	require.NoError(t, err)
	publisher := nostrAdapter.NewPublisher(cfg, nostrAdapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		nostrAdapter.WithPublishTarget(repository.NostrPublishTargetControlPlane), nostrAdapter.WithLocalOutbox(outbox, store))
	history := nostrAdapter.NewLocalEventRepository(store, nil).Authored(pubkey)
	registry := service.NewRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	projector := nostrAdapter.NewProjector(cfg, registry, publisher, history, zap.NewNop())
	publisher.OnDeliveryAbandoned(projector.ForgetAbandonedProjection)
	signer, err := controlplane.NewPrivateKeySigner(journalDaemonKeyHex)
	require.NoError(t, err)
	ockManager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{
		Signer: signer, ServicePubkey: pubkey, Publisher: projector,
		History: nostrAdapter.NewProjectorOCKEnvelopeHistory(history), Logger: zap.NewNop(),
	})
	encryptor := controlplane.NewConfidentialEncryptor(ockManager, zap.NewNop())
	d := &journalDaemon{store: store, outbox: outbox, publisher: publisher, pubkey: pubkey,
		journal: nostrAdapter.NewHiveCICanonicalPublisher(projector, encryptor, zap.NewNop())}
	t.Cleanup(d.close)
	return d
}

func (d *journalDaemon) close() {
	d.publisher.Close()
	_ = d.outbox.Close()
	_ = d.store.Close()
}

// memoryInitiationIndex stands in for the PostgreSQL index; it can be told to
// fail or be wiped, as a lost database would be.
type memoryInitiationIndex struct {
	mu   sync.Mutex
	rows map[string]InitiationRecord
	fail bool
}

func newMemoryInitiationIndex() *memoryInitiationIndex {
	return &memoryInitiationIndex{rows: map[string]InitiationRecord{}}
}

func (m *memoryInitiationIndex) Upsert(_ context.Context, rec *InitiationRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errors.New("database unavailable")
	}
	m.rows[rec.SourceEventID] = *rec
	return nil
}

func (m *memoryInitiationIndex) ListInFlight(context.Context) ([]*InitiationRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return nil, errors.New("database unavailable")
	}
	var out []*InitiationRecord
	for _, rec := range m.rows {
		if rec.Stage != StageEvidencePublished {
			copied := rec
			out = append(out, &copied)
		}
	}
	return out, nil
}

func (m *memoryInitiationIndex) wipe() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = map[string]InitiationRecord{}
}

// Every durable boundary of an initiation resumes from the journal in the
// local event store after a restart: a fresh daemon process over the same
// store files, no SQL.
func TestCanonicalInitiationResumeEveryDurableBoundary(t *testing.T) {
	proveInitiationResume(t, func(t *testing.T) (InitiationStore, func() InitiationStore) {
		dir := t.TempDir()
		daemon := startJournalDaemon(t, dir)
		return NewCanonicalInitiationStore(daemon.journal, nil, nil), func() InitiationStore {
			daemon.close()
			daemon = startJournalDaemon(t, dir)
			return NewCanonicalInitiationStore(daemon.journal, nil, nil)
		}
	})
}

// Thirty-two concurrent initiations of the same signed request, each with its
// own initiator over the same journal, produce one build id, one 5401, one
// Loom job and one evidence record.
func TestCanonicalConcurrentInitiationSingleDispatch(t *testing.T) {
	daemon := startJournalDaemon(t, t.TempDir())
	store := NewCanonicalInitiationStore(daemon.journal, nil, nil)
	proveConcurrentInitiation(t, func() InitiationStore { return store })
}

// The build id is the source event's: a request that names another build id
// for the same signed event is refused, and a request without one gets it.
func TestCanonicalInitiationBuildIdentityIsDerivedFromTheSourceEvent(t *testing.T) {
	ctx := context.Background()
	daemon := startJournalDaemon(t, t.TempDir())
	store := NewCanonicalInitiationStore(daemon.journal, nil, nil)
	req := arcanaStartRequest(uuid.New())
	derived := controlplane.BuildIDForSourceEvent(req.SourceEventID)
	require.Equal(t, derived, req.BuildID)

	foreign := req
	foreign.BuildID = uuid.New()
	_, _, err := store.Claim(ctx, foreign)
	require.ErrorContains(t, err, "does not belong to source event")

	unnamed := req
	unnamed.BuildID = uuid.Nil
	rec, claimed, err := store.Claim(ctx, unnamed)
	require.NoError(t, err)
	require.True(t, claimed)
	require.Equal(t, derived, rec.Request.BuildID)
	require.Equal(t, derived, rec.Result.BuildID)

	again, claimed, err := store.Claim(ctx, req)
	require.NoError(t, err)
	require.False(t, claimed, "the same request adopts the retained record")
	require.Equal(t, rec.Fingerprint, again.Fingerprint)

	changed := req
	changed.GitRef = "release"
	_, _, err = store.Claim(ctx, changed)
	require.ErrorContains(t, err, "conflicts with its canonical build initiation")
}

// The prepared operation (signed 5401, pinned Loom job) and the per-run
// publisher key survive a restart through the journal, and the key is never
// in the fleet-readable document nor in plaintext in the stored event.
func TestCanonicalInitiationKeepsThePreparedOperationWithoutPlaintextSecrets(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	daemon := startJournalDaemon(t, dir)
	store := NewCanonicalInitiationStore(daemon.journal, nil, nil)
	server := httptest.NewServer((&fakeGitea{}).handler(t))
	defer server.Close()
	original, _, _, credential, _ := newConformanceInitiator(t, server)
	req := arcanaStartRequest(credential)
	relay := newInitiationRelay(t)

	// Crash after the request is prepared and journaled, before anything is
	// published: the prepared 5401 and the per-run key exist only in the journal.
	_, err := restartInitiator(t, original, &crashInitiationStore{store, StageRequestReady, false}, relay).StartHiveCIBuild(ctx, req)
	require.ErrorIs(t, err, errSimulatedCrash)
	prepared, err := store.Get(ctx, req.SourceEventID)
	require.NoError(t, err)
	require.Equal(t, StageRequestReady, prepared.Stage)
	require.NotNil(t, prepared.RunEvent)
	require.NotNil(t, prepared.LoomJob)
	require.True(t, strings.HasPrefix(prepared.PublisherNsec, "nsec1"), "per-run key is recovered from the service-only layer")
	require.Equal(t, 0, relay.sends[nostr.Kind(kinds.HiveCIWorkflowRun)])

	var journalEvents []nostr.Event
	for ev := range daemon.store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}}) {
		if tagged(ev, "t") == kinds.CPStateTopicHiveCIInitiation {
			journalEvents = append(journalEvents, ev)
		}
	}
	require.Len(t, journalEvents, 1, "one replaceable journal record per source event")
	require.NotContains(t, journalEvents[0].Content, prepared.PublisherNsec)
	require.NotContains(t, journalEvents[0].Content, "nsec1")
	require.NotContains(t, journalEvents[0].Content, prepared.RunEvent.ID.Hex(), "the document is not plaintext on the relay")
	entry, err := daemon.journal.ReadInitiation(ctx, req.SourceEventID)
	require.NoError(t, err)
	require.NotContains(t, string(entry.Document), prepared.PublisherNsec, "the fleet-readable document carries no key material")
	var secrets map[string]string
	require.NoError(t, json.Unmarshal(entry.ServiceOnly, &secrets))
	require.Equal(t, prepared.PublisherNsec, secrets["publisher_nsec"])

	// A new daemon process over the same store resumes the prepared
	// operation: the journaled 5401 is published, not a second one.
	daemon.close()
	daemon = startJournalDaemon(t, dir)
	resumed := NewCanonicalInitiationStore(daemon.journal, nil, nil)
	result, err := restartInitiator(t, original, resumed, relay).StartHiveCIBuild(ctx, req)
	require.NoError(t, err)
	require.Equal(t, req.BuildID, result.BuildID)
	require.Equal(t, prepared.RunEvent.ID.Hex(), result.CIRunID)
	relay.assertOneBuild(t)
	final, err := resumed.Get(ctx, req.SourceEventID)
	require.NoError(t, err)
	require.Equal(t, StageEvidencePublished, final.Stage)
	require.Empty(t, final.PublisherNsec, "the per-run key is dropped once the job is dispatched")
}

// The SQL index is derived: it may fail or be lost mid-initiation and the
// initiation still completes from the journal; the index is rebuilt from it.
func TestCanonicalInitiationSurvivesIndexFailureAndLoss(t *testing.T) {
	ctx := context.Background()
	daemon := startJournalDaemon(t, t.TempDir())
	index := newMemoryInitiationIndex()
	store := NewCanonicalInitiationStore(daemon.journal, index, nil)
	server := httptest.NewServer((&fakeGitea{}).handler(t))
	defer server.Close()
	original, _, _, credential, _ := newConformanceInitiator(t, server)
	req := arcanaStartRequest(credential)
	relay := newInitiationRelay(t)

	index.fail = true
	_, err := restartInitiator(t, original, &crashInitiationStore{store, StageRequestPublished, false}, relay).StartHiveCIBuild(ctx, req)
	require.ErrorIs(t, err, errSimulatedCrash)
	index.fail = false
	index.wipe()

	result, err := restartInitiator(t, original, store, relay).StartHiveCIBuild(ctx, req)
	require.NoError(t, err)
	require.Equal(t, req.BuildID, result.BuildID)
	relay.assertOneBuild(t)
	require.NoError(t, store.RebuildIndex(ctx))
	index.mu.Lock()
	indexed, ok := index.rows[req.SourceEventID]
	index.mu.Unlock()
	require.True(t, ok, "the index is rebuilt from the journal")
	require.Equal(t, StageEvidencePublished, indexed.Stage)
}

// An initiation that only the SQL index knows (claimed before the journal was
// canonical) is journaled once, keeping its stage and prepared operation, and
// then resumes from the journal.
func TestCanonicalInitiationBackfillsInFlightSQLInitiations(t *testing.T) {
	ctx := context.Background()
	daemon := startJournalDaemon(t, t.TempDir())
	index := newMemoryInitiationIndex()
	server := httptest.NewServer((&fakeGitea{}).handler(t))
	defer server.Close()
	original, _, _, credential, _ := newConformanceInitiator(t, server)
	req := arcanaStartRequest(credential)
	relay := newInitiationRelay(t)

	// The SQL-era store claims and prepares, then the process dies.
	legacy := NewMemoryInitiationStore()
	_, err := restartInitiator(t, original, &crashInitiationStore{legacy, StageRequestReady, false}, relay).StartHiveCIBuild(ctx, req)
	require.ErrorIs(t, err, errSimulatedCrash)
	rec, err := legacy.Get(ctx, req.SourceEventID)
	require.NoError(t, err)
	require.NoError(t, index.Upsert(ctx, rec))

	store := NewCanonicalInitiationStore(daemon.journal, index, nil)
	require.NoError(t, store.BackfillFromIndex(ctx, daemon.outbox))
	journaled, err := store.Get(ctx, req.SourceEventID)
	require.NoError(t, err)
	require.Equal(t, StageRequestReady, journaled.Stage)
	require.Equal(t, rec.PublisherNsec, journaled.PublisherNsec)
	require.Equal(t, rec.RunEvent.ID, journaled.RunEvent.ID)
	require.NoError(t, store.BackfillFromIndex(ctx, daemon.outbox), "the backfill runs once")

	result, err := restartInitiator(t, original, store, relay).StartHiveCIBuild(ctx, req)
	require.NoError(t, err)
	require.Equal(t, rec.RunEvent.ID.Hex(), result.CIRunID)
	relay.assertOneBuild(t)
}

func tagged(ev nostr.Event, key string) string {
	for _, tag := range ev.Tags {
		if len(tag) >= 2 && tag[0] == key {
			return tag[1]
		}
	}
	return ""
}
