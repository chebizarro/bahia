//go:build integration

package identityrecovery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	"fiatjaf.com/nostr/nip11"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/config"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/relayguard"
	"github.com/openagentsinc/bahia/internal/trustepoch"
)

type rehearsalInventory []string

func (i rehearsalInventory) IDs(context.Context, string, string) ([]string, error) { return i, nil }

func rehearsalServer(t *testing.T) (string, *slicestore.SliceStore) {
	t.Helper()
	relay := khatru.NewRelay()
	store := &slicestore.SliceStore{}
	if err := store.Init(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	const perREQ = 7
	relay.UseEventstore(store, perREQ)
	relay.Info.Limitation = &nip11.RelayLimitationDocument{MaxLimit: perREQ}
	server := httptest.NewServer(relay)
	t.Cleanup(server.Close)
	return "ws" + strings.TrimPrefix(server.URL, "http"), store
}

func TestStagingIdentityRecoveryRehearsal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	controlled, controlledStore := rehearsalServer(t)
	cleanup, cleanupStore := rehearsalServer(t)
	if controlled == cleanup {
		t.Fatal("cleanup relay must be distinct")
	}
	operator, newKey, old := signer(t), signer(t), signer(t)
	oldPub, newPub, root := pub(t, old), pub(t, newKey), pub(t, operator)
	dir := t.TempDir()
	daemonPath := filepath.Join(dir, "daemon.bolt")
	ledgerPath := filepath.Join(dir, "recovery.bolt")
	store, err := localstore.Open(daemonPath)
	if err != nil {
		t.Fatal(err)
	}
	var accepted []string
	seed := func(ev nostr.Event) {
		t.Helper()
		if err := controlledStore.SaveEvent(ev); err != nil {
			t.Fatal(err)
		}
		if err := cleanupStore.SaveEvent(ev); err != nil {
			t.Fatal(err)
		}
		if _, err := store.SaveEvent(ev); err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, ev.ID.Hex())
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	svc := &domain.Service{ID: uuid.New(), OrgID: uuid.New(), Name: "api", ArtifactRepo: "registry.example/api", RuntimeType: domain.RuntimeTypeDocker, CreatedAt: now, UpdatedAt: now}
	kind, tags, content := adapter.ServiceRegistryRecoveryRecord(svc, false)
	snapshotOld := signedRegistryHead(t, old, kind, tags, content, 100)
	accepted = append(accepted, snapshotOld.ID.Hex())
	svc.Name = "api-current"
	_, currentTags, currentContent := adapter.ServiceRegistryRecoveryRecord(svc, false)
	currentService := signedRegistryHead(t, old, kind, currentTags, currentContent, 110)
	seed(currentService)
	snapshotUnapproved := signedRegistryHead(t, old, 1, nostr.Tags{{"t", "historical"}}, "not operator approved", 99)
	snapshot, snapshotRaw := writeSnapshot(t, snapshotLine(t, snapshotOld), snapshotLine(t, snapshotUnapproved))
	recipe := &domain.BackupRecipe{ID: uuid.New(), OrgID: svc.OrgID, Name: "daily", Version: "v1", Backend: domain.BackupBackendKopia, RepositoryID: uuid.New(), TargetRef: "service:api", CreatedAt: now, UpdatedAt: now}
	backupTags, backupContent := adapter.BackupRecipeRegistryRecord(recipe, false)
	backupKind, backupEnvelope := adapter.ControlStateEnvelope(adapter.KindBackupRecipeRegistry, adapter.BackupRecipeDTag(recipe.ID), false)
	seed(signedRegistryHead(t, old, backupKind, append(backupEnvelope, backupTags...), backupContent, 101))
	securityOCK, err := controlplane.GenerateOrgContentKey(kinds.FleetOCKScope, 1)
	if err != nil {
		t.Fatal(err)
	}
	securityContent, err := controlplane.EncryptConfidentialContent(ctx, securityOCK, []byte(`{"canonical":"unchanged"}`), controlplane.ConfidentialRecordContext{LegacyKind: adapter.KindSecurityTargetRecord, DTag: "security:target:rehearsal", Topic: securityRecoveryTopic(adapter.KindSecurityTargetRecord)}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	securityKind, securityEnvelope := adapter.ControlStateEnvelope(adapter.KindSecurityTargetRecord, "security:target:rehearsal", false)
	securityTags := append(securityEnvelope, nostr.Tag{"target_key_hash", "rehearsal"}, nostr.Tag{"target_type", "sbom"}, nostr.Tag{"state_hash", hex.EncodeToString(make([]byte, 32))})
	seed(signedRegistryHead(t, old, securityKind, securityTags, securityContent, 102))
	channel := &domain.NotificationChannel{ID: uuid.New(), OrgID: svc.OrgID, Name: "oncall", ChannelType: domain.ChannelTypeWebhook, Config: map[string]any{"url": "https://private.invalid/hook", "secret": "test-secret"}, Enabled: true}
	d := adapter.NotificationChannelDTag(channel.ID)
	ock, err := controlplane.GenerateOrgContentKey(channel.OrgID.String(), 1)
	if err != nil {
		t.Fatal(err)
	}
	_, plain := adapter.NotificationChannelRegistryRecord(channel, false, true)
	notificationContent, err := controlplane.EncryptConfidentialContent(ctx, ock, []byte(`{"id":"visible"}`), controlplane.ConfidentialRecordContext{LegacyKind: adapter.KindNotificationChannelRegistry, DTag: d, Topic: kinds.CPStateTopicNotificationChannelRegistry}, []byte(plain), func(ctx context.Context, p string) (string, error) { return old.Encrypt(ctx, p, oldPub) })
	if err != nil {
		t.Fatal(err)
	}
	notificationKind, notificationTags := adapter.ControlStateEnvelope(adapter.KindNotificationChannelRegistry, d, false)
	notificationOld := signedRegistryHead(t, old, notificationKind, notificationTags, notificationContent, 103)
	seed(notificationOld)
	for i, key := range []controlplane.OrgContentKey{securityOCK, ock} {
		wrapped, err := controlplane.MarshalOCKWrap(key, oldPub.Hex())
		if err != nil {
			t.Fatal(err)
		}
		cipher, err := old.Encrypt(ctx, string(wrapped), oldPub)
		if err != nil {
			t.Fatal(err)
		}
		dTag := "org-key:" + key.OrgID + ":v1:" + oldPub.Hex()
		kind, tags := adapter.ControlStateEnvelope(adapter.KindOrgKeyEnvelope, dTag, false)
		seed(signedRegistryHead(t, old, kind, tags, cipher, int64(105+i)))
	}
	// These reviewed events exist only on relays, beyond a single REQ's
	// advertised cap. Pairs share a created_at across page boundaries.
	for i := range 12 {
		ev := signedRegistryHead(t, old, 1, nostr.Tags{{"t", "historical"}}, fmt.Sprintf("approved historical %d", i), int64(200+i/2))
		if err := controlledStore.SaveEvent(ev); err != nil {
			t.Fatal(err)
		}
		if err := cleanupStore.SaveEvent(ev); err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, ev.ID.Hex())
	}
	// A historical old-key event visible only on the external cleanup relay is
	// deliberately not accepted as canonical, but must still receive deletion.
	externalOnly := signedRegistryHead(t, old, 1, nostr.Tags{{"t", "historical"}}, "historical", 104)
	if err := cleanupStore.SaveEvent(externalOnly); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	cfg := config.Defaults()
	cfg.DB = config.DBConfig{}
	cfg.Nostr.ServiceID = "rehearsal-service"
	cfg.Nostr.OperatorRootPubkey = root.Hex()
	cfg.Nostr.PublicKey = oldPub.Hex()
	cfg.Nostr.LocalStore.Path = daemonPath
	cfg.Nostr.ControlledRelays = []string{controlled}
	cfg.Nostr.Relays = []string{controlled}
	cfg.Nostr.ServiceRelays = []string{controlled}
	cfg.Nostr.BrowserRelays = []string{controlled}
	cfg.Nostr.ContextVMRelays = []string{controlled}
	keys := map[string]nostr.Keyer{"operator": operator, "new": newKey, "old": old}
	authorities := map[string]string{"daemon": newPub.Hex(), "cli": root.Hex(), "web": root.Hex(), "pinned-runtimes": root.Hex()}
	for _, id := range []string{"sidecar", "dns"} {
		key := signer(t)
		keys[id] = key
		authorities[id] = pub(t, key).Hex()
	}
	authorities["loom"], authorities["hiveci"], authorities["soulfactory"] = root.Hex(), root.Hex(), root.Hex()
	signerConfig := func(key nostr.Keyer) config.ConfigFabricConfig {
		return config.ConfigFabricConfig{PublicKey: pub(t, key).Hex(), Signer: config.NostrSignerConfig{Method: config.NostrSignerNIP55L}}
	}
	purgePath := filepath.Join(dir, "purge.json")
	updatesPath := filepath.Join(dir, "rotation.json")
	purgeRaw, _ := json.Marshal(map[string]string{"archive-artifacts": "archive-digest", "pg-backups": "backup-digest", "wal": "wal-digest", "controlled-re-encryption": "reencryption-digest"})
	if err := os.WriteFile(purgePath, purgeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg.IdentityRecovery = config.IdentityRecoveryConfig{LedgerPath: ledgerPath, OldPubkey: oldPub.Hex(), ReasonCode: "compromised", ControlledRelays: []string{controlled}, CleanupRelays: []string{controlled, cleanup}, ConsumerRoster: trustepoch.ConsumerIDs(), AcknowledgmentAuthorities: authorities, AcceptedEventIDs: accepted, VerifiedSnapshots: []config.VerifiedSnapshotConfig{{Path: snapshot.Path, SHA256: snapshot.SHA256}}, PurgeEvidencePath: purgePath, Operator: signerConfig(operator), NewIdentity: signerConfig(newKey), OldSigner: signerConfig(old)}
	cfg.SecretStore.SystemKeys.AssistantTranscript = []config.SystemKeyConfig{{KeyID: "k1", CredentialID: "cred_" + strings.Repeat("a", 64), Version: 1}}
	budget := nostrout.PurposeBudget{RatePerMinute: 100000, Burst: 100000}
	admission := nostrout.New(nostrout.Config{Aggregate: budget, PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{nostrout.PurposePriority: budget, nostrout.PurposeState: budget, nostrout.PurposeGeneral: budget, nostrout.PurposeBulk: budget, nostrout.PurposeSigner: budget}, RelayWire: budget, RelayWirePriority: budget})
	opts := RuntimeOptions{OutboundAdmission: admission, RotationUpdatesPath: updatesPath, OpenSigner: func(_ context.Context, c config.ConfigFabricConfig) (nostr.Keyer, error) {
		for _, key := range keys {
			if pub(t, key).Hex() == c.PublicKey {
				return key, nil
			}
		}
		return nil, errors.New("unrecognized rehearsal signer")
	}}
	open := func() *ProductionRuntime {
		runtime, err := NewProductionRuntime(ctx, cfg, opts)
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	runtime := open()
	defer func() {
		if runtime != nil {
			_ = runtime.Close()
		}
	}()
	engine := runtime.Engine
	if _, ok := engine.Acks.(*AcknowledgmentReader); !ok {
		t.Fatal("production acknowledgment reader absent")
	}
	if _, ok := engine.Rotation.(*LedgerRotationTracker); !ok {
		t.Fatal("production rotation tracker absent")
	}
	if _, ok := engine.Cleanup.(CleanupRelaySource); !ok {
		t.Fatal("production cleanup publisher absent")
	}
	if _, ok := runtime.SQLDrafts.Rows.(PGReviewRows); !ok {
		t.Fatal("production SQL reporter absent")
	}
	for _, name := range engine.RequiredDomains {
		if engine.Domains[name] == nil {
			t.Fatalf("production domain %s absent", name)
		}
	}
	var resolved trustepoch.Resolution
	for _, phase := range Phases {
		switch phase {
		case Contain:
			state, err := engine.Status()
			if err != nil {
				t.Fatal(err)
			}
			marker := []byte(`{"incident_id":"` + state.IncidentID + `"}`)
			if err := os.WriteFile(cfg.Nostr.LocalStore.ResolvedOutboxPath()+".identity-containment", marker, 0o600); err != nil {
				t.Fatal(err)
			}
		case Inventory:
			tampered := append([]byte(nil), snapshotRaw...)
			tampered[len(tampered)/2] ^= 1
			if err := os.WriteFile(snapshot.Path, tampered, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := engine.Apply(ctx, Inventory); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
				t.Fatalf("tampered snapshot accepted: %v", err)
			}
			failed, err := engine.Status()
			if err != nil {
				t.Fatal(err)
			}
			if failed.InventoryDigest != "" {
				t.Fatal("tampered snapshot advanced inventory")
			}
			if _, ok := failed.Receipt(Inventory); ok {
				t.Fatal("tampered snapshot got completion receipt")
			}
			if err := os.WriteFile(snapshot.Path, snapshotRaw, 0o600); err != nil {
				t.Fatal(err)
			}
		case CutActivateTrust:
			if err := engine.Apply(ctx, phase); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("cut activated without readiness: %v", err)
			}
			for _, id := range trustepoch.ConsumerIDs() {
				if id == "dns" {
					continue
				}
				proof := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "rehearsal-enforcement-state:" + id}
				if err := operator.SignEvent(ctx, &proof); err != nil {
					t.Fatal(err)
				}
				if err := engine.Source.Publish(ctx, proof); err != nil {
					t.Fatal(err)
				}
				reason := ""
				if id == "soulfactory" || id == "pinned-runtimes" {
					reason = trustepoch.WaiverConsumerIsolated
				}
				if err := engine.AttestReadiness(ctx, id, []string{proof.ID.Hex()}, reason); err != nil {
					t.Fatalf("readiness %s: %v", id, err)
				}
			}
			if err := engine.Apply(ctx, phase); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("cut activated without DNS readiness: %v", err)
			}
			dnsProof := nostr.Event{Kind: 1, CreatedAt: nostr.Now(), Content: "rehearsal-enforcement-state:dns"}
			if err := operator.SignEvent(ctx, &dnsProof); err != nil {
				t.Fatal(err)
			}
			if err := engine.Source.Publish(ctx, dnsProof); err != nil {
				t.Fatal(err)
			}
			if err := engine.AttestReadiness(ctx, "dns", []string{dnsProof.ID.Hex()}, ""); err != nil {
				t.Fatal(err)
			}
		case RotateExternalSecrets:
			refs, err := engine.Rotation.Inventory(ctx)
			if err != nil || len(refs) != 1 {
				t.Fatalf("rotation inventory: %v %v", refs, err)
			}
			replacement := domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("b", 64), Version: 2}
			raw, _ := json.Marshal([]RotationUpdate{{RefID: refs[0].ID, NewRef: &replacement, SourceRotationEvidenceID: "source-rotation-digest"}})
			if err := os.WriteFile(updatesPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		case Verify:
			if err := engine.Apply(ctx, phase); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("verification accepted missing consumer acks: %v", err)
			}
			for _, id := range []string{"cli", "web", "pinned-runtimes", "loom", "hiveci"} {
				if err := engine.AttestAcknowledgment(ctx, id); err != nil {
					t.Fatalf("operator ack %s: %v", id, err)
				}
			}
			publishComponent := func(id string, key nostr.Keyer) {
				t.Helper()
				publisher := trustepoch.AckPublisher{ConsumerID: id, Authority: pub(t, key).Hex(),
					OperatorRoot: root.Hex(), Existing: engine.Source.Read,
					Sign: key.SignEvent, Publish: engine.Source.Publish}
				if err := publisher.Acknowledge(ctx, resolved); err != nil {
					t.Fatalf("production ack publisher %s: %v", id, err)
				}
			}
			publishComponent("daemon", newKey)
			publishComponent("sidecar", keys["sidecar"])
			state, err := engine.Status()
			if err != nil {
				t.Fatal(err)
			}
			missingBeforeDNS, err := engine.Acks.Missing(ctx, state.Epoch, engine.Config.ConsumerRoster)
			if err != nil || !slices.Contains(missingBeforeDNS, "dns") {
				t.Fatalf("missing production DNS ack did not block verification: %v %v", missingBeforeDNS, err)
			}
			publishComponent("dns", keys["dns"])
			missingAfterDNS, err := engine.Acks.Missing(ctx, state.Epoch, engine.Config.ConsumerRoster)
			if err != nil || slices.Contains(missingAfterDNS, "dns") || slices.Contains(missingAfterDNS, "daemon") || slices.Contains(missingAfterDNS, "sidecar") {
				t.Fatalf("production publisher acks not admitted: %v %v", missingAfterDNS, err)
			}
		}
		if phase == Verify {
			if err := engine.Resume(ctx, phase); !errors.Is(err, ErrIncomplete) {
				t.Fatalf("pending consumers unexpectedly passed verification: %v", err)
			}
			state, err := engine.Status()
			if err != nil {
				t.Fatal(err)
			}
			missing, err := engine.Acks.Missing(ctx, state.Epoch, engine.Config.ConsumerRoster)
			if err != nil || !slices.Contains(missing, "soulfactory") || !slices.Contains(missing, "pinned-runtimes") {
				t.Fatalf("pending consumer policy not enforced: %v %v", missing, err)
			}
			if _, ok := state.Receipt(Verify); ok {
				t.Fatal("verify completed despite pending enforcers")
			}
			if next, _ := state.Next(); next != Verify {
				t.Fatal("blocked phase advanced")
			}
			if err := engine.Apply(ctx, RetireOldIdentity); err == nil {
				t.Fatal("retired old identity without verification")
			}
			break
		}
		if err := engine.Resume(ctx, phase); err != nil {
			t.Fatalf("resume %s: %v", phase, err)
		}
		state, err := engine.Status()
		if err != nil {
			t.Fatal(err)
		}
		receipt, ok := state.Receipt(phase)
		if !ok || receipt.Status != "completed" || len(receipt.EvidenceIDs) == 0 {
			t.Fatalf("%s completion receipt absent", phase)
		}
		switch phase {
		case Contain:
			if !slices.Contains(receipt.EvidenceIDs, "containment:runtime-kill-switch:"+state.IncidentID) {
				t.Fatal("seeded containment marker not verified")
			}
		case Inventory:
			if state.InventorySources[snapshotOld.ID.Hex()] != "snapshot:"+snapshot.SHA256[:12] || receipt.InventorySources[snapshotOld.ID.Hex()] != state.InventorySources[snapshotOld.ID.Hex()] || state.InventorySources[currentService.ID.Hex()] != "relay" || receipt.InventorySourceDefault != "relay" || receipt.InventorySources[currentService.ID.Hex()] != "" || len(receipt.InventorySnapshots) != 1 || receipt.InventorySnapshots[0].EventCount != 2 || !slices.Equal(receipt.InventorySnapshots[0].AdmittedIDs, []string{snapshotOld.ID.Hex()}) || slices.Contains(receipt.InventorySnapshots[0].AdmittedIDs, snapshotUnapproved.ID.Hex()) {
				t.Fatalf("snapshot provenance/admission: state=%v receipt=%+v", state.InventorySources, receipt.InventorySnapshots)
			}
			if len(state.Observed) != len(accepted)+1 || len(receipt.InventoryRelays) != 2 {
				t.Fatal("historical external inventory or signed relay proof absent")
			}
			for _, relay := range []string{controlled, cleanup} {
				proof := state.CleanupInventory[relay]
				if proof.Status != "complete" || proof.PageProof == nil || !proof.PageProof.FinalEmptyPage || proof.PageProof.Pages < 3 || proof.PageProof.PageLimit != 7 || proof.PageProof.NIP11MaxLimit != 7 {
					t.Fatalf("relay %s pagination proof incomplete: %+v", relay, proof)
				}
				if proof.PageProof.UniqueIDs < len(accepted)-1 || len(proof.PageProof.Windows) != proof.PageProof.Pages || proof.PageProof.Windows[proof.PageProof.Pages-1].Events != 0 {
					t.Fatalf("relay %s pagination chain incomplete: %+v", relay, proof.PageProof)
				}
				sum := sha256.Sum256([]byte(relayguard.Normalize(relay)))
				if signed := receipt.InventoryRelays[hex.EncodeToString(sum[:])]; !reflect.DeepEqual(signed, proof) {
					t.Fatal("signed pagination proof differs from ledger")
				}
			}
		case CutActivateTrust:
			heads, err := engine.Source.Read(ctx, nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{root}, Tags: nostr.TagMap{"d": {"trust-epoch:" + cfg.Nostr.ServiceID}}})
			if err != nil || len(heads) != 1 {
				t.Fatalf("trust head: %v %d", err, len(heads))
			}
			persistence, err := trustepoch.OpenBolt(filepath.Join(dir, "consumer.bolt"))
			if err != nil {
				t.Fatal(err)
			}
			verifier := trustepoch.Verifier{OperatorRoot: root.Hex(), LegacyPubkey: oldPub.Hex(), Persistence: persistence, Inventory: rehearsalInventory(accepted)}
			resolved, err = verifier.AcceptHead(ctx, cfg.Nostr.ServiceID, heads[0])
			if err != nil {
				t.Fatal(err)
			}
			_ = persistence.Close()
			hostile := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(state.ActivationBarrier + 1), Content: "post-barrier"}
			if err := old.SignEvent(ctx, &hostile); err != nil {
				t.Fatal(err)
			}
			if resolved.JudgeEvent(hostile) != trustepoch.Hostile {
				t.Fatal("post-barrier old key accepted")
			}
			if err := engine.Abort(ctx); !errors.Is(err, ErrForwardOnly) {
				t.Fatalf("post-cut abort: %v", err)
			}
		case ReassertCanonicalState:
			serviceHeads, err := engine.Source.Read(ctx, nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kind)}, Authors: []nostr.PubKey{newPub}, Tags: nostr.TagMap{"d": {tag(currentService, "d")}}})
			if err != nil || len(serviceHeads) != 1 || serviceHeads[0].Content != currentService.Content || serviceHeads[0].Content == snapshotOld.Content {
				t.Fatalf("superseded revision reasserted: heads=%v err=%v", serviceHeads, err)
			}
			raw, readErr := os.ReadFile(runtime.SQLDrafts.ArtifactPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			var sqlReport SQLDraftReport
			if json.Unmarshal(raw, &sqlReport) != nil || sqlReport.ReviewedRows != 0 || len(sqlReport.Drafts) != 0 {
				t.Fatal("absent PostgreSQL projection misreported")
			}
			digest, count, err := InventoryDigest(state.Inventory)
			if err != nil || digest != state.InventoryDigest || count != len(accepted) {
				t.Fatal("canonical inventory digest drift")
			}
			for _, name := range []string{"cp-state/service-registry", "backup-canonical-families", "security-canonical", "notification-channels"} {
				if _, err := engine.Domains[name].Verify(ctx); err != nil {
					t.Fatalf("%s verification: %v", name, err)
				}
			}
			events, err := engine.Source.Read(ctx, nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{newPub}, Tags: nostr.TagMap{"d": {d}}})
			if err != nil || len(events) != 1 {
				t.Fatalf("new notification head: %v %d", err, len(events))
			}
			oldEnv, _ := controlplane.ParseConfidentialEnvelope(notificationOld.Content)
			newEnv, _ := controlplane.ParseConfidentialEnvelope(events[0].Content)
			encrypted := newEnv.ServiceInner
			oldEnv.ServiceInner = ""
			newEnv.ServiceInner = ""
			if !reflect.DeepEqual(oldEnv, newEnv) {
				t.Fatal("OCK-visible notification layer changed")
			}
			recovered, err := newKey.Decrypt(ctx, encrypted, newPub)
			if err != nil || recovered != plain {
				t.Fatal("notification decrypt/re-encrypt failed")
			}
		case DeleteAndPurge:
			if !hasPurgeEvidence(receipt, state.DeletionIDs, cfg.IdentityRecovery.CleanupRelays) {
				t.Fatal("cleanup relay OK or purge evidence absent")
			}
			if len(state.DeletionIDs) != 1 {
				t.Fatal("expected one old-key deletion batch")
			}
			for _, relay := range []string{controlled, cleanup} {
				if state.CleanupDeletions[state.DeletionIDs[0]][relay].Status != "ok" {
					t.Fatalf("%s missing deletion OK", relay)
				}
			}
			found := false
			for ev := range cleanupStore.QueryEvents(nostr.Filter{Kinds: []nostr.Kind{nostr.KindDeletion}, Authors: []nostr.PubKey{oldPub}}, 100) {
				if ev.ID.Hex() == state.DeletionIDs[0] {
					found = true
				}
			}
			if !found {
				t.Fatal("external cleanup relay has no kind-5 deletion")
			}
		}
		if err := runtime.Close(); err != nil {
			t.Fatal(err)
		}
		runtime = open()
		engine = runtime.Engine
		if next, has := state.Next(); has && next == phase {
			t.Fatalf("phase %s did not advance", phase)
		}
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	runtime = open()
	engine = runtime.Engine
	if err := engine.Resume(ctx, Verify); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("restart bypassed pending consumer verification: %v", err)
	}
	if resolved.Record == nil || !resolved.Record.Activated {
		t.Fatal("trust epoch not activated")
	}
	state, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	if next, _ := state.Next(); next != Verify {
		t.Fatalf("expected verified fail-closed boundary, got %s", next)
	}
}
