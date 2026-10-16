package identityrecovery

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	adapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

type registryTestRelay struct {
	events    []nostr.Event
	publishes int
	readErr   error
}

func (s *registryTestRelay) Read(_ context.Context, f nostr.Filter) ([]nostr.Event, error) {
	if s.readErr != nil {
		return nil, s.readErr
	}
	var out []nostr.Event
	for _, ev := range s.events {
		if f.Matches(ev) {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *registryTestRelay) Publish(_ context.Context, ev nostr.Event) error {
	s.publishes++
	s.events = append(s.events, ev)
	return nil
}

type registryTestLocal struct {
	events []nostr.Event
	writes int
}

func (s *registryTestLocal) Events(_ context.Context, pub nostr.PubKey) ([]nostr.Event, error) {
	var out []nostr.Event
	for _, ev := range s.events {
		if ev.PubKey == pub {
			out = append(out, ev)
		}
	}
	return out, nil
}

func (s *registryTestLocal) StoreEvent(_ context.Context, ev nostr.Event) error {
	s.writes++
	s.events = append(s.events, ev)
	return nil
}

func signedRegistryHead(t *testing.T, key nostr.Keyer, kind int, tags nostr.Tags, content string, at int64) nostr.Event {
	t.Helper()
	ev := nostr.Event{Kind: nostr.Kind(kind), CreatedAt: nostr.Timestamp(at), Tags: tags, Content: content}
	if err := key.SignEvent(context.Background(), &ev); err != nil {
		t.Fatal(err)
	}
	return ev
}

func registryFixture(t *testing.T, family int, oldHeads []nostr.Event) (*RegistryRecoveryAdapter, *registryTestRelay, *registryTestLocal, nostr.Keyer) {
	t.Helper()
	newKey := signer(t)
	oldPub := oldHeads[0].PubKey
	local := &registryTestLocal{events: slices.Clone(oldHeads)}
	relay := &registryTestRelay{events: slices.Clone(oldHeads)}
	ledger, err := OpenLedger(filepath.Join(t.TempDir(), "recovery.bolt"), filepath.Join(t.TempDir(), "daemon.bolt"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ledger.Close() })
	items := make([]InventoryEvent, len(oldHeads))
	for i, ev := range oldHeads {
		items[i] = InventoryEvent{ID: ev.ID.Hex(), Kind: int(ev.Kind), CreatedAt: int64(ev.CreatedAt)}
	}
	digest, _, err := InventoryDigest(items)
	if err != nil {
		t.Fatal(err)
	}
	if err := ledger.Save(State{OldPubkey: oldPub.Hex(), NewPubkey: pub(t, newKey).Hex(), Inventory: items, InventoryDigest: digest, ActivationBarrier: 200}); err != nil {
		t.Fatal(err)
	}
	return &RegistryRecoveryAdapter{Family: family, OldPubkey: oldPub, Source: relay, Local: local, Ledger: ledger}, relay, local, newKey
}

func TestRegistryRecoveryRoundTripAndIdempotence(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0).UTC()
	for _, tc := range []struct {
		name   string
		family int
		build  func(*testing.T) (int, nostr.Tags, string)
	}{
		{"service", adapter.KindServiceRegistry, func(t *testing.T) (int, nostr.Tags, string) {
			svc := &domain.Service{ID: uuid.New(), OrgID: uuid.New(), Name: "api", ArtifactRepo: "registry.example/api", RuntimeType: domain.RuntimeTypeDocker, CreatedAt: t0, UpdatedAt: t0}
			return adapter.ServiceRegistryRecoveryRecord(svc, false)
		}},
		{"service-tombstone", adapter.KindServiceRegistry, func(t *testing.T) (int, nostr.Tags, string) {
			svc := &domain.Service{ID: uuid.New(), UpdatedAt: t0}
			return adapter.ServiceRegistryRecoveryRecord(svc, true)
		}},
		{"environment-with-units", adapter.KindEnvironmentRegistry, func(t *testing.T) (int, nostr.Tags, string) {
			env := &domain.Environment{ID: uuid.New(), Name: "prod", Targeting: domain.EnvironmentTargeting{DefaultUnitKey: "web"}, CreatedAt: t0, UpdatedAt: t0}
			units := []domain.DeploymentUnit{{ID: uuid.New(), Key: "web", RuntimeType: domain.RuntimeTypeDocker, ReconcileMode: domain.ReconcileModeObserveOnly, OwnershipMode: domain.OwnershipModeBahiaManaged, CreatedAt: t0, UpdatedAt: t0}}
			return adapter.EnvironmentRegistryRecoveryRecord(env, units, false)
		}},
		{"environment-implicit-unit", adapter.KindEnvironmentRegistry, func(t *testing.T) (int, nostr.Tags, string) {
			env := &domain.Environment{ID: uuid.New(), Name: "dev", CreatedAt: t0, UpdatedAt: t0}
			return adapter.EnvironmentRegistryRecoveryRecord(env, nil, false)
		}},
		{"environment-tombstone", adapter.KindEnvironmentRegistry, func(t *testing.T) (int, nostr.Tags, string) {
			env := &domain.Environment{ID: uuid.New(), UpdatedAt: t0}
			return adapter.EnvironmentRegistryRecoveryRecord(env, nil, true)
		}},
		{"adoption-binding", adapter.KindAdoptionBindingRecord, func(t *testing.T) (int, nostr.Tags, string) {
			binding := &domain.AdoptionBinding{OrgID: uuid.New(), ServiceID: uuid.New(), EnvironmentID: uuid.New(), Fingerprints: map[string]string{"container": "sha256:fixture"}, Status: domain.AdoptionBindingComplete, UpdatedAt: t0}
			kind, tags, content, err := adapter.AdoptionBindingRecoveryRecord(binding)
			if err != nil {
				t.Fatal(err)
			}
			return kind, tags, content
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			oldKey := signer(t)
			kind, tags, content := tc.build(t)
			old := signedRegistryHead(t, oldKey, kind, tags, content, 100)
			a, relay, local, newKey := registryFixture(t, tc.family, []nostr.Event{old})
			ctx := context.Background()
			ids, err := a.Inventory(ctx)
			if err != nil || len(ids) != 1 || ids[0] != old.ID.Hex() {
				t.Fatalf("inventory = %v, %v", ids, err)
			}
			if _, err := a.Reassert(ctx, newKey); err != nil {
				t.Fatal(err)
			}
			if relay.publishes != 1 || local.writes != 1 {
				t.Fatalf("first run publishes=%d local writes=%d", relay.publishes, local.writes)
			}
			proof, err := a.Verify(ctx)
			if err != nil || len(proof) != 2 || proof[0] == "" {
				t.Fatalf("verify = %v, %v", proof, err)
			}
			if _, err := a.Reassert(ctx, newKey); err != nil {
				t.Fatal(err)
			}
			if relay.publishes != 1 || local.writes != 1 {
				t.Fatalf("second run changed canonical state: publishes=%d writes=%d", relay.publishes, local.writes)
			}
		})
	}
}

func TestRegistryRecoveryRejectsDivergentNewHead(t *testing.T) {
	oldKey := signer(t)
	svc := &domain.Service{ID: uuid.New(), Name: "api"}
	kind, tags, content := adapter.ServiceRegistryRecoveryRecord(svc, false)
	old := signedRegistryHead(t, oldKey, kind, tags, content, 100)
	a, relay, local, newKey := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	if _, err := a.Reassert(context.Background(), newKey); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(content), &body); err != nil {
		t.Fatal(err)
	}
	body["name"] = "hostile-change"
	changed, _ := json.Marshal(body)
	divergent := signedRegistryHead(t, newKey, kind, tags, string(changed), time.Now().Unix()+100)
	relay.events = append(relay.events, divergent)
	local.events = append(local.events, divergent)
	if _, err := a.Verify(context.Background()); !errors.Is(err, ErrIncomplete) {
		t.Fatalf("divergent head verify error = %v", err)
	}
	if _, err := a.Reassert(context.Background(), newKey); err == nil {
		t.Fatal("reassert overwrote divergent new-key state")
	}
	if relay.publishes != 1 {
		t.Fatalf("divergent head caused publish: %d", relay.publishes)
	}
}

func TestRegistryRecoveryPreflightAndEOSEFailureDoNotPublish(t *testing.T) {
	oldKey := signer(t)
	svc := &domain.Service{ID: uuid.New(), Name: "api"}
	kind, tags, content := adapter.ServiceRegistryRecoveryRecord(svc, false)
	tags = append(slices.Clone(tags), nostr.Tag{"extra", "unreproducible"})
	old := signedRegistryHead(t, oldKey, kind, tags, content, 100)
	a, relay, _, newKey := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	if _, err := a.Reassert(context.Background(), newKey); err == nil || relay.publishes != 0 {
		t.Fatalf("unreproducible head reassert error=%v publishes=%d", err, relay.publishes)
	}
	relay.readErr = errors.New("EOSE incomplete")
	if _, err := a.Inventory(context.Background()); err == nil || relay.publishes != 0 {
		t.Fatalf("incomplete relay history inventory error=%v publishes=%d", err, relay.publishes)
	}
}

func TestRegistryRecoveryAcceptedInventoryOnly(t *testing.T) {
	oldKey := signer(t)
	svc := &domain.Service{ID: uuid.New(), Name: "accepted"}
	kind, tags, content := adapter.ServiceRegistryRecoveryRecord(svc, false)
	old := signedRegistryHead(t, oldKey, kind, tags, content, 100)
	a, relay, _, _ := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	badTags := slices.Clone(tags)
	newerUnaccepted := signedRegistryHead(t, oldKey, kind, badTags, `{"id":"`+svc.ID.String()+`","name":"unaccepted"}`, 101)
	relay.events = append(relay.events, newerUnaccepted)
	ids, err := a.Inventory(context.Background())
	if err != nil || len(ids) != 1 || ids[0] != old.ID.Hex() {
		t.Fatalf("unaccepted event changed inventory: %v, %v", ids, err)
	}
	if tag(old, "t") != kinds.CPStateTopicServiceRegistry {
		t.Fatal("fixture was not a service registry event")
	}
}

func TestRecoveryPhaseCannotCompleteWithUnregisteredDomainGroups(t *testing.T) {
	e := &Engine{Domains: map[string]DomainRecoveryAdapter{"cp-state/service-registry": testDomain{}}, RequiredDomains: []string{"cp-state/service-registry", "relay-settings-and-sets"}}
	if _, err := e.reassert(context.Background()); !errors.Is(err, ErrAdapterNotRegistered) {
		t.Fatalf("partial reassert error = %v", err)
	}
	if _, err := e.verify(context.Background(), State{}); !errors.Is(err, ErrAdapterNotRegistered) {
		t.Fatalf("partial verify error = %v", err)
	}
}

func TestRegistryRecoveryRepairsOneMissingCanonicalSurfaceWithoutResigning(t *testing.T) {
	oldKey := signer(t)
	svc := &domain.Service{ID: uuid.New(), Name: "api"}
	kind, tags, content := adapter.ServiceRegistryRecoveryRecord(svc, false)
	old := signedRegistryHead(t, oldKey, kind, tags, content, 100)
	a, relay, local, newKey := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	ctx := context.Background()
	if _, err := a.Reassert(ctx, newKey); err != nil {
		t.Fatal(err)
	}
	newHead := relay.events[len(relay.events)-1]
	relay.events = relay.events[:len(relay.events)-1]
	if _, err := a.Reassert(ctx, newKey); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 2 || relay.events[len(relay.events)-1].ID != newHead.ID || local.writes != 1 {
		t.Fatal("missing relay copy was not replayed byte-identically")
	}
	local.events = local.events[:len(local.events)-1]
	if _, err := a.Reassert(ctx, newKey); err != nil {
		t.Fatal(err)
	}
	if relay.publishes != 2 || local.writes != 2 || local.events[len(local.events)-1].ID != newHead.ID {
		t.Fatal("missing local copy was not restored without a new signature")
	}
}

func TestRegistryRecoveryMissingAcceptedEventFailsClosed(t *testing.T) {
	oldKey := signer(t)
	svc := &domain.Service{ID: uuid.New(), Name: "api"}
	kind, tags, content := adapter.ServiceRegistryRecoveryRecord(svc, false)
	old := signedRegistryHead(t, oldKey, kind, tags, content, 100)
	a, relay, local, newKey := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	relay.events = nil
	local.events = nil
	if _, err := a.Reassert(context.Background(), newKey); err == nil || relay.publishes != 0 {
		t.Fatalf("missing accepted event error=%v publishes=%d", err, relay.publishes)
	}
}

func TestRegistryRecoveryRejectsAcceptedOldEventAfterBarrier(t *testing.T) {
	oldKey := signer(t)
	svc := &domain.Service{ID: uuid.New(), Name: "api"}
	kind, tags, content := adapter.ServiceRegistryRecoveryRecord(svc, false)
	old := signedRegistryHead(t, oldKey, kind, tags, content, 201)
	a, relay, _, newKey := registryFixture(t, adapter.KindServiceRegistry, []nostr.Event{old})
	if _, err := a.Reassert(context.Background(), newKey); err == nil || relay.publishes != 0 {
		t.Fatalf("post-barrier accepted event error=%v publishes=%d", err, relay.publishes)
	}
}
