package soulfactory

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	casnostr "git.sharegap.net/cascadia/cascadia-go/nostr"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"

	nostrAdapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrutil"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
)

const (
	ledgerDaemonKeyHex  = "4444444444444444444444444444444444444444444444444444444444444444"
	ledgerBunkerSecret  = "bunker://secret-should-never-persist?secret=zzz"
	ledgerSuccessResult = "7d2b1c0e5f3a4b6c8d9e0f1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e"
)

// ledgerClock is a monotonic test clock shared by one host's stores.
type ledgerClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *ledgerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

// ledgerDaemon is one daemon host wired as app.go wires it: a local event
// store and publish outbox in dir, the real publisher over them (no relay,
// so every publish is queued in the outbox and retained locally), a real
// projector and a real fleet-OCK encryptor whose key envelopes are retained
// in the same store.
type ledgerDaemon struct {
	dir        string
	store      *localstore.Store
	outbox     *localstore.Outbox
	publisher  *nostrAdapter.Publisher
	ockManager *controlplane.OCKManager
	encryptor  *controlplane.ConfidentialEncryptor
	signer     casnostr.Signer
	pubkey     string
	clock      *ledgerClock
}

func startLedgerDaemon(t *testing.T, keyHex string) *ledgerDaemon {
	t.Helper()
	dir := t.TempDir()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	cfg := config.NostrConfig{PrivateKey: keyHex, PublishEnabled: true}
	pubkey, err := nostrutil.PublicKeyHexFromPrivateKeyHex(keyHex)
	require.NoError(t, err)
	publisher := nostrAdapter.NewPublisher(cfg, nostrAdapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		nostrAdapter.WithPublishTarget(repository.NostrPublishTargetControlPlane), nostrAdapter.WithLocalOutbox(outbox, store))
	history := nostrAdapter.NewLocalEventRepository(store, nil).Authored(pubkey)
	registry := service.NewRegistryService(nil, nil, nil, nil, nil, nil, nil, nil, nil, events.NewInProcessPublisher(zap.NewNop()), zap.NewNop())
	projector := nostrAdapter.NewProjector(cfg, registry, publisher, history, zap.NewNop())
	publisher.OnDeliveryAbandoned(projector.ForgetAbandonedProjection)
	signer, err := controlplane.NewPrivateKeySigner(keyHex)
	require.NoError(t, err)
	ockManager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{
		Signer: signer, ServicePubkey: pubkey, Publisher: projector,
		History: nostrAdapter.NewProjectorOCKEnvelopeHistory(history), Logger: zap.NewNop(),
	})
	d := &ledgerDaemon{
		dir: dir, store: store, outbox: outbox, publisher: publisher, ockManager: ockManager,
		encryptor: controlplane.NewConfidentialEncryptor(ockManager, zap.NewNop()), signer: signer, pubkey: pubkey,
		clock: &ledgerClock{now: time.Date(2026, 10, 6, 9, 0, 0, 0, time.UTC)},
	}
	t.Cleanup(func() {
		d.publisher.Close()
		_ = d.outbox.Close()
		_ = d.store.Close()
	})
	return d
}

// ledger opens the canonical ledger of the host over cacheDir.
func (d *ledgerDaemon) ledger(t *testing.T, cacheDir string) *productionStateStore {
	t.Helper()
	store, err := newProductionStateStore(cacheDir, productionLedgerSeams{
		Reader: d.store, Publisher: d.publisher, Encryptor: d.encryptor, ServicePubkey: d.pubkey, Now: d.clock.Now,
	})
	require.NoError(t, err)
	return store
}

// replicate copies every cp-state record the host authored into other, as
// relay catch-up fills a fresh host's local store: the ledger records and
// the key envelopes the fresh host recovers the fleet OCK from.
func (d *ledgerDaemon) replicate(t *testing.T, other *ledgerDaemon) {
	t.Helper()
	author, err := nostr.PubKeyFromHex(d.pubkey)
	require.NoError(t, err)
	for ev := range d.store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{author}}) {
		_, err := other.store.SaveEvent(ev)
		require.NoError(t, err)
	}
}

// ledgerRecords returns the host's retained ledger records at the
// coordinate, newest first.
func (d *ledgerDaemon) ledgerRecords(t *testing.T, dTag string) []nostr.Event {
	t.Helper()
	var out []nostr.Event
	for ev := range d.store.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Tags: nostr.TagMap{"d": {dTag}}}) {
		out = append(out, ev)
	}
	return out
}

func (d *ledgerDaemon) latestLedgerRecord(t *testing.T, dTag string) nostr.Event {
	t.Helper()
	records := d.ledgerRecords(t, dTag)
	require.NotEmpty(t, records, "no ledger record at %s", dTag)
	return records[0]
}

// fleetDocument decrypts the fleet-visible document of a ledger record with
// the fleet OCK alone, as any fleet operator can.
func (d *ledgerDaemon) fleetDocument(t *testing.T, ev nostr.Event) []byte {
	t.Helper()
	orgID, version, err := controlplane.VersionFromEnvelope(ev.Content)
	require.NoError(t, err)
	require.Equal(t, kinds.FleetOCKScope, orgID)
	key, err := d.ockManager.GetKeyByVersion(context.Background(), orgID, version)
	require.NoError(t, err)
	document, err := controlplane.DecryptConfidentialContent(key, ev.Content, controlplane.ConfidentialRecordContext{
		LegacyKind: int(kinds.CPStateFamilySoulFactoryAdapterLedger), DTag: tagValue(ev.Tags, "d"), Topic: kinds.CPStateTopicSoulFactoryAdapterLedger,
	})
	require.NoError(t, err)
	return document
}

func ledgerSampleState(t *testing.T, d *ledgerDaemon, requestID string) *productionProvisioningState {
	t.Helper()
	state := sampleProductionState(requestID)
	state.Soul.BunkerURI = ledgerBunkerSecret
	state.Soul.SoulMD = "# Scout\nA scout soul."
	state.Request = &domain.ProvisioningRequest{EventID: requestID, AgentID: "scout", Name: "Scout", Requester: d.pubkey}
	result := &nostr.Event{Kind: nostr.Kind(domain.KindProvisioningResult), CreatedAt: nostr.Timestamp(d.clock.Now().Unix()), Content: `{"status":"success","agent_id":"scout"}`, Tags: nostr.Tags{{"e", requestID}}}
	require.NoError(t, d.signer.SignEvent(context.Background(), result))
	state.SuccessResult = result
	return state
}

func ledgerSpec(requestID string) ProvisioningSpec {
	return ProvisioningSpec{RequestID: requestID, RunID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("run/"+requestID)).String(), AgentID: "scout", SpecHash: "spec-hash-scout", Runtime: domain.RuntimeTargetOpenClaw}
}

// A daemon on a fresh host, with no state directory at all, resumes the
// request state and the identity reservation from the canonical records
// alone, including the service-only success result, and its next save
// continues the version sequence.
func TestAdapterLedgerFreshHostResumesFromCanonicalRecords(t *testing.T) {
	ctx := context.Background()
	host := startLedgerDaemon(t, ledgerDaemonKeyHex)
	ledger := host.ledger(t, filepath.Join(host.dir, "adapters"))
	requestID := strings.Repeat("a1", 32)
	state := ledgerSampleState(t, host, requestID)
	require.NoError(t, ledger.save(ctx, state))
	require.Equal(t, uint64(1), state.Version)
	require.Empty(t, state.Soul.BunkerURI)
	state.Steps[StepRegisterServiceUnit] = productionStepState{Complete: true, Resources: []ObservedResource{ownedResource(ledgerSpec(requestID), "service", state.ServiceID.String(), true)}}
	require.NoError(t, ledger.save(ctx, state))
	require.Equal(t, uint64(2), state.Version)
	reservation, created, err := ledger.reservation(ctx, ledgerSpec(requestID), true)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, uint64(1), reservation.Version)

	fresh := startLedgerDaemon(t, ledgerDaemonKeyHex)
	host.replicate(t, fresh)
	resumed := fresh.ledger(t, filepath.Join(fresh.dir, "adapters"))
	got, err := resumed.load(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), got.Version)
	require.Equal(t, state.RunID, got.RunID)
	require.Equal(t, state.ServiceID, got.ServiceID)
	require.True(t, got.Steps[StepRegisterServiceUnit].Complete)
	require.Equal(t, state.Soul.SoulMD, got.Soul.SoulMD)
	require.Empty(t, got.Soul.BunkerURI)
	require.NotNil(t, got.SuccessResult)
	require.Equal(t, state.SuccessResult.ID, got.SuccessResult.ID)
	require.Equal(t, state.SuccessResult.Sig, got.SuccessResult.Sig)
	require.Equal(t, got.SuccessResult.ID.Hex(), got.SuccessResultID)
	again, created, err := resumed.reservation(ctx, ledgerSpec(requestID), true)
	require.NoError(t, err)
	require.False(t, created, "a fresh host must observe the reservation, not allocate another")
	require.Equal(t, reservation.RunID, again.RunID)
	require.Equal(t, reservation.CreatedAt, again.CreatedAt)

	got.SuccessDelivered = true
	require.NoError(t, resumed.save(ctx, got))
	require.Equal(t, uint64(3), got.Version)
	fresh.replicate(t, host)
	back, err := host.ledger(t, filepath.Join(host.dir, "adapters")).load(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, uint64(3), back.Version)
	require.True(t, back.SuccessDelivered)
	for _, ev := range host.ledgerRecords(t, ledgerRequestDTag(requestID)) {
		require.Equal(t, kinds.CPStateTopicSoulFactoryAdapterLedger, tagValue(ev.Tags, "t"))
		require.Equal(t, kinds.CPStateFamilySoulFactoryAdapterLedger.TagValue(), tagValue(ev.Tags, kinds.CASControlStateTagLegacyKind))
		require.Equal(t, ledgerRecordRequest, tagValue(ev.Tags, ledgerTagRecord))
	}
}

// The highest version wins across the record and the file cache: a stale
// cache file never overrides the record, a pre-canonical file (version 0)
// with no record is resumed from once and the next save publishes version
// 1, and a stale caller is refused.
func TestAdapterLedgerStaleFileVersusRecordPrecedence(t *testing.T) {
	ctx := context.Background()
	host := startLedgerDaemon(t, ledgerDaemonKeyHex)
	cacheDir := filepath.Join(host.dir, "adapters")
	ledger := host.ledger(t, cacheDir)
	requestID := strings.Repeat("b2", 32)
	state := ledgerSampleState(t, host, requestID)
	require.NoError(t, ledger.save(ctx, state))
	stale, err := ledgerClone(state)
	require.NoError(t, err)
	state.Prepared = false
	require.NoError(t, ledger.save(ctx, state))
	require.Equal(t, uint64(2), state.Version)

	// A stale file left behind at version 1 loses to the record at 2, for
	// this process and for a restarted one.
	require.NoError(t, writeProductionJSON(ledger.requestPath(requestID), stale))
	got, err := ledger.load(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), got.Version)
	require.False(t, got.Prepared)
	restarted := host.ledger(t, cacheDir)
	got, err = restarted.load(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, uint64(2), got.Version)
	require.False(t, got.Prepared)

	// The stale caller's save is a conflict, not a silent replacement.
	require.ErrorIs(t, restarted.save(ctx, stale), saga.ErrConflict)

	// In-place upgrade: a request known only from a pre-canonical file
	// resumes from it, and its next save publishes version 1.
	legacyID := strings.Repeat("c3", 32)
	legacy := sampleProductionState(legacyID)
	legacy.Schema = productionStateSchema
	require.NoError(t, writeProductionJSON(ledger.requestPath(legacyID), legacy))
	require.Empty(t, host.ledgerRecords(t, ledgerRequestDTag(legacyID)))
	upgraded, err := restarted.load(ctx, legacyID)
	require.NoError(t, err)
	require.Equal(t, uint64(0), upgraded.Version)
	require.NoError(t, restarted.save(ctx, upgraded))
	require.Equal(t, uint64(1), upgraded.Version)
	require.Len(t, host.ledgerRecords(t, ledgerRequestDTag(legacyID)), 1)

	// A compatibility reservation file is honoured the same way.
	spec := ledgerSpec(legacyID)
	require.NoError(t, writeProductionJSON(ledger.reservationPath(spec.AgentID), &productionIdentityReservation{
		Schema: productionReservationSchema, AgentID: spec.AgentID, SpecHash: spec.SpecHash, RequestID: legacyID, RunID: spec.RunID, CreatedAt: time.Now().UTC(),
	}))
	reservation, created, err := restarted.reservation(ctx, spec, true)
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, legacyID, reservation.RequestID)
}

// The record carries no secret value in its fleet-visible content: the
// bunker URI is absent, the signed success result is not in the document
// (only its id), and nothing of the state is in the public tags or in the
// ciphertext.
func TestAdapterLedgerRecordCarriesNoSecretInFleetContent(t *testing.T) {
	ctx := context.Background()
	host := startLedgerDaemon(t, ledgerDaemonKeyHex)
	ledger := host.ledger(t, filepath.Join(host.dir, "adapters"))
	requestID := strings.Repeat("d4", 32)
	state := ledgerSampleState(t, host, requestID)
	signature := hex.EncodeToString(state.SuccessResult.Sig[:])
	require.NoError(t, ledger.save(ctx, state))
	ev := host.latestLedgerRecord(t, ledgerRequestDTag(requestID))

	for _, marker := range []string{"secret-should-never-persist", "bunker://", signature, state.Soul.SoulMD, requestID, "scout", "spec-hash-scout", state.RunID} {
		require.NotContains(t, ev.Content, marker, "ciphertext leaks %q", marker)
	}
	for _, tag := range ev.Tags {
		require.Contains(t, []string{"d", "domain", "schema", "entity", "t", "legacy_kind", "deleted", ledgerTagRecord, ledgerTagVersion}, tag[0])
		require.NotContains(t, tag[1], requestID)
		require.NotEqual(t, "scout", tag[1])
	}
	document := host.fleetDocument(t, ev)
	for _, marker := range []string{"secret-should-never-persist", "bunker://", signature, `"success_result":`} {
		require.NotContains(t, string(document), marker, "fleet-visible document leaks %q", marker)
	}
	var visible map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(document, &visible))
	require.JSONEq(t, `"`+state.SuccessResult.ID.Hex()+`"`, string(visible["success_result_id"]))
	var soul map[string]any
	require.NoError(t, json.Unmarshal(visible["soul"], &soul))
	require.Equal(t, "", soul["bunker_uri"])

	// The reservation record is the same envelope at its own coordinate.
	_, _, err := ledger.reservation(ctx, ledgerSpec(requestID), true)
	require.NoError(t, err)
	identity := host.latestLedgerRecord(t, ledgerIdentityDTag("scout"))
	require.NotContains(t, identity.Content, "scout")
	require.NotContains(t, identity.Content, requestID)
	require.Equal(t, ledgerRecordIdentity, tagValue(identity.Tags, ledgerTagRecord))
	require.Contains(t, string(host.fleetDocument(t, identity)), `"agent_id":"scout"`)
}

// The service-only layer carries the signed success result and only the
// service key opens it: the fleet-visible layer decrypts without it, and
// another key's NIP-44 decrypt fails.
func TestAdapterLedgerServiceLayerRoundTripsForServiceKeyOnly(t *testing.T) {
	ctx := context.Background()
	host := startLedgerDaemon(t, ledgerDaemonKeyHex)
	ledger := host.ledger(t, filepath.Join(host.dir, "adapters"))
	requestID := strings.Repeat("e5", 32)
	state := ledgerSampleState(t, host, requestID)
	require.NoError(t, ledger.save(ctx, state))
	ev := host.latestLedgerRecord(t, ledgerRequestDTag(requestID))

	inner, err := host.encryptor.DecryptServiceInner(ctx, ev.Content)
	require.NoError(t, err)
	var serviceOnly ledgerServiceOnly
	require.NoError(t, json.Unmarshal(inner, &serviceOnly))
	require.NotNil(t, serviceOnly.SuccessResult)
	require.Equal(t, state.SuccessResult.ID, serviceOnly.SuccessResult.ID)
	require.Equal(t, state.SuccessResult.Sig, serviceOnly.SuccessResult.Sig)
	require.True(t, serviceOnly.SuccessResult.VerifySignature())

	other, err := controlplane.NewPrivateKeySigner("5555555555555555555555555555555555555555555555555555555555555555")
	require.NoError(t, err)
	servicePubkey, err := nostr.PubKeyFromHex(host.pubkey)
	require.NoError(t, err)
	_, err = controlplane.DecryptConfidentialServiceInner(ev.Content, func(ciphertext string) (string, error) {
		return other.Decrypt(ctx, ciphertext, servicePubkey)
	})
	require.Error(t, err, "a key other than the service key must not open the service layer")

	// A record without a success result has no service layer at all.
	bare := sampleProductionState(strings.Repeat("f6", 32))
	require.NoError(t, ledger.save(ctx, bare))
	bareEvent := host.latestLedgerRecord(t, ledgerRequestDTag(bare.RequestID))
	inner, err = host.encryptor.DecryptServiceInner(ctx, bareEvent.Content)
	require.NoError(t, err)
	require.Empty(t, inner)
}

// Removing a request publishes a tombstone at its version: the request is
// gone for this process, for a restarted one and for a fresh host, a stale
// cache file cannot resurrect it, and a new life of the request starts at
// version 1 after the tombstone. Deleting the saga run through the
// provisioner's store removes the ledger entry with it.
func TestAdapterLedgerTombstoneOnRemoval(t *testing.T) {
	ctx := context.Background()
	host := startLedgerDaemon(t, ledgerDaemonKeyHex)
	cacheDir := filepath.Join(host.dir, "adapters")
	ledger := host.ledger(t, cacheDir)
	requestID := strings.Repeat("a7", 32)
	state := ledgerSampleState(t, host, requestID)
	require.NoError(t, ledger.save(ctx, state))
	require.NoError(t, ledger.save(ctx, state))
	require.Equal(t, uint64(2), state.Version)

	sagas, err := saga.NewFileStore(filepath.Join(host.dir, "sagas"))
	require.NoError(t, err)
	run := &saga.Run{RequestID: requestID, RunID: state.RunID, RootKey: saga.DeriveKey(requestID+"/"+state.RunID, "root"), AgentID: state.AgentID, SpecHash: state.SpecHash, Stage: saga.StageFailedTerminal, Version: 1, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	require.NoError(t, sagas.Create(ctx, run))
	store := ledgerPurgingStore{Store: sagas, states: ledger}
	require.NoError(t, store.Delete(ctx, requestID, 1))
	_, err = sagas.Load(ctx, requestID)
	require.ErrorIs(t, err, saga.ErrNotFound)

	_, err = ledger.load(ctx, requestID)
	require.ErrorIs(t, err, errProductionStateNotFound)
	tombstone := host.latestLedgerRecord(t, ledgerRequestDTag(requestID))
	require.Equal(t, "true", tagValue(tombstone.Tags, kinds.CASControlStateTagDeleted))
	require.Equal(t, "2", tagValue(tombstone.Tags, ledgerTagVersion))
	require.Empty(t, tombstone.Content)
	require.NoFileExists(t, ledger.requestPath(requestID))
	require.ErrorIs(t, store.Delete(ctx, requestID, 1), saga.ErrNotFound, "the saga run is already gone")
	require.NoError(t, ledger.remove(ctx, requestID), "ledger removal is idempotent")
	require.Len(t, host.ledgerRecords(t, ledgerRequestDTag(requestID)), 1, "a repeated removal publishes nothing")

	// A stale cache file at the tombstoned version does not resurrect it.
	require.NoError(t, writeProductionJSON(ledger.requestPath(requestID), state))
	_, err = host.ledger(t, cacheDir).load(ctx, requestID)
	require.ErrorIs(t, err, errProductionStateNotFound)
	fresh := startLedgerDaemon(t, ledgerDaemonKeyHex)
	host.replicate(t, fresh)
	_, err = fresh.ledger(t, filepath.Join(fresh.dir, "adapters")).load(ctx, requestID)
	require.ErrorIs(t, err, errProductionStateNotFound)

	// A new life of the request starts over and replaces the tombstone.
	reborn := sampleProductionState(requestID)
	require.NoError(t, ledger.save(ctx, reborn))
	require.Equal(t, uint64(1), reborn.Version)
	live := host.latestLedgerRecord(t, ledgerRequestDTag(requestID))
	require.Equal(t, "false", tagValue(live.Tags, kinds.CASControlStateTagDeleted))
	require.Greater(t, live.CreatedAt, tombstone.CreatedAt)
	got, err := host.ledger(t, cacheDir).load(ctx, requestID)
	require.NoError(t, err)
	require.Equal(t, uint64(1), got.Version)
}

// A record that cannot fit one event is refused before anything is
// published or cached, and a partial seam configuration fails closed.
func TestAdapterLedgerBoundsAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	host := startLedgerDaemon(t, ledgerDaemonKeyHex)
	ledger := host.ledger(t, filepath.Join(host.dir, "adapters"))
	requestID := strings.Repeat("b8", 32)
	state := sampleProductionState(requestID)
	state.Soul.SoulMD = strings.Repeat("x", 70_000)
	err := ledger.save(ctx, state)
	require.Error(t, err)
	require.Contains(t, err.Error(), "over the 65535-byte event limit")
	require.Empty(t, host.ledgerRecords(t, ledgerRequestDTag(requestID)))
	require.NoFileExists(t, ledger.requestPath(requestID))
	require.Equal(t, uint64(0), state.Version)

	_, err = newProductionStateStore(t.TempDir(), productionLedgerSeams{Reader: host.store, Publisher: host.publisher, ServicePubkey: host.pubkey})
	require.Error(t, err)
	require.Contains(t, err.Error(), "confidential encryptor")
	_, err = newProductionStateStore(t.TempDir(), productionLedgerSeams{Reader: host.store, Publisher: host.publisher, Encryptor: host.encryptor})
	require.Error(t, err)
	require.Contains(t, err.Error(), "service identity")

	// The precedence helper retires every copy at or below a tombstone.
	type payload struct{ v uint64 }
	version := func(p *payload) uint64 { return p.v }
	best, _ := ledgerCurrent(ledgerCommitted[payload]{value: &payload{v: 3}, version: 3}, true, &ledgerCommitted[payload]{version: 3, deleted: true}, &payload{v: 3}, version)
	require.Nil(t, best)
	best, replaces := ledgerCurrent(ledgerCommitted[payload]{version: 2, deleted: true, createdAt: 10}, true, &ledgerCommitted[payload]{value: &payload{v: 3}, version: 3, createdAt: 9}, &payload{v: 1}, version)
	require.NotNil(t, best)
	require.Equal(t, uint64(3), best.v)
	require.Equal(t, nostr.Timestamp(10), replaces)
}

// The retention pass (bahia-fpubg): a terminal run past its retain_until is
// purged with its request record, and its identity reservation is released
// only when the run reserved the agent id and no Soul of the agent is live.
// Unexpired, recoverable and running runs are untouched; a failed Soul read
// leaves the run whole; a second pass changes nothing.
func TestAdapterLedgerRetentionPassRetiresExpiredRuns(t *testing.T) {
	ctx := context.Background()
	host := startLedgerDaemon(t, ledgerDaemonKeyHex)
	ledger := host.ledger(t, filepath.Join(host.dir, "adapters"))
	sagas, err := saga.NewFileStore(filepath.Join(host.dir, "sagas"))
	require.NoError(t, err)
	now := host.clock.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)

	souls := map[string]*domain.AgentSoul{}
	soulErrs := map[string]error{}
	store := ledgerPurgingStore{Store: sagas, states: ledger, souls: func(_ context.Context, agentID string) (*domain.AgentSoul, error) {
		return souls[agentID], soulErrs[agentID]
	}}

	// seed writes one request's ledger record, reserves its agent id when no
	// reservation exists, and checkpoints its saga run at stage.
	seed := func(requestID, agentID string, stage saga.Stage, retainUntil *time.Time) *saga.Run {
		t.Helper()
		spec := ProvisioningSpec{RequestID: requestID, RunID: uuid.NewSHA1(uuid.NameSpaceOID, []byte("run/"+requestID)).String(), AgentID: agentID, SpecHash: "spec-hash-" + agentID, Runtime: domain.RuntimeTargetOpenClaw}
		state := sampleProductionState(requestID)
		state.AgentID, state.Soul.AgentID, state.SpecHash, state.RunID = agentID, agentID, spec.SpecHash, spec.RunID
		require.NoError(t, ledger.save(ctx, state))
		_, _, err := ledger.reservation(ctx, spec, true)
		require.NoError(t, err)
		run, err := saga.NewRun(requestID, spec.RunID, agentID, spec.SpecHash, now.Add(-48*time.Hour))
		require.NoError(t, err)
		run.Stage, run.RetainUntil = stage, retainUntil
		require.NoError(t, sagas.Create(ctx, run))
		return run
	}
	reservationOf := func(agentID string) *productionIdentityReservation {
		t.Helper()
		reservation, _, err := ledger.reservation(ctx, ProvisioningSpec{AgentID: agentID}, false)
		require.NoError(t, err)
		return reservation
	}
	requireLive := func(run *saga.Run) {
		t.Helper()
		_, err := sagas.Load(ctx, run.RequestID)
		require.NoError(t, err, "saga run of %s", run.AgentID)
		_, err = ledger.load(ctx, run.RequestID)
		require.NoError(t, err, "ledger record of %s", run.AgentID)
	}
	requirePurged := func(run *saga.Run) {
		t.Helper()
		_, err := sagas.Load(ctx, run.RequestID)
		require.ErrorIs(t, err, saga.ErrNotFound, "saga run of %s", run.AgentID)
		_, err = ledger.load(ctx, run.RequestID)
		require.ErrorIs(t, err, errProductionStateNotFound, "ledger record of %s", run.AgentID)
		tombstone := host.latestLedgerRecord(t, ledgerRequestDTag(run.RequestID))
		require.Equal(t, "true", tagValue(tombstone.Tags, kinds.CASControlStateTagDeleted), "request tombstone of %s", run.AgentID)
	}

	free := seed(strings.Repeat("01", 32), "free", saga.StageFailedTerminal, &past)
	live := seed(strings.Repeat("02", 32), "live", saga.StageRolledBack, &past)
	souls["live"] = &domain.AgentSoul{AgentID: "live", Status: domain.SoulStatusActive}
	revoked := seed(strings.Repeat("03", 32), "revoked", saga.StageFailedTerminal, &past)
	souls["revoked"] = &domain.AgentSoul{AgentID: "revoked", Status: domain.SoulStatusRevoked}
	fresh := seed(strings.Repeat("04", 32), "fresh", saga.StageFailedTerminal, &future)
	stuck := seed(strings.Repeat("05", 32), "stuck", saga.StageFailedRecoverable, &past)
	winner := seed(strings.Repeat("06", 32), "contested", saga.StageRunning, nil)
	loser := seed(strings.Repeat("07", 32), "contested", saga.StageFailedTerminal, &past)
	require.Equal(t, winner.RequestID, reservationOf("contested").RequestID, "the first request holds the agent id")
	unreachable := seed(strings.Repeat("08", 32), "unreachable", saga.StageFailedTerminal, &past)
	soulErrs["unreachable"] = errors.New("relay read incomplete")

	removed, err := saga.PurgeExpired(ctx, store, now)
	require.ErrorContains(t, err, unreachable.RequestID)
	require.ErrorContains(t, err, "relay read incomplete")
	require.Equal(t, 4, removed, "free, live, revoked and the contested loser")

	// Purged with their request records.
	for _, run := range []*saga.Run{free, live, revoked, loser} {
		requirePurged(run)
	}
	// Untouched: unexpired, recoverable, running, and the run whose Soul could
	// not be read.
	for _, run := range []*saga.Run{fresh, stuck, winner, unreachable} {
		requireLive(run)
	}

	// Identity rule.
	require.Nil(t, reservationOf("free"), "no Soul: the agent id is released")
	require.Equal(t, "true", tagValue(host.latestLedgerRecord(t, ledgerIdentityDTag("free")).Tags, kinds.CASControlStateTagDeleted))
	require.NoFileExists(t, ledger.reservationPath("free"))
	require.Nil(t, reservationOf("revoked"), "a revoked Soul does not hold the agent id")
	require.NotNil(t, reservationOf("live"), "an active Soul keeps the agent id reserved")
	require.Equal(t, live.RequestID, reservationOf("live").RequestID)
	require.Equal(t, "false", tagValue(host.latestLedgerRecord(t, ledgerIdentityDTag("live")).Tags, kinds.CASControlStateTagDeleted))
	require.Equal(t, winner.RequestID, reservationOf("contested").RequestID, "the loser's purge leaves the winner's reservation")
	require.Equal(t, unreachable.RequestID, reservationOf("unreachable").RequestID, "a failed Soul read keeps the reservation")
	require.NotNil(t, reservationOf("fresh"))
	require.NotNil(t, reservationOf("stuck"))

	// A released agent id can be reserved again by a new request.
	again := ProvisioningSpec{RequestID: strings.Repeat("09", 32), RunID: "run-again", AgentID: "free", SpecHash: "spec-hash-free", Runtime: domain.RuntimeTargetOpenClaw}
	reservation, created, err := ledger.reservation(ctx, again, true)
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, again.RequestID, reservation.RequestID)
	require.Equal(t, uint64(1), reservation.Version)
	require.Equal(t, again.RequestID, reservationOf("free").RequestID)

	// Idempotent: a second pass retires nothing more and publishes nothing.
	records := func() int {
		total := 0
		for _, run := range []*saga.Run{free, live, revoked, fresh, stuck, winner, loser, unreachable} {
			total += len(host.ledgerRecords(t, ledgerRequestDTag(run.RequestID))) + len(host.ledgerRecords(t, ledgerIdentityDTag(run.AgentID)))
		}
		return total
	}
	before := records()
	removed, err = saga.PurgeExpired(ctx, store, now)
	require.ErrorContains(t, err, unreachable.RequestID)
	require.Equal(t, 0, removed)
	require.Equal(t, before, records())

	// Once the Soul read succeeds, the held-back run is purged and its agent
	// id released.
	delete(soulErrs, "unreachable")
	removed, err = saga.PurgeExpired(ctx, store, now)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	requirePurged(unreachable)
	require.Nil(t, reservationOf("unreachable"))

	// A fresh host sees the same outcome from the canonical records alone.
	replica := startLedgerDaemon(t, ledgerDaemonKeyHex)
	host.replicate(t, replica)
	resumed := replica.ledger(t, filepath.Join(replica.dir, "adapters"))
	_, err = resumed.load(ctx, free.RequestID)
	require.ErrorIs(t, err, errProductionStateNotFound)
	reservation, _, err = resumed.reservation(ctx, ProvisioningSpec{AgentID: "live"}, false)
	require.NoError(t, err)
	require.Equal(t, live.RequestID, reservation.RequestID)
	reservation, _, err = resumed.reservation(ctx, ProvisioningSpec{AgentID: "revoked"}, false)
	require.NoError(t, err)
	require.Nil(t, reservation)
}

// Without a Soul seam (the file-only configuration tests run with) a purge
// still retires the run and its request record but keeps the reservation:
// an agent id is never released blind.
func TestAdapterLedgerRetentionKeepsIdentityWithoutSoulLookup(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	ledger, err := newProductionStateStore(filepath.Join(dir, "adapters"), productionLedgerSeams{})
	require.NoError(t, err)
	sagas, err := saga.NewFileStore(filepath.Join(dir, "sagas"))
	require.NoError(t, err)
	requestID := strings.Repeat("0a", 32)
	spec := ledgerSpec(requestID)
	require.NoError(t, ledger.save(ctx, sampleProductionState(requestID)))
	_, _, err = ledger.reservation(ctx, spec, true)
	require.NoError(t, err)
	now := time.Now().UTC()
	past := now.Add(-time.Minute)
	run, err := saga.NewRun(requestID, spec.RunID, spec.AgentID, spec.SpecHash, now.Add(-time.Hour))
	require.NoError(t, err)
	run.Stage, run.RetainUntil = saga.StageFailedTerminal, &past
	require.NoError(t, sagas.Create(ctx, run))

	removed, err := saga.PurgeExpired(ctx, store(sagas, ledger, nil), now)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	_, err = sagas.Load(ctx, requestID)
	require.ErrorIs(t, err, saga.ErrNotFound)
	require.NoFileExists(t, ledger.requestPath(requestID))
	reservation, _, err := ledger.reservation(ctx, ProvisioningSpec{AgentID: spec.AgentID}, false)
	require.NoError(t, err)
	require.NotNil(t, reservation)
	require.FileExists(t, ledger.reservationPath(spec.AgentID))

	// With a seam that finds no Soul, the same request's run (recreated, as a
	// replay of the request would) releases the file-only reservation too.
	require.NoError(t, sagas.Create(ctx, run))
	removed, err = saga.PurgeExpired(ctx, store(sagas, ledger, func(context.Context, string) (*domain.AgentSoul, error) { return nil, nil }), now)
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	reservation, _, err = ledger.reservation(ctx, ProvisioningSpec{AgentID: spec.AgentID}, false)
	require.NoError(t, err)
	require.Nil(t, reservation)
	require.NoFileExists(t, ledger.reservationPath(spec.AgentID))
}

func store(sagas saga.Store, ledger *productionStateStore, souls soulLookup) saga.Store {
	return ledgerPurgingStore{Store: sagas, states: ledger, souls: souls}
}
