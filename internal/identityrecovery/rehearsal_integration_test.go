//go:build integration

package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/eventstore/slicestore"
	"fiatjaf.com/nostr/khatru"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/adapters/nostr/localstore"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/nostrout"
	"github.com/openagentsinc/bahia/internal/trustepoch"
	"go.uber.org/zap"
)

type rehearsalRelay struct {
	pool *adapter.RelayPool
	url  string
}

func (r rehearsalRelay) Publish(ctx context.Context, ev nostr.Event) error {
	results, err := r.pool.PublishToRelaysWithResults(ctx, ev, []string{r.url})
	if err != nil {
		return err
	}
	if len(results) != 1 || results[0].RelayURL != r.url || !results[0].Succeeded() {
		return errors.New("relay OK missing")
	}
	return nil
}
func (r rehearsalRelay) Read(ctx context.Context, filter nostr.Filter) ([]nostr.Event, error) {
	sub, err := r.pool.SubscribeWithOptions(ctx, []nostr.Filter{filter}, adapter.SubscribeOptions{Relays: []string{r.url}, AwaitUnavailableRelays: true, ValidateEvent: func(ev *nostr.Event) bool { return ev.CheckID() && ev.VerifySignature() }})
	if err != nil {
		return nil, err
	}
	defer sub.Close()
	out := map[nostr.ID]nostr.Event{}
	for {
		select {
		case ev, ok := <-sub.Events:
			if !ok {
				return nil, errors.New("relay closed before EOSE")
			}
			out[ev.ID] = *ev
		case <-sub.EndOfStoredEvents:
			if err := sub.StoredEventsIncomplete(nil); err != nil || !sub.AllRelaysReachedEOSE() {
				return nil, errors.New("relay catch-up incomplete")
			}
			events := make([]nostr.Event, 0, len(out))
			for _, ev := range out {
				events = append(events, ev)
			}
			return events, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

type rehearsalDomain struct {
	old        nostr.Event
	source     rehearsalRelay
	newPub     nostr.PubKey
	reasserted nostr.ID
}

func (d *rehearsalDomain) Inventory(context.Context) ([]string, error) {
	return []string{d.old.ID.Hex()}, nil
}
func (d *rehearsalDomain) Reassert(ctx context.Context, signer nostr.Keyer) ([]string, error) {
	if d.reasserted != nostr.ZeroID {
		return []string{d.reasserted.Hex()}, nil
	}
	ev := nostr.Event{Kind: d.old.Kind, CreatedAt: nostr.Timestamp(3000), Tags: slices.Clone(d.old.Tags), Content: d.old.Content}
	if err := signer.SignEvent(ctx, &ev); err != nil {
		return nil, err
	}
	if ev.PubKey != d.newPub {
		return nil, errors.New("reassert signer mismatch")
	}
	if err := d.source.Publish(ctx, ev); err != nil {
		return nil, err
	}
	d.reasserted = ev.ID
	return []string{ev.ID.Hex()}, nil
}
func (d *rehearsalDomain) Verify(ctx context.Context) ([]string, error) {
	if d.reasserted == nostr.ZeroID {
		return nil, ErrIncomplete
	}
	events, err := d.source.Read(ctx, nostr.Filter{Kinds: []nostr.Kind{d.old.Kind}, Authors: []nostr.PubKey{d.newPub}})
	if err != nil {
		return nil, err
	}
	for _, ev := range events {
		if ev.ID == d.reasserted {
			return []string{ev.ID.Hex()}, nil
		}
	}
	return nil, ErrIncomplete
}

type rehearsalInventory []string

func (i rehearsalInventory) IDs(context.Context, string, string) ([]string, error) { return i, nil }

func TestStagingIdentityRecoveryRehearsal(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	relay := khatru.NewRelay()
	events := &slicestore.SliceStore{}
	if err := events.Init(); err != nil {
		t.Fatal(err)
	}
	defer events.Close()
	relay.UseEventstore(events, 500)
	server := httptest.NewServer(relay)
	defer server.Close()
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	budget := nostrout.PurposeBudget{RatePerMinute: 100000, Burst: 100000}
	admission := nostrout.New(nostrout.Config{Aggregate: budget, PurposeBudgets: map[nostrout.Purpose]nostrout.PurposeBudget{
		nostrout.PurposePriority: budget, nostrout.PurposeState: budget, nostrout.PurposeGeneral: budget, nostrout.PurposeBulk: budget, nostrout.PurposeSigner: budget}, RelayWire: budget, RelayWirePriority: budget})
	pool := adapter.NewRelayPool([]string{url}, zap.NewNop(), adapter.WithOutboundAdmission(admission))
	defer pool.Close()
	pool.Connect(ctx)
	source := rehearsalRelay{pool: pool, url: url}
	operator, newKey, old := signer(t), signer(t), signer(t)
	oldPub, newPub := pub(t, old), pub(t, newKey)
	dir := t.TempDir()
	daemonPath := filepath.Join(dir, "daemon.bolt")
	ledgerPath := filepath.Join(dir, "recovery.bolt")
	daemon, err := localstore.Open(daemonPath)
	if err != nil {
		t.Fatal(err)
	}
	domains := map[string]DomainRecoveryAdapter{}
	var accepted []string
	for i, name := range []string{"service", "environment", "deployment"} {
		ev := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(1000 + i), Tags: nostr.Tags{{"d", name + ":rehearsal"}, {"t", name + "-registry"}}, Content: `{"record":"rehearsal"}`}
		if err := old.SignEvent(ctx, &ev); err != nil {
			t.Fatal(err)
		}
		if err := source.Publish(ctx, ev); err != nil {
			t.Fatal(err)
		}
		if _, err := daemon.SaveEvent(ev); err != nil {
			t.Fatal(err)
		}
		accepted = append(accepted, ev.ID.Hex())
		domains[name] = &rehearsalDomain{old: ev, source: source, newPub: newPub}
	}
	if err := daemon.Close(); err != nil {
		t.Fatal(err)
	}
	oldRef := domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("a", 64), Version: 1}
	newRef := domain.SecretCredentialRef{Store: "signet", CredentialID: "cred_" + strings.Repeat("b", 64), Version: 2}
	label := "bahia-system/rehearsal/k1"
	updatesPath := filepath.Join(dir, "rotation.json")
	purgePath := filepath.Join(dir, "purge.json")
	purgeRaw, _ := json.Marshal(map[string]string{"archive-artifacts": "archive-digest", "pg-backups": "backup-digest", "wal": "wal-digest", "controlled-re-encryption": "reencryption-digest"})
	if err := os.WriteFile(purgePath, purgeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := Config{ServiceID: "rehearsal-service", OldPubkey: oldPub, ReasonCode: "compromised", ControlledRelays: []string{url}, ConsumerRoster: trustepoch.ConsumerIDs(), OperatorPubkey: pub(t, operator), LocalStorePath: daemonPath, AcceptedEventIDs: accepted}
	open := func() *Ledger {
		l, err := OpenLedger(ledgerPath, daemonPath)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	engine := &Engine{Config: cfg, Ledger: open(), Source: source, Local: rehearsalLocal{path: daemonPath}, Operator: operator, NewSigner: func(context.Context) (nostr.Keyer, error) { return newKey, nil }, OldSigner: func(context.Context) (nostr.Keyer, error) { return old, nil }, Containment: testContain{}, Domains: domains, Purge: FilePurgeReceipts{Path: purgePath}, Acks: testAcks{}, Now: func() time.Time { return time.Unix(2000, 0).UTC() }}
	defer engine.Ledger.Close()
	tracker := &LedgerRotationTracker{Ledger: engine.Ledger, Source: rotationSource{refs: []SecretRef{{Provider: "signet", Label: label, Credential: oldRef}}}, UpdatesPath: updatesPath}
	engine.Rotation = tracker
	var resolved trustepoch.Resolution
	for _, phase := range Phases {
		if phase == RotateExternalSecrets {
			refs, err := tracker.Inventory(ctx)
			if err != nil || len(refs) != 1 {
				t.Fatalf("rotation inventory: %v", err)
			}
			raw, _ := json.Marshal([]RotationUpdate{{RefID: refs[0].ID, NewRef: &newRef, SourceRotationEvidenceID: "source-rotation-digest"}})
			if err := os.WriteFile(updatesPath, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var applyErr error
		if phase == Declare {
			applyErr = engine.Apply(ctx, phase)
		} else {
			applyErr = engine.Resume(ctx, phase)
		}
		if applyErr != nil {
			t.Fatalf("apply/resume %s: %v", phase, applyErr)
		}
		state, err := engine.Status()
		if err != nil {
			t.Fatal(err)
		}
		receipt, ok := state.Receipt(phase)
		if !ok || receipt.Status != "completed" || len(receipt.EvidenceIDs) == 0 {
			t.Fatalf("%s receipt incomplete", phase)
		}
		if phase == CutActivateTrust {
			heads, err := source.Read(ctx, nostr.Filter{Kinds: []nostr.Kind{nostr.Kind(kinds.CASControlState)}, Authors: []nostr.PubKey{cfg.OperatorPubkey}, Tags: nostr.TagMap{"d": {"trust-epoch:" + cfg.ServiceID}}})
			if err != nil || len(heads) != 1 {
				t.Fatalf("trust head: %v %d", err, len(heads))
			}
			persistence, err := trustepoch.OpenBolt(filepath.Join(dir, "consumer.bolt"))
			if err != nil {
				t.Fatal(err)
			}
			defer persistence.Close()
			verifier := trustepoch.Verifier{OperatorRoot: cfg.OperatorPubkey.Hex(), LegacyPubkey: oldPub.Hex(), Persistence: persistence, Inventory: rehearsalInventory(accepted)}
			resolved, err = verifier.AcceptHead(ctx, cfg.ServiceID, heads[0])
			if err != nil {
				t.Fatal(err)
			}
			hostile := nostr.Event{Kind: nostr.Kind(kinds.CASControlState), CreatedAt: nostr.Timestamp(state.ActivationBarrier + 1), Content: "post-barrier"}
			if err := old.SignEvent(ctx, &hostile); err != nil {
				t.Fatal(err)
			}
			if decision := resolved.JudgeEvent(hostile); decision != trustepoch.Hostile {
				t.Fatalf("post-barrier old key: %s", decision)
			}
			if err := engine.Abort(ctx); !errors.Is(err, ErrForwardOnly) {
				t.Fatalf("post-cut abort: %v", err)
			}
		}
		if phase == DeleteAndPurge {
			if !hasPurgeEvidence(receipt, state.DeletionIDs, cfg.ControlledRelays) {
				t.Fatal("deletion or purge evidence incomplete")
			}
		}
		if phase == ReassertCanonicalState {
			digest, count, err := InventoryDigest(state.Inventory)
			if err != nil || digest != state.InventoryDigest || count != len(accepted) {
				t.Fatal("inventory digest drift after reassert")
			}
			for name, domain := range domains {
				if _, err := domain.Verify(ctx); err != nil {
					t.Fatalf("%s not reasserted: %v", name, err)
				}
			}
		}
		if err := engine.Ledger.Close(); err != nil {
			t.Fatal(err)
		}
		engine.Ledger = open()
		tracker.Ledger = engine.Ledger
		if next, has := state.Next(); has && next == phase {
			t.Fatalf("phase %s not advanced", phase)
		}
	}
	if resolved.Record == nil || !resolved.Record.Activated {
		t.Fatal("trust epoch not activated")
	}
	state, err := engine.Status()
	if err != nil {
		t.Fatal(err)
	}
	if err := state.ValidateReceipts(cfg.OperatorPubkey); err != nil {
		t.Fatal(err)
	}
}

type rehearsalLocal struct{ path string }

func (l rehearsalLocal) Events(ctx context.Context, pub nostr.PubKey) ([]nostr.Event, error) {
	store, err := localstore.Open(l.path)
	if err != nil {
		return nil, err
	}
	defer store.Close()
	var out []nostr.Event
	for ev := range store.QueryEvents(nostr.Filter{Authors: []nostr.PubKey{pub}}) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}
