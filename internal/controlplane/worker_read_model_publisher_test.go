package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/kinds"
)

// fakeReadModelSource stubs the WorkerReadModelSource interface for testing.
type fakeReadModelSource struct {
	assignment *domain.WorkerAssignmentState
	drain      *domain.WorkerDrainStatus
	getCalls   int
}

func (s *fakeReadModelSource) GetAssignmentState(_ context.Context, _ string) (*domain.WorkerAssignmentState, error) {
	s.getCalls++
	return s.assignment, nil
}

func (s *fakeReadModelSource) GetDrainStatus(_ context.Context, _ string) (*domain.WorkerDrainStatus, error) {
	s.getCalls++
	return s.drain, nil
}

func newTestWorkerReadModelPublisher(capture *captureNostrPublisher, source *fakeReadModelSource) *WorkerReadModelPublisher {
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	return NewWorkerReadModelPublisher(capture, signer, source, nil)
}

func TestWorkerReadModelPublisherAssignmentPublishedOnce(t *testing.T) {
	workerPubkey := "aabb" + "ccdd" + "eeff" + "0011" + "aabb" + "ccdd" + "eeff" + "0011" + "aabb" + "ccdd" + "eeff" + "0011" + "aabb" + "ccdd" + "eeff" + "0011"
	source := &fakeReadModelSource{
		assignment: &domain.WorkerAssignmentState{
			WorkerPubKey:      workerPubkey,
			ActiveAssignments: []domain.WorkerAssignment{{Type: domain.WorkerAssignmentService, WorkloadID: "svc-1", Status: "running"}},
		},
		drain: &domain.WorkerDrainStatus{
			WorkerPubKey:    workerPubkey,
			SchedulingState: domain.WorkerSchedulingActive,
		},
	}
	capture := &captureNostrPublisher{published: 1}
	pub := newTestWorkerReadModelPublisher(capture, source)

	pub.PublishForWorker(context.Background(), workerPubkey)

	if len(capture.events) != 2 {
		t.Fatalf("expected 2 events (assignment + drain), got %d", len(capture.events))
	}

	// Verify assignment event is cp-state 30900 with correct tags.
	assignmentEv := capture.events[0]
	if assignmentEv.Kind != KindCASControlState {
		t.Fatalf("expected kind %d, got %d", KindCASControlState, assignmentEv.Kind)
	}
	if tagValueNostr(assignmentEv.Tags, "legacy_kind") != kinds.CPStateFamilyWorkerAssignment.TagValue() {
		t.Fatalf("assignment legacy_kind = %q, want %q", tagValueNostr(assignmentEv.Tags, "legacy_kind"), kinds.CPStateFamilyWorkerAssignment.TagValue())
	}
	if tagValueNostr(assignmentEv.Tags, "t") != kinds.WorkerAssignmentTopic {
		t.Fatalf("assignment topic = %q, want %q", tagValueNostr(assignmentEv.Tags, "t"), kinds.WorkerAssignmentTopic)
	}
	if tagValueNostr(assignmentEv.Tags, "worker") != workerPubkey {
		t.Fatalf("assignment worker tag = %q, want %q", tagValueNostr(assignmentEv.Tags, "worker"), workerPubkey)
	}
	if tagValueNostr(assignmentEv.Tags, "domain") != kinds.WorkerDomain {
		t.Fatalf("assignment domain = %q, want %q", tagValueNostr(assignmentEv.Tags, "domain"), kinds.WorkerDomain)
	}

	var content map[string]any
	if err := json.Unmarshal([]byte(assignmentEv.Content), &content); err != nil {
		t.Fatalf("unmarshal assignment content: %v", err)
	}
	if content["worker_pubkey"] != workerPubkey {
		t.Fatalf("content.worker_pub_key = %v, want %q", content["worker_pubkey"], workerPubkey)
	}
}

func TestWorkerReadModelPublisherDrainPublished(t *testing.T) {
	workerPubkey := "ddee" + "ff00" + "1122" + "3344" + "ddee" + "ff00" + "1122" + "3344" + "ddee" + "ff00" + "1122" + "3344" + "ddee" + "ff00" + "1122" + "3344"
	source := &fakeReadModelSource{
		assignment: &domain.WorkerAssignmentState{
			WorkerPubKey:      workerPubkey,
			ActiveAssignments: []domain.WorkerAssignment{},
		},
		drain: &domain.WorkerDrainStatus{
			WorkerPubKey:    workerPubkey,
			SchedulingState: domain.WorkerSchedulingDraining,
		},
	}
	capture := &captureNostrPublisher{published: 1}
	pub := newTestWorkerReadModelPublisher(capture, source)

	pub.PublishForWorker(context.Background(), workerPubkey)

	if len(capture.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(capture.events))
	}

	// Drain is the second event.
	drainEv := capture.events[1]
	if drainEv.Kind != KindCASControlState {
		t.Fatalf("expected kind %d, got %d", KindCASControlState, drainEv.Kind)
	}
	if tagValueNostr(drainEv.Tags, "legacy_kind") != kinds.CPStateFamilyWorkerDrain.TagValue() {
		t.Fatalf("drain legacy_kind = %q, want %q", tagValueNostr(drainEv.Tags, "legacy_kind"), kinds.CPStateFamilyWorkerDrain.TagValue())
	}
	if tagValueNostr(drainEv.Tags, "t") != kinds.WorkerDrainTopic {
		t.Fatalf("drain topic = %q, want %q", tagValueNostr(drainEv.Tags, "t"), kinds.WorkerDrainTopic)
	}
	if tagValueNostr(drainEv.Tags, "scheduling_state") != string(domain.WorkerSchedulingDraining) {
		t.Fatalf("drain scheduling_state = %q, want %q", tagValueNostr(drainEv.Tags, "scheduling_state"), string(domain.WorkerSchedulingDraining))
	}
}

func TestWorkerReadModelPublisherEligibilityPublished(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	pub := NewWorkerReadModelPublisher(capture, signer, nil, nil)

	preview := &domain.WorkerEligibilityPreview{
		PreviewID:    "preview-abc",
		WorkloadType: "service",
	}
	if err := pub.publishEligibilityPreview(context.Background(), preview); err != nil {
		t.Fatalf("publish eligibility: %v", err)
	}

	if len(capture.events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(capture.events))
	}
	ev := capture.events[0]
	if tagValueNostr(ev.Tags, "legacy_kind") != kinds.CPStateFamilyWorkerEligibility.TagValue() {
		t.Fatalf("eligibility legacy_kind = %q, want %q", tagValueNostr(ev.Tags, "legacy_kind"), kinds.CPStateFamilyWorkerEligibility.TagValue())
	}
	if tagValueNostr(ev.Tags, "t") != kinds.WorkerEligibilityTopic {
		t.Fatalf("eligibility topic = %q, want %q", tagValueNostr(ev.Tags, "t"), kinds.WorkerEligibilityTopic)
	}
	if tagValueNostr(ev.Tags, "preview_id") != "preview-abc" {
		t.Fatalf("eligibility preview_id = %q, want %q", tagValueNostr(ev.Tags, "preview_id"), "preview-abc")
	}
}

func TestWorkerReadModelPublisherNoopOnEmptyPubKey(t *testing.T) {
	source := &fakeReadModelSource{}
	capture := &captureNostrPublisher{published: 1}
	pub := newTestWorkerReadModelPublisher(capture, source)

	pub.PublishForWorker(context.Background(), "")
	pub.PublishForWorker(context.Background(), "   ")

	if len(capture.events) != 0 {
		t.Fatalf("expected no events for empty pubkey, got %d", len(capture.events))
	}
	if source.getCalls != 0 {
		t.Fatalf("expected no source lookups for empty pubkey, got %d", source.getCalls)
	}
}

func TestWorkerReadModelPublisherNoopOnNilSource(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	signer, _ := NewPrivateKeySigner(nostr.Generate().Hex())
	pub := NewWorkerReadModelPublisher(capture, signer, nil, nil)

	// Should not panic, should not publish.
	pub.PublishForWorker(context.Background(), "some-pubkey")
	if len(capture.events) != 0 {
		t.Fatalf("expected no events for nil source, got %d", len(capture.events))
	}
}

func TestWorkerReadModelPublisherNoRelayAcceptance(t *testing.T) {
	workerPubkey := "1234" + "5678" + "9abc" + "def0" + "1234" + "5678" + "9abc" + "def0" + "1234" + "5678" + "9abc" + "def0" + "1234" + "5678" + "9abc" + "def0"
	source := &fakeReadModelSource{
		assignment: &domain.WorkerAssignmentState{
			WorkerPubKey:      workerPubkey,
			ActiveAssignments: []domain.WorkerAssignment{},
		},
		drain: &domain.WorkerDrainStatus{
			WorkerPubKey:    workerPubkey,
			SchedulingState: domain.WorkerSchedulingActive,
		},
	}
	// published: 0 → Publish returns 0 relay acceptances, triggering the
	// error path. PublishForWorker logs warnings but doesn't return errors,
	// so the test just verifies it doesn't panic.
	capture := &captureNostrPublisher{published: 0}
	pub := newTestWorkerReadModelPublisher(capture, source)

	pub.PublishForWorker(context.Background(), workerPubkey)

	if len(capture.events) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(capture.events))
	}
}

func TestWorkerReadModelPublisherDistinctCoordinates(t *testing.T) {
	workerPubkey := "aabb" + "ccdd" + "eeff" + "0011" + "aabb" + "ccdd" + "eeff" + "0011" + "aabb" + "ccdd" + "eeff" + "0011" + "aabb" + "ccdd" + "eeff" + "0011"
	source := &fakeReadModelSource{
		assignment: &domain.WorkerAssignmentState{
			WorkerPubKey:      workerPubkey,
			ActiveAssignments: []domain.WorkerAssignment{{Type: domain.WorkerAssignmentService, WorkloadID: "svc-1"}},
		},
		drain: &domain.WorkerDrainStatus{
			WorkerPubKey:    workerPubkey,
			SchedulingState: domain.WorkerSchedulingActive,
		},
	}
	capture := &captureNostrPublisher{published: 1}
	pub := newTestWorkerReadModelPublisher(capture, source)

	pub.PublishForWorker(context.Background(), workerPubkey)

	// Assignment and drain must be published on distinct d-tag coordinates.
	if len(capture.events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(capture.events))
	}
	assignmentD := tagValueNostr(capture.events[0].Tags, "d")
	drainD := tagValueNostr(capture.events[1].Tags, "d")
	if assignmentD == drainD {
		t.Fatalf("assignment and drain share d-tag %q — must be distinct coordinates", assignmentD)
	}
	wantAssignmentD := kinds.WorkerAssignmentDPrefix + workerPubkey
	if assignmentD != wantAssignmentD {
		t.Fatalf("assignment d = %q, want %q", assignmentD, wantAssignmentD)
	}
	wantDrainD := kinds.WorkerDrainDPrefix + workerPubkey
	if drainD != wantDrainD {
		t.Fatalf("drain d = %q, want %q", drainD, wantDrainD)
	}
}
