package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"github.com/google/uuid"
	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func TestF74aReceiptSurvivesSettledOutboxPruneAndRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "outbox.bolt")
	outbox, err := localstore.OpenOutbox(path)
	require.NoError(t, err)
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "pkg"}
	d := nostradapter.SBOMPackageDTag(pkg)
	event := gonostr.Event{Kind: gonostr.Kind(nostradapter.KindCASControlState), CreatedAt: gonostr.Now(), Tags: gonostr.Tags{{"d", d}, {"legacy_kind", ""}, {"deleted", "false"}}}
	event.Tags[1][1] = "" + itoa(nostradapter.KindSBOMPackageRegistry)
	event.ID[0] = 1
	ledger := f74aDeliveryLedger{store: outbox}
	hash, err := f74aSourceHash(pkg)
	require.NoError(t, err)
	require.NoError(t, ledger.stageWithHash(event, hash))
	accepted, err := ledger.prove(context.Background(), "semantic_packages", pkg)
	require.NoError(t, err)
	require.False(t, accepted)
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: repository.NostrPublishTargetControlPlane, EnqueuedAt: time.Now().Add(-26 * time.Hour)})
	require.NoError(t, err)
	_, err = outbox.CommitRound(event.ID, localstore.OutboxRound{Delivered: true, State: localstore.OutboxPublished, At: time.Now().Add(-25 * time.Hour)})
	require.NoError(t, err)
	require.NoError(t, ledger.accepted(event))
	removed, err := outbox.Prune(time.Now().Add(-24*time.Hour), time.Now().Add(-7*24*time.Hour))
	require.NoError(t, err)
	require.Equal(t, 1, removed)
	_, found, err := outbox.Get(event.ID)
	require.NoError(t, err)
	require.False(t, found)
	require.NoError(t, outbox.Close())
	reopened, err := localstore.OpenOutbox(path)
	require.NoError(t, err)
	defer reopened.Close()
	ledger = f74aDeliveryLedger{store: reopened}
	accepted, err = ledger.prove(context.Background(), "semantic_packages", pkg)
	require.NoError(t, err)
	require.True(t, accepted)
}

func TestF74aReceiptNewPendingAndRefusalInvalidateOldAcceptance(t *testing.T) {
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New()}
	makeEvent := func(id byte) gonostr.Event {
		ev := gonostr.Event{Kind: gonostr.Kind(nostradapter.KindCASControlState), Tags: gonostr.Tags{{"d", nostradapter.SBOMPackageDTag(pkg)}, {"legacy_kind", itoa(nostradapter.KindSBOMPackageRegistry)}, {"deleted", "false"}}}
		ev.ID[0] = id
		return ev
	}
	ledger := f74aDeliveryLedger{store: outbox}
	old, newer := makeEvent(1), makeEvent(2)
	hash, err := f74aSourceHash(pkg)
	require.NoError(t, err)
	require.NoError(t, ledger.stageWithHash(old, hash))
	require.NoError(t, ledger.accepted(old))
	require.NoError(t, ledger.stageWithHash(newer, hash))
	ok, err := ledger.prove(context.Background(), "semantic_packages", pkg)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, ledger.accepted(old)) // late ACK for superseded event
	ok, err = ledger.prove(context.Background(), "semantic_packages", pkg)
	require.NoError(t, err)
	require.False(t, ok)
	require.NoError(t, ledger.abandoned(newer))
	ok, err = ledger.prove(context.Background(), "semantic_packages", pkg)
	require.NoError(t, err)
	require.False(t, ok)
}

func TestF74aAcceptedCoordinateDoesNotProveChangedSourceContent(t *testing.T) {
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module", Version: "1"}
	event := gonostr.Event{Kind: gonostr.Kind(nostradapter.KindCASControlState), Tags: gonostr.Tags{{"d", nostradapter.SBOMPackageDTag(pkg)}, {"legacy_kind", itoa(nostradapter.KindSBOMPackageRegistry)}, {"deleted", "false"}}}
	event.ID[0] = 1
	ledger := f74aDeliveryLedger{store: outbox}
	hash, err := f74aSourceHash(pkg)
	require.NoError(t, err)
	require.NoError(t, ledger.stageWithHash(event, hash))
	require.NoError(t, ledger.accepted(event))
	proved, err := ledger.prove(context.Background(), "semantic_packages", pkg)
	require.NoError(t, err)
	require.True(t, proved)
	changed := *pkg
	changed.ID = uuid.New() // same semantic coordinate, different SQL source row
	require.Equal(t, nostradapter.SBOMPackageDTag(pkg), nostradapter.SBOMPackageDTag(&changed))
	proved, err = ledger.prove(context.Background(), "semantic_packages", &changed)
	require.NoError(t, err)
	require.False(t, proved)
}

func TestF74aObservationMetadataChangeKeepsAcceptedProjectionProof(t *testing.T) {
	ctx := context.Background()
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	secret, err := gonostr.SecretKeyFromHex(key)
	require.NoError(t, err)
	cfg := config.NostrConfig{PrivateKey: key, PublishEnabled: true}
	obs := &domain.RuntimeObservation{
		ID: uuid.New(), ServiceID: uuid.New(), EnvironmentID: uuid.New(),
		ObservedImageDigest: "sha256:old", ObservedContainerID: "container", ObservedAt: time.Now().UTC(),
		Metadata: map[string]any{"password": "old secret"},
	}
	capture := &f74aProjectionCapture{}
	seed := nostradapter.NewProjector(cfg, (*service.RegistryService)(nil), capture, nil, zap.NewNop())
	require.NoError(t, nostradapter.NewF74aCanonicalPublisher(seed, nil).PublishRuntimeObservation(ctx, obs))
	require.Len(t, capture.events, 1)
	event := capture.events[0]
	hash, err := f74aSourceHash(obs)
	require.NoError(t, err)
	var published map[string]any
	require.NoError(t, json.Unmarshal([]byte(event.Content), &published))
	delete(published, "observed_at") // the projector excludes this volatile field from its fingerprint
	publishedJSON, err := json.Marshal(published)
	require.NoError(t, err)
	publishedDigest := sha256.Sum256(publishedJSON)
	require.Equal(t, hex.EncodeToString(publishedDigest[:]), hash, "receipt digest must match the actual published record")
	dir := t.TempDir()
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	defer store.Close()
	_, err = store.SaveEvent(event)
	require.NoError(t, err)
	ledger := f74aDeliveryLedger{store: outbox, author: secret.Public()}
	require.NoError(t, ledger.stageWithHash(event, hash))
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: event, Target: repository.NostrPublishTargetControlPlane})
	require.NoError(t, err)
	_, err = outbox.CommitRound(event.ID, localstore.OutboxRound{Delivered: true, State: localstore.OutboxPublished, At: time.Now()})
	require.NoError(t, err)
	require.NoError(t, ledger.accepted(event))
	changed := *obs
	changed.Metadata = map[string]any{"password": "new secret"}
	changed.ObservedHost = "sql-only-host"
	changed.ObservedAt = changed.ObservedAt.Add(time.Minute)
	restartedPub := nostradapter.NewPublisher(cfg, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		nostradapter.WithPublishTarget(repository.NostrPublishTargetControlPlane), nostradapter.WithLocalOutbox(outbox, store))
	defer restartedPub.Close()
	history := nostradapter.NewLocalEventRepository(store, nil).Authored(secret.Public().Hex())
	restarted := nostradapter.NewProjector(cfg, (*service.RegistryService)(nil), f74aTrackedPublisher{Publisher: restartedPub, ledger: ledger}, history, zap.NewNop())
	recordPub := f74aRecordPublisher{inner: nostradapter.NewF74aCanonicalPublisher(restarted, nil)}
	require.NoError(t, recordPub.PublishRuntimeObservation(ctx, &changed))
	entries, err := outbox.ListEntries([]string{localstore.OutboxPending, localstore.OutboxPublished, localstore.OutboxFailed}, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1, "SQL-only metadata must not create another relay event")
	proved, err := ledger.prove(ctx, "observations", &changed)
	require.NoError(t, err)
	require.True(t, proved, "metadata-only change must retain durable relay proof")
}

func TestF74aLegacyTombstoneIgnoresMutablePackageFields(t *testing.T) {
	ctx := context.Background()
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	secret, err := gonostr.SecretKeyFromHex(key)
	require.NoError(t, err)
	cfg := config.NostrConfig{PrivateKey: key, PublishEnabled: true}
	pkg := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "old", Version: "1"}
	dir := t.TempDir()
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	defer store.Close()
	ledger := f74aDeliveryLedger{store: outbox, author: secret.Public()}
	history := nostradapter.NewLocalEventRepository(store, nil).Authored(secret.Public().Hex())
	initialPub := nostradapter.NewPublisher(cfg, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		nostradapter.WithPublishTarget(repository.NostrPublishTargetControlPlane), nostradapter.WithLocalOutbox(outbox, store))
	defer initialPub.Close()
	initial := nostradapter.NewProjector(cfg, (*service.RegistryService)(nil), f74aTrackedPublisher{Publisher: initialPub, ledger: ledger}, history, zap.NewNop())
	recordPub := f74aRecordPublisher{inner: nostradapter.NewF74aCanonicalPublisher(initial, nil)}
	require.NoError(t, recordPub.PublishLegacySBOMPackageTombstone(ctx, pkg))
	entries, err := outbox.ListEntries([]string{localstore.OutboxPending, localstore.OutboxPublished, localstore.OutboxFailed}, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	event := entries[0].Event
	var published struct {
		ID      uuid.UUID `json:"id"`
		Deleted bool      `json:"deleted"`
	}
	require.NoError(t, json.Unmarshal([]byte(event.Content), &published))
	require.True(t, published.Deleted)
	require.Equal(t, pkg.ID, published.ID)
	require.Equal(t, pkg.SBOMID.String(), f74aEventTag(event, "sbom_id"))
	hash, err := f74aSourceHash(f74aLegacyTombstoneIdentity(pkg))
	require.NoError(t, err)
	publishedHash, err := f74aSourceHash(map[string]any{"id": published.ID, "sbom_id": f74aEventTag(event, "sbom_id")})
	require.NoError(t, err)
	require.Equal(t, publishedHash, hash, "receipt must bind only the signed tombstone content and tag")
	receipt, found, err := ledger.receipt(nostradapter.KindSBOMPackageRegistry, "artifact:sbom-package:"+pkg.ID.String())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, hash, receipt.SourceHash, "import publisher must stage the tombstone identity")
	_, err = outbox.CommitRound(event.ID, localstore.OutboxRound{Delivered: true, State: localstore.OutboxPublished, At: time.Now()})
	require.NoError(t, err)
	require.NoError(t, ledger.accepted(event))
	changed := *pkg
	changed.Name, changed.Version, changed.PURL = "new", "2", "pkg:example/new@2"
	restartedPub := nostradapter.NewPublisher(cfg, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(),
		nostradapter.WithPublishTarget(repository.NostrPublishTargetControlPlane), nostradapter.WithLocalOutbox(outbox, store))
	defer restartedPub.Close()
	restarted := nostradapter.NewProjector(cfg, (*service.RegistryService)(nil), f74aTrackedPublisher{Publisher: restartedPub, ledger: ledger}, history, zap.NewNop())
	recordPub = f74aRecordPublisher{inner: nostradapter.NewF74aCanonicalPublisher(restarted, nil)}
	require.NoError(t, recordPub.PublishLegacySBOMPackageTombstone(ctx, &changed))
	entries, err = outbox.ListEntries([]string{localstore.OutboxPending, localstore.OutboxPublished, localstore.OutboxFailed}, 10)
	require.NoError(t, err)
	require.Len(t, entries, 1, "unchanged UUID tombstone must reuse the accepted signed event")
	proved, err := ledger.prove(ctx, "legacy_packages", &changed)
	require.NoError(t, err)
	require.True(t, proved, "changed semantic fields must not invalidate the UUID tombstone proof")
	changed.SBOMID = uuid.New()
	proved, err = ledger.prove(ctx, "legacy_packages", &changed)
	require.NoError(t, err)
	require.False(t, proved, "changed published SBOM tag must invalidate the proof")
}

func TestF74aOwnerOnlyFleetRecipientCanDecrypt(t *testing.T) {
	ctx := context.Background()
	serviceKey := "0000000000000000000000000000000000000000000000000000000000000001"
	ownerKey := "0000000000000000000000000000000000000000000000000000000000000002"
	serviceSecret, err := gonostr.SecretKeyFromHex(serviceKey)
	require.NoError(t, err)
	ownerSecret, err := gonostr.SecretKeyFromHex(ownerKey)
	require.NoError(t, err)
	ownerPK := ownerSecret.Public().Hex()
	trust := f74aMigrationTrustSet(config.NostrConfig{BootstrapOwners: map[string]string{"org": ownerPK}}, zap.NewNop())
	members := controlplane.NewTrustSetMemberSource(trust, nil)
	recipients, err := f74aRecipients(ctx, serviceSecret.Public().Hex(), members)
	require.NoError(t, err)
	require.Contains(t, recipients, ownerPK)
	serviceSigner, err := controlplane.NewPrivateKeySigner(serviceKey)
	require.NoError(t, err)
	ownerSigner, err := controlplane.NewPrivateKeySigner(ownerKey)
	require.NoError(t, err)
	envelope := &f74aCaptureEnvelope{}
	manager := controlplane.NewOCKManager(controlplane.OCKManagerConfig{Signer: serviceSigner, ServicePubkey: serviceSecret.Public().Hex(), Publisher: envelope, Members: members, Logger: zap.NewNop()})
	key, err := manager.RotateKey(ctx, kinds.FleetOCKScope)
	require.NoError(t, err)
	var recovered controlplane.OrgContentKey
	for _, content := range envelope.contents {
		plain, err := ownerSigner.Decrypt(ctx, content, serviceSecret.Public())
		if err != nil {
			continue
		}
		candidate, recipient, err := controlplane.UnmarshalOCKWrap([]byte(plain))
		if err == nil && recipient == ownerPK {
			recovered = candidate
			break
		}
	}
	require.Equal(t, key.Version, recovered.Version)
	require.Equal(t, key.Key, recovered.Key)
}

type f74aCaptureEnvelope struct{ contents []string }

func (c *f74aCaptureEnvelope) PublishKeyEnvelope(_ context.Context, _ string, content string) error {
	c.contents = append(c.contents, content)
	return nil
}
func itoa(v int) string { return fmt.Sprintf("%d", v) }

func TestF74aOCKEnvelopesMustAllBeQuorumAccepted(t *testing.T) {
	ctx := context.Background()
	outbox, err := localstore.OpenOutbox(filepath.Join(t.TempDir(), "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	ledger := f74aDeliveryLedger{store: outbox}
	ds := []string{"org-key:fleet:v3:service", "org-key:fleet:v3:owner"}
	manifest := f74aOCKManifest{Version: 3, KeyHash: "hash", Recipients: []string{"service", "owner"}, Coordinates: ds}
	raw, err := json.Marshal(manifest)
	require.NoError(t, err)
	require.NoError(t, outbox.PutControlRecord(f74aOCKManifestFamily, f74aOCKManifestID(gonostr.PubKey{}), raw))
	makeEvent := func(id byte, d string) gonostr.Event {
		ev := gonostr.Event{Kind: gonostr.Kind(nostradapter.KindCASControlState), Tags: gonostr.Tags{{"d", d}, {"legacy_kind", itoa(nostradapter.KindOrgKeyEnvelope)}, {"deleted", "false"}}}
		ev.ID[0] = id
		return ev
	}
	service, owner := makeEvent(1, ds[0]), makeEvent(2, ds[1])
	require.NoError(t, ledger.stage(service))
	require.NoError(t, ledger.accepted(service))
	require.NoError(t, ledger.stage(owner))
	ready, err := ledger.proveOCK(ctx, outbox, 3)
	require.NoError(t, err)
	require.False(t, ready)
	require.NoError(t, ledger.abandoned(owner))
	ready, err = ledger.proveOCK(ctx, outbox, 3)
	require.NoError(t, err)
	require.False(t, ready)
	require.NoError(t, ledger.accepted(owner)) // late valid quorum acceptance
	ready, err = ledger.proveOCK(ctx, outbox, 3)
	require.NoError(t, err)
	require.True(t, ready)
	for _, malformed := range []f74aOCKManifest{
		{Version: 3, KeyHash: "hash", Recipients: []string{"service", "owner"}, Coordinates: []string{ds[0], ds[0]}},
		{Version: 3, KeyHash: "hash", Recipients: []string{"service", "service"}, Coordinates: ds},
	} {
		duplicate, err := json.Marshal(malformed)
		require.NoError(t, err)
		require.NoError(t, outbox.PutControlRecord(f74aOCKManifestFamily, f74aOCKManifestID(gonostr.PubKey{}), duplicate))
		ready, err = ledger.proveOCK(ctx, outbox, 3)
		require.NoError(t, err)
		require.False(t, ready, "one accepted envelope must not prove multiple recipients")
		_, err = f74aPrepareOCK(ctx, outbox, nil, nil, malformed.Recipients, gonostr.PubKey{})
		require.ErrorContains(t, err, "duplicates", "restart must reject a malformed manifest before rotating")
	}
	require.NoError(t, outbox.PutControlRecord(f74aOCKManifestFamily, f74aOCKManifestID(gonostr.PubKey{}), raw))
	newer := makeEvent(3, ds[1])
	require.NoError(t, ledger.stage(newer))
	require.NoError(t, ledger.accepted(owner)) // superseded late callback cannot satisfy newer event
	ready, err = ledger.proveOCK(ctx, outbox, 3)
	require.NoError(t, err)
	require.False(t, ready)
}

// The tracked publisher must promote Publisher's package-coordinate retained
// lookup. The local event cache is deliberately empty: only the signed outbox
// row survived the simulated enqueue-before-cache crash.
func TestF74aTrackedPublisherHydratesOutboxOnlyCoordinate(t *testing.T) {
	ctx := context.Background()
	key := "0000000000000000000000000000000000000000000000000000000000000001"
	secret, err := gonostr.SecretKeyFromHex(key)
	require.NoError(t, err)
	cfg := config.NostrConfig{PrivateKey: key, PublishEnabled: true}
	original := &domain.SBOMPackage{ID: uuid.New(), SBOMID: uuid.New(), Name: "module", Version: "1"}
	capture := &f74aProjectionCapture{}
	seed := nostradapter.NewProjector(cfg, (*service.RegistryService)(nil), capture, nil, zap.NewNop())
	require.NoError(t, nostradapter.NewF74aCanonicalPublisher(seed, nil).PublishSBOMPackage(ctx, original))
	require.Len(t, capture.events, 1)
	held := capture.events[0]
	held.CreatedAt = gonostr.Now() + 3600
	require.NoError(t, held.Sign(secret))
	dir := t.TempDir()
	outbox, err := localstore.OpenOutbox(filepath.Join(dir, "outbox.bolt"))
	require.NoError(t, err)
	defer outbox.Close()
	store, err := localstore.Open(filepath.Join(dir, "events.bolt"))
	require.NoError(t, err)
	defer store.Close()
	_, err = outbox.Enqueue(localstore.OutboxEntry{Event: held, Target: repository.NostrPublishTargetControlPlane})
	require.NoError(t, err)
	raw := nostradapter.NewPublisher(cfg, nostradapter.NewRelayPool(nil, zap.NewNop()), nil, zap.NewNop(), nostradapter.WithPublishTarget(repository.NostrPublishTargetControlPlane), nostradapter.WithLocalOutbox(outbox, store))
	defer raw.Close()
	ledger := f74aDeliveryLedger{store: outbox, author: secret.Public()}
	history := nostradapter.NewLocalEventRepository(store, nil).Authored(secret.Public().Hex())
	wrapped := f74aTrackedPublisher{Publisher: raw, ledger: ledger}
	restarted := nostradapter.NewProjector(cfg, (*service.RegistryService)(nil), wrapped, history, zap.NewNop())
	canonical := nostradapter.NewF74aCanonicalPublisher(restarted, nil)
	require.NoError(t, canonical.PublishSBOMPackage(ctx, original), "unchanged row must reuse held signed event")
	counts, err := outbox.Counts()
	require.NoError(t, err)
	require.Equal(t, int64(1), counts.Pending)
	changed := *original
	changed.ID = uuid.New()                                                       // same semantic coordinate, changed payload
	_ = (f74aRecordPublisher{inner: canonical}).PublishSBOMPackage(ctx, &changed) // no relay in fixture
	entries, err := outbox.ListEntries([]string{localstore.OutboxPending, localstore.OutboxPublished, localstore.OutboxFailed}, 10)
	require.NoError(t, err)
	require.Len(t, entries, 2, "changed row must enqueue a new signed event")
	var newer gonostr.Event
	for _, entry := range entries {
		if entry.Event.ID != held.ID {
			newer = entry.Event
		}
	}
	require.NotEqual(t, gonostr.ZeroID, newer.ID)
	require.Greater(t, int64(newer.CreatedAt), int64(held.CreatedAt), "new event must supersede outbox-only retained timestamp")
	receipt, found, err := ledger.receipt(nostradapter.KindSBOMPackageRegistry, nostradapter.SBOMPackageDTag(&changed))
	require.NoError(t, err)
	require.True(t, found)
	changedHash, err := f74aSourceHash(&changed)
	require.NoError(t, err)
	require.Equal(t, changedHash, receipt.SourceHash, "signed event must retain the exact SQL source-row digest")
}

type f74aProjectionCapture struct{ events []gonostr.Event }

func (c *f74aProjectionCapture) PublishProjection(_ context.Context, ev gonostr.Event, _ string, _ *uuid.UUID) error {
	c.events = append(c.events, ev)
	return nil
}
