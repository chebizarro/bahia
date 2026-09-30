package controlplane

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
)

func meshWorker(pubkey string) *domain.Worker {
	advertised := time.Date(2026, 9, 30, 12, 0, 0, 123456789, time.UTC)
	return &domain.Worker{
		PubKey:              pubkey,
		Name:                "mesh-worker",
		Architecture:        "linux/arm64",
		MaxConcurrentJobs:   2,
		Software:            []domain.WorkerSoftware{{Name: "docker", Version: "27.0"}},
		Status:              domain.WorkerStatusOnline,
		SchedulingState:     domain.WorkerSchedulingActive,
		FIPSOverlayAddr:     "fd00::42",
		FIPSEndpoints:       []domain.FIPSTransportEndpoint{{Transport: "udp", Address: "203.0.113.7:4242"}},
		MeshHealth:          &domain.MeshHealth{RTT: 40 * time.Millisecond, Loss: 0.01, LastReport: advertised},
		StandbyAssignments:  []domain.WorkerStandbyAssignment{{ServiceKey: "svc-1"}},
		LastAdvertisementAt: advertised,
		UpdatedAt:           advertised,
	}
}

func publishWorkerState(t *testing.T, publisher *WorkerStatePublisher, capture *captureNostrPublisher, worker *domain.Worker) nostr.Event {
	t.Helper()
	if err := publisher.Publish(context.Background(), worker); err != nil {
		t.Fatalf("publish worker state: %v", err)
	}
	return capture.events[len(capture.events)-1]
}

func newTestWorkerStatePublisher(t *testing.T) (*WorkerStatePublisher, *captureNostrPublisher) {
	t.Helper()
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	capture := &captureNostrPublisher{published: 1}
	return NewWorkerStatePublisher(capture, signer), capture
}

// The worker-state record is canonical cp-state: the envelope carries the
// CPStateFamily discriminator and the single-letter t topic web REQs use.
func TestWorkerStatePublisherEmitsCPStateEnvelope(t *testing.T) {
	publisher, capture := newTestWorkerStatePublisher(t)
	workerPubkey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	event := publishWorkerState(t, publisher, capture, meshWorker(workerPubkey))

	if int(event.Kind) != kinds.CASControlState {
		t.Fatalf("worker state kind = %d, want %d", event.Kind, kinds.CASControlState)
	}
	wantEnvelope := nostr.Tags{
		{"d", "worker:state:" + workerPubkey},
		{"domain", "worker"},
		{"schema", "bahia.cp-state.v1"},
		{"legacy_kind", "32000"},
		{"deleted", "false"},
		{"t", "worker-state"},
	}
	for i, want := range wantEnvelope {
		if !slices.Equal(event.Tags[i], want) {
			t.Fatalf("tag[%d] = %v, want %v (tags %v)", i, event.Tags[i], want, event.Tags)
		}
	}
	if got := tagValueNostr(event.Tags, "worker"); got != workerPubkey {
		t.Fatalf("worker tag = %q", got)
	}
}

// Content is the full worker, so FIPS mesh fields reach the web mesh page and
// a relay replay into the worker repository loses nothing.
func TestWorkerStatePublisherContentIsLosslessAndCarriesMeshFields(t *testing.T) {
	publisher, capture := newTestWorkerStatePublisher(t)
	worker := meshWorker(testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex()))
	event := publishWorkerState(t, publisher, capture, worker)

	var raw map[string]any
	if err := json.Unmarshal([]byte(event.Content), &raw); err != nil {
		t.Fatalf("content json: %v", err)
	}
	for _, key := range []string{"fips_overlay_addr", "fips_endpoints", "mesh_health", "standby_assignments", "software"} {
		if raw[key] == nil {
			t.Fatalf("worker state content missing %s: %v", key, raw)
		}
	}
	if raw["deleted"] != false {
		t.Fatalf("live worker state content deleted = %v", raw["deleted"])
	}
	var decoded domain.Worker
	if err := json.Unmarshal([]byte(event.Content), &decoded); err != nil {
		t.Fatalf("decode worker: %v", err)
	}
	if decoded.FIPSOverlayAddr != "fd00::42" || len(decoded.FIPSEndpoints) != 1 || decoded.FIPSEndpoints[0].Address != "203.0.113.7:4242" ||
		decoded.MeshHealth == nil || decoded.MeshHealth.RTT != 40*time.Millisecond || len(decoded.Software) != 1 ||
		!decoded.LastAdvertisementAt.Equal(worker.LastAdvertisementAt) {
		t.Fatalf("worker state did not round-trip: %+v", decoded)
	}
}

// A tombstone must replace the live record: same kind, d and t topic, with
// deleted="true". Both worker publishers build tags through one envelope.
func TestWorkerCPStateTombstonesShareLiveCoordinateAndTopic(t *testing.T) {
	worker := meshWorker("worker-pubkey-1")
	cleanup := events.WorkerCleanupEvent{WorkerPubKey: "worker-pubkey-1", CleanupMode: "reclaimable_only", LoomJobID: "job-1", Status: "completed"}
	cleanupID := workerCleanupStateID(cleanup)
	for name, pair := range map[string][2]nostr.Tags{
		"worker state":   {workerStateTags(worker, false), workerStateTags(worker, true)},
		"worker cleanup": {workerCleanupStateTags(cleanupID, cleanup, false), workerCleanupStateTags(cleanupID, cleanup, true)},
	} {
		live, dead := pair[0], pair[1]
		for _, key := range []string{"d", "domain", "schema", "legacy_kind", "t"} {
			if tagValueNostr(live, key) != tagValueNostr(dead, key) || tagValueNostr(live, key) == "" {
				t.Fatalf("%s: %s differs between live %v and tombstone %v", name, key, live, dead)
			}
		}
		if tagValueNostr(live, "deleted") != "false" || tagValueNostr(dead, "deleted") != "true" {
			t.Fatalf("%s: deleted live=%q tombstone=%q", name, tagValueNostr(live, "deleted"), tagValueNostr(dead, "deleted"))
		}
	}
}

// Relays keep only the newest addressable event per coordinate, so a
// republish inside the same second must still sort after the previous one.
func TestWorkerStatePublisherRepublishSupersedesPreviousRecord(t *testing.T) {
	publisher, capture := newTestWorkerStatePublisher(t)
	worker := meshWorker(testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex()))
	first := publishWorkerState(t, publisher, capture, worker)
	worker.Status = domain.WorkerStatusOffline
	second := publishWorkerState(t, publisher, capture, worker)
	if second.CreatedAt <= first.CreatedAt {
		t.Fatalf("republish created_at %d does not supersede %d", second.CreatedAt, first.CreatedAt)
	}
	if tagValueNostr(first.Tags, "d") != tagValueNostr(second.Tags, "d") {
		t.Fatalf("republish moved the coordinate: %v vs %v", first.Tags, second.Tags)
	}
}

// Every domain.Worker JSON field is either published or deliberately listed in
// workerStateUnpublishedFields. A new Worker field fails here until someone
// decides whether it belongs on the relay.
func TestWorkerStateContentPublishesEveryWorkerFieldDeliberately(t *testing.T) {
	published := map[string]bool{"deleted": true}
	workerType := reflect.TypeOf(domain.Worker{})
	var fields []string
	for i := 0; i < workerType.NumField(); i++ {
		name := strings.Split(workerType.Field(i).Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		fields = append(fields, name)
		published[name] = !slices.Contains(workerStateUnpublishedFields, name)
	}
	sort.Strings(fields)
	want := []string{
		"accelerators", "architecture", "capabilities", "created_at", "current_queue_depth", "description", "fips_endpoints", "fips_overlay_addr",
		"geohash", "heartbeat_status", "labels", "last_advertisement_at", "last_heartbeat_at", "max_concurrent_jobs", "max_duration_secs", "mesh_health",
		"min_duration_secs", "ml_capabilities", "name", "preferred_relays", "pressure", "pricing", "pubkey", "resources", "runtime_target",
		"scheduling_note", "scheduling_state", "software", "standby_assignments", "status", "telemetry", "updated_at", "verified_execution_planes",
	}
	if !slices.Equal(fields, want) {
		t.Fatalf("domain.Worker JSON fields changed; decide whether each is published (workerStateUnpublishedFields):\n got %v\nwant %v", fields, want)
	}

	worker := meshWorker("worker-pubkey-1")
	worker.VerifiedExecutionPlanes = []domain.VerifiedExecutionPlaneCapabilities{{Generation: 1}}
	content, err := workerStateContent(worker)
	if err != nil {
		t.Fatalf("worker state content: %v", err)
	}
	for key := range content {
		if !published[key] {
			t.Fatalf("worker state content publishes %q", key)
		}
	}
}
