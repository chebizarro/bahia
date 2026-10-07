package service_test

import (
	"context"
	"encoding/json"
	"sort"
	"sync"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/service"
)

// latestPerCoordinateRelay keeps what a NIP-01 relay serves for addressable
// kinds: one event per (kind, pubkey, d), the newest created_at, ties to the
// lowest id.
type latestPerCoordinateRelay struct {
	mu     sync.Mutex
	latest map[string]gonostr.Event
}

func newLatestPerCoordinateRelay() *latestPerCoordinateRelay {
	return &latestPerCoordinateRelay{latest: map[string]gonostr.Event{}}
}

func relayCoordinate(kind gonostr.Kind, pubkey gonostr.PubKey, d string) string {
	encoded, _ := json.Marshal([]any{int(kind), pubkey.Hex(), d})
	return string(encoded)
}

func (r *latestPerCoordinateRelay) Publish(_ context.Context, ev gonostr.Event) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	key := relayCoordinate(ev.Kind, ev.PubKey, ev.Tags.GetD())
	current, ok := r.latest[key]
	if !ok || ev.CreatedAt > current.CreatedAt || (ev.CreatedAt == current.CreatedAt && ev.ID.Hex() < current.ID.Hex()) {
		r.latest[key] = ev
	}
	return 1, nil
}

func (r *latestPerCoordinateRelay) get(t *testing.T, author gonostr.PubKey, d string) gonostr.Event {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	ev, ok := r.latest[relayCoordinate(gonostr.Kind(kinds.CASControlState), author, d)]
	if !ok {
		t.Fatalf("relay serves nothing on d=%q", d)
	}
	return ev
}

func (r *latestPerCoordinateRelay) retained() []gonostr.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]gonostr.Event, 0, len(r.latest))
	for _, ev := range r.latest {
		out = append(out, ev)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Tags.GetD() < out[j].Tags.GetD() })
	return out
}

// projectorWorkerRecord is a worker read model in the projector's own envelope
// (nostr.ControlStateEnvelope), signed by the same service author as the
// control plane's worker publishers.
func projectorWorkerRecord(t *testing.T, author gonostr.SecretKey, legacyKind int, id string, deleted bool, createdAt gonostr.Timestamp, workerPubkey string, content any) gonostr.Event {
	t.Helper()
	wireKind, tags := nostr.ControlStateEnvelope(legacyKind, id, deleted)
	encoded, err := json.Marshal(content)
	if err != nil {
		t.Fatalf("marshal content: %v", err)
	}
	ev := gonostr.Event{Kind: gonostr.Kind(wireKind), CreatedAt: createdAt, Tags: append(tags, gonostr.Tag{"worker", workerPubkey}), Content: string(encoded)}
	if err := ev.Sign(author); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return ev
}

// TestWorkerCPStateFamiliesCoexistOnRelay pins: every worker
// family one service author publishes for the same worker (state and cleanup
// from the control plane; assignment, drain and eligibility from the
// projector) lives on its own coordinate, so a relay that keeps the latest
// event per (kind, pubkey, d) serves all of them, a republish or tombstone of
// one family leaves the others in place, and every retained record replays
// into the daemon's projection cache.
func TestWorkerCPStateFamiliesCoexistOnRelay(t *testing.T) {
	ctx := context.Background()
	relay := newLatestPerCoordinateRelay()
	author := gonostr.Generate()
	signer := keyer.NewPlainKeySigner(author)
	authorPub := author.Public()
	workerPubkey := gonostr.Generate().Public().Hex()
	base := gonostr.Timestamp(time.Now().Add(-time.Hour).Unix())

	worker := domain.Worker{PubKey: workerPubkey, Name: "coexist-worker", Status: domain.WorkerStatusOnline, SchedulingState: domain.WorkerSchedulingActive, LastAdvertisementAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := controlplane.NewWorkerStatePublisher(relay, signer).Publish(ctx, &worker); err != nil {
		t.Fatalf("publish worker state: %v", err)
	}
	if err := controlplane.NewWorkerCleanupStatePublisher(relay, signer).Publish(ctx, events.WorkerCleanupEvent{WorkerPubKey: workerPubkey, CleanupMode: "safe", LoomJobID: "job-1", Status: "completed", StartedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("publish worker cleanup: %v", err)
	}
	assignmentState := domain.WorkerAssignmentState{WorkerPubKey: workerPubkey, ActiveAssignments: []domain.WorkerAssignment{{Type: domain.WorkerAssignmentService, WorkloadID: "svc-1", Status: "assigned"}}}
	drainStatus := domain.WorkerDrainStatus{WorkerPubKey: workerPubkey, SchedulingState: domain.WorkerSchedulingDraining}
	for _, ev := range []gonostr.Event{
		projectorWorkerRecord(t, author, nostr.KindWorkerAssignmentState, workerPubkey, false, base, workerPubkey, assignmentState),
		projectorWorkerRecord(t, author, nostr.KindWorkerDrainStatus, workerPubkey, false, base+1, workerPubkey, drainStatus),
		projectorWorkerRecord(t, author, nostr.KindWorkerEligibilityPreview, "preview-1", false, base+2, workerPubkey, domain.WorkerEligibilityPreview{PreviewID: "preview-1"}),
	} {
		if _, err := relay.Publish(ctx, ev); err != nil {
			t.Fatalf("publish: %v", err)
		}
	}

	coordinates := map[string]kinds.CPStateFamily{
		kinds.WorkerStateDPrefix + workerPubkey:              kinds.CPStateFamilyWorkerState,
		kinds.WorkerAssignmentDPrefix + workerPubkey:         kinds.CPStateFamilyWorkerAssignment,
		kinds.WorkerDrainDPrefix + workerPubkey:              kinds.CPStateFamilyWorkerDrain,
		kinds.WorkerEligibilityDPrefix + "preview-1":         kinds.CPStateFamilyWorkerEligibility,
		kinds.WorkerCleanupDPrefix + workerPubkey + ":job-1": kinds.CPStateFamilyWorkerCleanup,
	}
	if got := len(relay.retained()); got != len(coordinates) {
		t.Fatalf("relay retains %d worker records, want %d (one per family)", got, len(coordinates))
	}
	for d, family := range coordinates {
		ev := relay.get(t, authorPub, d)
		if legacy := ev.Tags.Find(kinds.CASControlStateTagLegacyKind); legacy == nil || legacy[1] != family.TagValue() {
			t.Fatalf("d=%q serves legacy_kind %v, want %s", d, legacy, family.TagValue())
		}
	}

	// A newer drain replaces only the drain coordinate.
	firstAssignment := relay.get(t, authorPub, kinds.WorkerAssignmentDPrefix+workerPubkey)
	drainStatus.SchedulingState = domain.WorkerSchedulingActive
	if _, err := relay.Publish(ctx, projectorWorkerRecord(t, author, nostr.KindWorkerDrainStatus, workerPubkey, false, base+10, workerPubkey, drainStatus)); err != nil {
		t.Fatalf("republish drain: %v", err)
	}
	if got := relay.get(t, authorPub, kinds.WorkerAssignmentDPrefix+workerPubkey); got.ID != firstAssignment.ID {
		t.Fatalf("drain republish replaced the assignment record")
	}
	if got := relay.get(t, authorPub, kinds.WorkerDrainDPrefix+workerPubkey); got.CreatedAt != base+10 {
		t.Fatalf("drain coordinate serves created_at %d, want the republish", got.CreatedAt)
	}

	// An assignment tombstone lands on the assignment coordinate only.
	if _, err := relay.Publish(ctx, projectorWorkerRecord(t, author, nostr.KindWorkerAssignmentState, workerPubkey, true, base+20, workerPubkey, map[string]any{"deleted": true, "worker_pubkey": workerPubkey})); err != nil {
		t.Fatalf("publish assignment tombstone: %v", err)
	}
	if got := relay.get(t, authorPub, kinds.WorkerAssignmentDPrefix+workerPubkey); got.Tags.Find(kinds.CASControlStateTagDeleted)[1] != "true" {
		t.Fatalf("assignment coordinate does not serve the tombstone: %v", got.Tags)
	}
	for _, d := range []string{kinds.WorkerDrainDPrefix + workerPubkey, kinds.WorkerStateDPrefix + workerPubkey} {
		if got := relay.get(t, authorPub, d); got.Tags.Find(kinds.CASControlStateTagDeleted)[1] != "false" {
			t.Fatalf("assignment tombstone reached d=%q", d)
		}
	}

	// Every retained record replays into the projection cache on its own key.
	meta := newRelayProjectionMetaMemoryRepo()
	repo := newStandbyProjectionWorkerRepo()
	cache := service.NewRelayProjectionCache(meta, zap.NewNop())
	cache.RegisterProjectionAppliers(service.ProjectionCacheRepositories{Workers: repo})
	for _, ev := range relay.retained() {
		requireApply(t, cache, decodeWorkerCPState(t, ev))
	}
	assertStoredMeta(t, meta, string(nostr.FamilyWorker), kinds.WorkerStateDPrefix+workerPubkey, relay.get(t, authorPub, kinds.WorkerStateDPrefix+workerPubkey).ID.Hex(), false)
	assertStoredMeta(t, meta, string(nostr.FamilyWorkerAssignment), kinds.WorkerAssignmentDPrefix+workerPubkey, relay.get(t, authorPub, kinds.WorkerAssignmentDPrefix+workerPubkey).ID.Hex(), true)
	assertStoredMeta(t, meta, string(nostr.FamilyWorkerDrain), kinds.WorkerDrainDPrefix+workerPubkey, relay.get(t, authorPub, kinds.WorkerDrainDPrefix+workerPubkey).ID.Hex(), false)
	if got := repo.workers[workerPubkey]; got == nil || got.Name != "coexist-worker" {
		t.Fatalf("replayed worker = %+v", got)
	}
}

// TestWorkerCPStateLegacySharedCoordinateIsIgnored pins the decision for
// records published before: assignment and drain on the bare
// worker pubkey shared one coordinate, so a relay kept only whichever came
// last. That survivor is not decoded; the projector republishes both families
// on their own coordinates at startup.
func TestWorkerCPStateLegacySharedCoordinateIsIgnored(t *testing.T) {
	author := gonostr.Generate()
	workerPubkey := gonostr.Generate().Public().Hex()
	relay := newLatestPerCoordinateRelay()
	legacy := func(family kinds.CPStateFamily, createdAt gonostr.Timestamp, content any) gonostr.Event {
		encoded, _ := json.Marshal(content)
		ev := gonostr.Event{Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: createdAt, Tags: gonostr.Tags{
			{"d", workerPubkey}, {"domain", kinds.WorkerDomain}, {"schema", kinds.CASControlStateSchema},
			{"legacy_kind", family.TagValue()}, {"deleted", "false"}, {"worker", workerPubkey},
		}, Content: string(encoded)}
		if err := ev.Sign(author); err != nil {
			t.Fatalf("sign: %v", err)
		}
		return ev
	}
	assignment := legacy(kinds.CPStateFamilyWorkerAssignment, 1_800_000_000, domain.WorkerAssignmentState{WorkerPubKey: workerPubkey})
	drain := legacy(kinds.CPStateFamilyWorkerDrain, 1_800_000_001, domain.WorkerDrainStatus{WorkerPubKey: workerPubkey})
	_, _ = relay.Publish(context.Background(), assignment)
	_, _ = relay.Publish(context.Background(), drain)
	if got := relay.retained(); len(got) != 1 || got[0].ID != drain.ID {
		t.Fatalf("shared coordinate retained %d records; the legacy collision kept only the newest", len(got))
	}

	decode, ok := nostr.NewKindCatalog().Decoder(kinds.CASControlState)
	if !ok {
		t.Fatal("no catalog decoder for 30900")
	}
	for name, ev := range map[string]gonostr.Event{"assignment": assignment, "drain": drain} {
		decoded, err := decode(&ev)
		if err != nil || decoded != nil {
			t.Fatalf("legacy shared-coordinate %s decoded as %+v, %v; want skipped", name, decoded, err)
		}
	}
}
