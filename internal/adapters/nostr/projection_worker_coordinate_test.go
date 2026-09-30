package nostr

import (
	"context"
	"strings"
	"testing"

	gonostr "fiatjaf.com/nostr"
	"go.uber.org/zap"

	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// fakeWorkerReadModelSource serves one worker's assignment and drain read
// models to the projector.
type fakeWorkerReadModelSource struct {
	assignment domain.WorkerAssignmentState
	drain      domain.WorkerDrainStatus
}

func (s *fakeWorkerReadModelSource) ListAssignmentStates(context.Context) ([]domain.WorkerAssignmentState, error) {
	return []domain.WorkerAssignmentState{s.assignment}, nil
}

func (s *fakeWorkerReadModelSource) GetAssignmentState(context.Context, string) (*domain.WorkerAssignmentState, error) {
	state := s.assignment
	return &state, nil
}

func (s *fakeWorkerReadModelSource) ListDrainStatuses(context.Context) ([]domain.WorkerDrainStatus, error) {
	return []domain.WorkerDrainStatus{s.drain}, nil
}

func (s *fakeWorkerReadModelSource) GetDrainStatus(context.Context, string) (*domain.WorkerDrainStatus, error) {
	status := s.drain
	return &status, nil
}

// TestProjectorWorkerFamiliesCoexistOnRelay pins bahia-irsry.36 against a relay
// with NIP-01 addressable replacement: the projector's assignment and drain
// records for one worker (and a worker-state or eligibility record built by
// the same envelope) sit on distinct coordinates, so the relay serves every
// family, and a tombstone replaces only its own family's record.
func TestProjectorWorkerFamiliesCoexistOnRelay(t *testing.T) {
	ctx := context.Background()
	workerPubkey := strings.Repeat("ab", 32)
	relay := newReplaceableRelay()
	source := &fakeWorkerReadModelSource{
		assignment: domain.WorkerAssignmentState{WorkerPubKey: workerPubkey, ActiveAssignments: []domain.WorkerAssignment{{Type: domain.WorkerAssignmentService, WorkloadID: "svc-1"}}},
		drain:      domain.WorkerDrainStatus{WorkerPubKey: workerPubkey, SchedulingState: domain.WorkerSchedulingDraining},
	}
	projector := newTestProjector(projectorTestConfig(), newFakeProjectionSource(), relay, newMemoryNostrEventRepo(), zap.NewNop(), WithWorkerReadModelProjectionSource(source))

	projector.publishWorkerReadModelsForWorker(ctx, workerPubkey)
	if err := projector.publishReplaceableJSON(ctx, KindWorkerEligibilityPreview, "preview-1", nil, domain.WorkerEligibilityPreview{PreviewID: "preview-1"}, "worker_eligibility.projection", nil); err != nil {
		t.Fatalf("publish eligibility: %v", err)
	}
	if err := projector.publishReplaceableJSON(ctx, KindWorkerState, workerPubkey, nil, domain.Worker{PubKey: workerPubkey}, "worker_state.projection", nil); err != nil {
		t.Fatalf("publish worker state: %v", err)
	}

	families := map[int]string{
		KindWorkerAssignmentState:    kinds.WorkerAssignmentDPrefix + workerPubkey,
		KindWorkerDrainStatus:        kinds.WorkerDrainDPrefix + workerPubkey,
		KindWorkerEligibilityPreview: kinds.WorkerEligibilityDPrefix + "preview-1",
		KindWorkerState:              kinds.WorkerStateDPrefix + workerPubkey,
	}
	for legacyKind, wantD := range families {
		live := onlyLive(t, relay, legacyKind)
		if eventDTag(live) != wantD {
			t.Fatalf("family %d published on d=%q, want %q", legacyKind, eventDTag(live), wantD)
		}
	}
	if got := relay.countKind(KindCASControlState); got != len(families) {
		t.Fatalf("relay received %d cp-state events, want %d", got, len(families))
	}

	// The assignment tombstone shares only the assignment coordinate.
	assignmentLive := onlyLive(t, relay, KindWorkerAssignmentState)
	if err := projector.publishReplaceableTombstone(ctx, KindWorkerAssignmentState, workerPubkey, nil, map[string]any{"deleted": true, "worker_pubkey": workerPubkey}, "worker_assignment_state.projection", nil); err != nil {
		t.Fatalf("publish assignment tombstone: %v", err)
	}
	assertRelayTombstoned(t, relay, assignmentLive)
	for _, legacyKind := range []int{KindWorkerDrainStatus, KindWorkerState, KindWorkerEligibilityPreview} {
		if live := onlyLive(t, relay, legacyKind); eventDTag(live) != families[legacyKind] {
			t.Fatalf("family %d lost its live record after the assignment tombstone", legacyKind)
		}
	}
}

// TestCatalogKeysWorkerRecordsByFamilyCoordinate: the catalog reads a worker
// record's id off its family coordinate when content and tags omit it, and
// skips a record that is not on its family's coordinate (the bare-pubkey d
// assignment and drain shared before bahia-irsry.36).
func TestCatalogKeysWorkerRecordsByFamilyCoordinate(t *testing.T) {
	workerPubkey := strings.Repeat("cd", 32)
	decode, ok := NewKindCatalog().Decoder(KindCASControlState)
	if !ok {
		t.Fatal("no catalog decoder for 30900")
	}
	record := func(legacyKind int, d string) *gonostr.Event {
		_, tags := controlStateEnvelope(legacyKind, "", false)
		for i, tag := range tags {
			if tag[0] == "d" {
				tags[i] = gonostr.Tag{"d", d}
			}
		}
		return &gonostr.Event{Kind: gonostr.Kind(KindCASControlState), CreatedAt: 1_800_000_000, Tags: tags, Content: `{}`}
	}

	for legacyKind, family := range map[int]ProjectionFamily{
		KindWorkerState:           FamilyWorker,
		KindWorkerAssignmentState: FamilyWorkerAssignment,
		KindWorkerDrainStatus:     FamilyWorkerDrain,
	} {
		prefix, _ := kinds.CPStateFamily(legacyKind).WorkerDPrefix()
		decoded, err := decode(record(legacyKind, prefix+workerPubkey))
		if err != nil || decoded == nil || decoded.Family != family || decoded.DTag != prefix+workerPubkey {
			t.Fatalf("family %d on its coordinate decoded as %+v, %v", legacyKind, decoded, err)
		}
		if legacyKind == KindWorkerState && (decoded.Worker == nil || decoded.Worker.Worker.PubKey != workerPubkey) {
			t.Fatalf("worker state pubkey not read off d: %+v", decoded.Worker)
		}

		skipped, err := decode(record(legacyKind, workerPubkey))
		if err != nil || skipped != nil {
			t.Fatalf("family %d on the bare-pubkey d decoded as %+v, %v; want skipped", legacyKind, skipped, err)
		}
	}
}
