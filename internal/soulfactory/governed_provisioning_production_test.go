package soulfactory

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/soulfactory/saga"
)

// sampleProductionState builds a fully-populated durable state for the given
// request so the round-trip covers the secret-free ledger fields.
func sampleProductionState(requestID string) *productionProvisioningState {
	return &productionProvisioningState{
		RequestID: requestID,
		RunID:     uuid.NewSHA1(uuid.NameSpaceOID, []byte("run/"+requestID)).String(),
		AgentID:   "scout",
		SpecHash:  "spec-hash-scout",
		Runtime:   domain.RuntimeTargetOpenClaw,
		Soul: domain.AgentSoul{
			ID:        uuid.NewSHA1(uuid.NameSpaceOID, []byte("soul/"+requestID)),
			AgentID:   "scout",
			Name:      "Scout",
			Status:    domain.SoulStatusProvisioning,
			BunkerURI: "bunker://secret-should-not-persist",
		},
		Prepared:  true,
		ServiceID: uuid.New(),
		Steps: map[OrderedStep]productionStepState{
			StepReserveIdentity: {Complete: true, Resources: []ObservedResource{{
				Kind: "identity_reservation", ExternalID: "identity:scout",
				Ownership: saga.OwnershipAdopted, SpecHash: "spec-hash-scout", CorrelationID: requestID,
			}}},
		},
	}
}

// TestProductionStateStoreRestartDurableRoundTrip proves the durable adapter
// ledger survives a process restart (a fresh store over the same directory
// reads back the saved state) and that secret-bearing fields are never
// persisted.
func TestProductionStateStoreRestartDurableRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := newProductionStateStore(dir)
	if err != nil {
		t.Fatalf("newProductionStateStore() error = %v", err)
	}
	want := sampleProductionState("req-durable-1")
	if err := store.save(ctx, want); err != nil {
		t.Fatalf("save() error = %v", err)
	}

	// Simulate a process restart: a brand-new store instance over the same dir.
	restarted, err := newProductionStateStore(dir)
	if err != nil {
		t.Fatalf("newProductionStateStore(restart) error = %v", err)
	}
	got, err := restarted.load(ctx, "req-durable-1")
	if err != nil {
		t.Fatalf("load(after restart) error = %v", err)
	}
	if got.RunID != want.RunID || got.AgentID != want.AgentID || got.SpecHash != want.SpecHash || got.Runtime != want.Runtime {
		t.Fatalf("durable state changed across restart: got %+v", got)
	}
	if got.ServiceID != want.ServiceID || !got.Prepared {
		t.Fatalf("durable service/prepared state lost: got service=%s prepared=%v", got.ServiceID, got.Prepared)
	}
	if step, ok := got.Steps[StepReserveIdentity]; !ok || !step.Complete || len(step.Resources) != 1 {
		t.Fatalf("durable step ledger lost: %+v", got.Steps)
	}
	if got.Soul.BunkerURI != "" {
		t.Fatalf("secret bunker URI must never be persisted, got %q", got.Soul.BunkerURI)
	}
}

// TestProductionStateStoreLoadMissingIsNotFound ensures a missing request maps
// to the sentinel the Provision replay path branches on.
func TestProductionStateStoreLoadMissingIsNotFound(t *testing.T) {
	store, err := newProductionStateStore(t.TempDir())
	if err != nil {
		t.Fatalf("newProductionStateStore() error = %v", err)
	}
	if _, err := store.load(context.Background(), "absent"); !errors.Is(err, errProductionStateNotFound) {
		t.Fatalf("load(absent) error = %v, want errProductionStateNotFound", err)
	}
}

// TestProductionStateStoreReservationIsReplayIdempotent proves identity
// reservation is durable and idempotent: the first create wins, a replay
// observes the same reservation (created=false) without allocating a new
// identity, and this holds across a simulated restart.
func TestProductionStateStoreReservationIsReplayIdempotent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := newProductionStateStore(dir)
	if err != nil {
		t.Fatalf("newProductionStateStore() error = %v", err)
	}
	spec := ProvisioningSpec{RequestID: "req-a", RunID: "run-a", AgentID: "scout", SpecHash: "hash-a", Runtime: domain.RuntimeTargetOpenClaw}

	// A create=false probe before any reservation must not allocate one.
	if r, created, err := store.reservation(ctx, spec, false); err != nil || created || r != nil {
		t.Fatalf("reservation(create=false, none) = (%v,%v,%v), want (nil,false,nil)", r, created, err)
	}
	first, created, err := store.reservation(ctx, spec, true)
	if err != nil || !created || first == nil {
		t.Fatalf("reservation(create) = (%v,%v,%v), want created", first, created, err)
	}
	replay, created, err := store.reservation(ctx, spec, true)
	if err != nil || created {
		t.Fatalf("reservation(replay) created=%v err=%v, want reuse", created, err)
	}
	if replay.RunID != first.RunID || replay.RequestID != first.RequestID || replay.SpecHash != first.SpecHash {
		t.Fatalf("replay reservation differs from first: %+v vs %+v", replay, first)
	}

	// Restart durability: a fresh store over the same dir still observes it.
	restarted, err := newProductionStateStore(dir)
	if err != nil {
		t.Fatalf("newProductionStateStore(restart) error = %v", err)
	}
	afterRestart, created, err := restarted.reservation(ctx, spec, true)
	if err != nil || created {
		t.Fatalf("reservation(after restart) created=%v err=%v, want durable reuse", created, err)
	}
	if afterRestart.RunID != first.RunID {
		t.Fatalf("reservation RunID changed across restart: %s vs %s", afterRestart.RunID, first.RunID)
	}
}
