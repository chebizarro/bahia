package service_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	gonostr "fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
)

// These tests drive worker cp-state records the real producers emit through
// the daemon's relay consumer chain: catalog decode by wire kind (as the
// bootstrapper does) -> RelayProjectionCache -> worker repository.

type workerStateCapture struct{ events []gonostr.Event }

func (c *workerStateCapture) Publish(_ context.Context, ev gonostr.Event) (int, error) {
	c.events = append(c.events, ev)
	return 1, nil
}

func publishedWorkerState(t *testing.T, publisher *controlplane.WorkerStatePublisher, capture *workerStateCapture, worker domain.Worker) gonostr.Event {
	t.Helper()
	if err := publisher.Publish(context.Background(), &worker); err != nil {
		t.Fatalf("publish worker state: %v", err)
	}
	return capture.events[len(capture.events)-1]
}

// tombstoneOf is the worker-state tombstone for a live record: same kind, d
// and t topic with deleted=true (the envelope contract controlplane's
// TestWorkerCPStateTombstonesShareLiveCoordinateAndTopic pins).
func tombstoneOf(t *testing.T, live gonostr.Event, author gonostr.SecretKey) gonostr.Event {
	t.Helper()
	tags := make(gonostr.Tags, 0, len(live.Tags))
	for _, tag := range live.Tags {
		if tag[0] == kinds.CASControlStateTagDeleted {
			tag = gonostr.Tag{kinds.CASControlStateTagDeleted, "true"}
		}
		tags = append(tags, tag)
	}
	var content map[string]any
	if err := json.Unmarshal([]byte(live.Content), &content); err != nil {
		t.Fatalf("live content: %v", err)
	}
	content["deleted"] = true
	encoded, _ := json.Marshal(content)
	dead := gonostr.Event{Kind: live.Kind, CreatedAt: live.CreatedAt + 1, Tags: tags, Content: string(encoded)}
	if err := dead.Sign(author); err != nil {
		t.Fatalf("sign tombstone: %v", err)
	}
	return dead
}

func decodeWorkerCPState(t *testing.T, ev gonostr.Event) *nostr.DecodedProjectionEvent {
	t.Helper()
	decode, ok := nostr.NewKindCatalog().Decoder(int(ev.Kind))
	if !ok {
		t.Fatalf("no catalog decoder for wire kind %d", ev.Kind)
	}
	decoded, err := decode(&ev)
	if err != nil {
		t.Fatalf("decode kind %d legacy_kind %s: %v", ev.Kind, ev.Tags.GetD(), err)
	}
	return decoded
}

func workerCPStateFixture(t *testing.T) (*controlplane.WorkerStatePublisher, *workerStateCapture, gonostr.SecretKey, domain.Worker) {
	t.Helper()
	author := gonostr.Generate()
	capture := &workerStateCapture{}
	publisher := controlplane.NewWorkerStatePublisher(capture, keyer.NewPlainKeySigner(author))
	advertised := time.Date(2026, 9, 30, 12, 0, 0, 5000, time.UTC)
	worker := domain.Worker{
		PubKey:              gonostr.Generate().Public().Hex(),
		Name:                "mesh-worker",
		Software:            []domain.WorkerSoftware{{Name: "docker", Version: "27.0"}},
		Status:              domain.WorkerStatusOnline,
		SchedulingState:     domain.WorkerSchedulingActive,
		FIPSOverlayAddr:     "fd00::42",
		FIPSEndpoints:       []domain.FIPSTransportEndpoint{{Transport: "udp", Address: "203.0.113.7:4242"}},
		MeshHealth:          &domain.MeshHealth{RTT: 40 * time.Millisecond, Loss: 0.01},
		LastAdvertisementAt: advertised,
		UpdatedAt:           advertised,
	}
	return publisher, capture, author, worker
}

func TestWorkerCPStateLiveRecordReplaysLosslesslyIntoWorkerRepository(t *testing.T) {
	publisher, capture, _, worker := workerCPStateFixture(t)
	live := publishedWorkerState(t, publisher, capture, worker)

	decoded := decodeWorkerCPState(t, live)
	if decoded.Family != nostr.FamilyWorker || decoded.Tombstone || decoded.Worker == nil || decoded.Worker.Worker == nil {
		t.Fatalf("live worker state decoded as %+v", decoded)
	}
	repo := newStandbyProjectionWorkerRepo()
	cache := service.NewRelayProjectionCache(newRelayProjectionMetaMemoryRepo(), zap.NewNop())
	cache.RegisterProjectionAppliers(service.ProjectionCacheRepositories{Workers: repo})
	requireApply(t, cache, decoded)

	got := repo.workers[worker.PubKey]
	if got == nil || got.Name != "mesh-worker" || got.FIPSOverlayAddr != "fd00::42" || len(got.FIPSEndpoints) != 1 ||
		got.MeshHealth == nil || got.MeshHealth.RTT != 40*time.Millisecond || len(got.Software) != 1 ||
		!got.LastAdvertisementAt.Equal(worker.LastAdvertisementAt) {
		t.Fatalf("replayed worker = %+v", got)
	}
}

func TestWorkerCPStateTombstoneMarksWorkerOffline(t *testing.T) {
	publisher, capture, author, worker := workerCPStateFixture(t)
	live := publishedWorkerState(t, publisher, capture, worker)
	dead := tombstoneOf(t, live, author)

	repo := newStandbyProjectionWorkerRepo()
	cache := service.NewRelayProjectionCache(newRelayProjectionMetaMemoryRepo(), zap.NewNop())
	cache.RegisterProjectionAppliers(service.ProjectionCacheRepositories{Workers: repo})
	requireApply(t, cache, decodeWorkerCPState(t, live))

	decoded := decodeWorkerCPState(t, dead)
	if !decoded.Tombstone || decoded.DTag != "worker:state:"+worker.PubKey {
		t.Fatalf("tombstone decoded as tombstone=%t d=%q", decoded.Tombstone, decoded.DTag)
	}
	requireApply(t, cache, decoded)
	if got := repo.workers[worker.PubKey]; got == nil || got.Status != domain.WorkerStatusOffline {
		t.Fatalf("worker after tombstone = %+v", got)
	}
}

func TestWorkerCPStateStaleRecordDoesNotOverwriteNewer(t *testing.T) {
	publisher, capture, _, worker := workerCPStateFixture(t)
	older := publishedWorkerState(t, publisher, capture, worker)
	worker.Name = "renamed-worker"
	newer := publishedWorkerState(t, publisher, capture, worker)
	if newer.CreatedAt <= older.CreatedAt {
		t.Fatalf("publisher did not order republishes: %d then %d", older.CreatedAt, newer.CreatedAt)
	}

	repo := newStandbyProjectionWorkerRepo()
	cache := service.NewRelayProjectionCache(newRelayProjectionMetaMemoryRepo(), zap.NewNop())
	cache.RegisterProjectionAppliers(service.ProjectionCacheRepositories{Workers: repo})
	requireApply(t, cache, decodeWorkerCPState(t, newer))
	requireApply(t, cache, decodeWorkerCPState(t, older))
	if got := repo.workers[worker.PubKey]; got == nil || got.Name != "renamed-worker" {
		t.Fatalf("stale replay overwrote newer worker state: %+v", got)
	}
}

// staleWriteWorkerRepo rejects upserts older than the stored advertisement,
// as PgWorkerRepository does with repository.ErrStaleWrite.
type staleWriteWorkerRepo struct {
	*standbyProjectionWorkerRepo
}

func (r staleWriteWorkerRepo) Upsert(ctx context.Context, worker *domain.Worker) error {
	if current := r.workers[worker.PubKey]; current != nil && worker.LastAdvertisementAt.Before(current.LastAdvertisementAt) {
		return repository.ErrStaleWrite
	}
	return r.standbyProjectionWorkerRepo.Upsert(ctx, worker)
}

// Replaying a relay copy older than the local row (the daemon's own earlier
// publish) is expected during bootstrap and must not fail the replay group.
func TestWorkerCPStateReplayOlderThanLocalRowIsSkipped(t *testing.T) {
	publisher, capture, _, worker := workerCPStateFixture(t)
	live := publishedWorkerState(t, publisher, capture, worker)
	local := worker
	local.Name = "local-newer"
	local.LastAdvertisementAt = worker.LastAdvertisementAt.Add(time.Minute)
	repo := staleWriteWorkerRepo{newStandbyProjectionWorkerRepo(&local)}
	cache := service.NewRelayProjectionCache(newRelayProjectionMetaMemoryRepo(), zap.NewNop())
	cache.RegisterProjectionAppliers(service.ProjectionCacheRepositories{Workers: repo})

	requireApply(t, cache, decodeWorkerCPState(t, live))
	if got := repo.workers[worker.PubKey]; got.Name != "local-newer" {
		t.Fatalf("older relay copy overwrote the local row: %+v", got)
	}
}

// Projector-shaped assignment and drain records for one worker sit on their
// own family coordinates (worker:assignment:<pk>, worker:drain:<pk>) and land
// in separate projection streams; cleanup execution has no daemon read model.
func TestWorkerCPStateFamiliesDecodeIntoSeparateStreams(t *testing.T) {
	author := gonostr.Generate()
	workerPubkey := gonostr.Generate().Public().Hex()
	record := func(legacyKind int, id string, content any) gonostr.Event {
		wireKind, tags := nostr.ControlStateEnvelope(legacyKind, id, false)
		encoded, _ := json.Marshal(content)
		ev := gonostr.Event{Kind: gonostr.Kind(wireKind), CreatedAt: gonostr.Timestamp(1_800_000_000), Tags: append(tags, gonostr.Tag{"worker", workerPubkey}), Content: string(encoded)}
		if err := ev.Sign(author); err != nil {
			t.Fatalf("sign: %v", err)
		}
		return ev
	}
	assignment := decodeWorkerCPState(t, record(nostr.KindWorkerAssignmentState, workerPubkey, domain.WorkerAssignmentState{WorkerPubKey: workerPubkey}))
	drain := decodeWorkerCPState(t, record(nostr.KindWorkerDrainStatus, workerPubkey, domain.WorkerDrainStatus{WorkerPubKey: workerPubkey}))
	eligibility := decodeWorkerCPState(t, record(nostr.KindWorkerEligibilityPreview, "preview-1", domain.WorkerEligibilityPreview{PreviewID: "preview-1"}))
	if assignment.Family != nostr.FamilyWorkerAssignment || assignment.Worker == nil || assignment.Worker.AssignmentState == nil {
		t.Fatalf("assignment decoded as %+v", assignment)
	}
	if drain.Family != nostr.FamilyWorkerDrain || drain.Worker == nil || drain.Worker.DrainStatus == nil {
		t.Fatalf("drain decoded as %+v", drain)
	}
	if eligibility.Family != nostr.FamilyWorkerEligibility || eligibility.Worker == nil || eligibility.Worker.EligibilityPreview == nil {
		t.Fatalf("eligibility decoded as %+v", eligibility)
	}

	meta := newRelayProjectionMetaMemoryRepo()
	cache := service.NewRelayProjectionCache(meta, zap.NewNop())
	requireApply(t, cache, assignment)
	requireApply(t, cache, drain)
	assertStoredMeta(t, meta, string(nostr.FamilyWorkerAssignment), kinds.WorkerAssignmentDPrefix+workerPubkey, assignment.SourceID, false)
	assertStoredMeta(t, meta, string(nostr.FamilyWorkerDrain), kinds.WorkerDrainDPrefix+workerPubkey, drain.SourceID, false)

	cleanup := gonostr.Event{Kind: gonostr.Kind(kinds.CASControlState), CreatedAt: 1_800_000_000, Tags: gonostr.Tags{
		{"d", "worker:cleanup:" + workerPubkey + ":job-1"}, {"domain", kinds.WorkerDomain}, {"schema", kinds.CASControlStateSchema},
		{"legacy_kind", kinds.CPStateFamilyWorkerCleanup.TagValue()}, {"deleted", "false"}, {"t", kinds.WorkerCleanupTopic},
	}, Content: `{"status":"completed"}`}
	if got := decodeWorkerCPState(t, cleanup); got.Family != nostr.FamilyState || got.Worker != nil {
		t.Fatalf("cleanup execution decoded as %+v", got)
	}
}
