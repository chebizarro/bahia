package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	"github.com/google/uuid"

	nostradapter "github.com/openagentsinc/bahia/internal/adapters/nostr"
	"github.com/openagentsinc/bahia/internal/controlplane"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/kinds"
)

const (
	seedDNSZone         = "prod.example.com"
	seedDNSEndpointFQDN = "checkout.prod.example.com"
	seedDNSBackendRef   = "route53"
)

var seedDNSPolicyID = uuid.MustParse("6d3f2a4e-8c1b-4f7a-9e2d-5b6c7a8d9e01")

// controlStateSeed is one projected read model: the catalog (legacy) kind and
// record id the projector keys it by, its JSON content, and the family tags
// the projector appends after the envelope.
type controlStateSeed struct {
	legacyKind int
	id         string
	content    any
	tags       nostr.Tags
}

// controlStateEvent signs seed exactly as the projector publishes it: the wire
// kind and envelope tags (d, domain, schema=bahia.cp-state.v1, legacy_kind,
// deleted=false) come from the projector's own builder, so the seed corpus
// cannot drift from the producer contract.
func controlStateEvent(seed controlStateSeed, createdAt nostr.Timestamp, author nostr.SecretKey) (nostr.Event, error) {
	wireKind, envelope := nostradapter.ControlStateEnvelope(seed.legacyKind, seed.id, false)
	if wireKind != kinds.CASControlState {
		return nostr.Event{}, fmt.Errorf("legacy kind %d is not projected through the control-state envelope", seed.legacyKind)
	}
	content, err := json.Marshal(seed.content)
	if err != nil {
		return nostr.Event{}, fmt.Errorf("encode legacy kind %d record %s: %w", seed.legacyKind, seed.id, err)
	}
	tags := make(nostr.Tags, 0, len(envelope)+len(seed.tags))
	tags = append(tags, envelope...)
	tags = append(tags, seed.tags...)
	evt := nostr.Event{Kind: nostr.Kind(wireKind), CreatedAt: createdAt, Tags: tags, Content: string(content)}
	if err := evt.Sign(author); err != nil {
		return nostr.Event{}, err
	}
	return evt, nil
}

// record merges a seed's fields with the id/deleted fields every projected
// record's content carries.
func record(id string, fields map[string]any) map[string]any {
	body := map[string]any{"id": id, "deleted": false}
	for k, v := range fields {
		body[k] = v
	}
	return body
}

// controlStateSeeds returns the projected read models the relay serves.
func controlStateSeeds(workerPubkey string, seededAt time.Time) ([]controlStateSeed, error) {
	svcEnv := nostr.Tags{{"service", "svc-1"}, {"environment", "env-1"}}
	seeds := []controlStateSeed{
		{kinds.ServiceRegistry, "svc-1", record("svc-1", map[string]any{"name": "Checkout API", "slug": "checkout-api", "artifact_repo": "registry.example/checkout", "runtime_type": "docker", "status": "running"}), nil},
		{kinds.EnvironmentRegistry, "env-1", record("env-1", map[string]any{"name": "Production", "slug": "production", "protected": true, "status": "active"}), nil},
		{kinds.ServiceState, "svc-1:env-1", record("svc-1:env-1", map[string]any{"service_id": "svc-1", "environment_id": "env-1", "drift_status": "drifted", "status": "running"}), svcEnv},
		{kinds.ArtifactRegistry, "art-1", record("art-1", map[string]any{"service_id": "svc-1", "digest": "sha256:abc", "status": "available", "created_at": "2026-06-12T12:00:00Z"}), nostr.Tags{{"service", "svc-1"}}},
		{kinds.BuildRegistry, "build-1", record("build-1", map[string]any{"service_id": "svc-1", "status": "succeeded", "created_at": "2026-06-12T11:00:00Z"}), nostr.Tags{{"service", "svc-1"}}},
		{kinds.DeploymentIntentRegistry, "intent-1", record("intent-1", map[string]any{"service_id": "svc-1", "environment_id": "env-1", "status": "pending", "created_at": "2026-06-12T12:05:00Z"}), svcEnv},
		{kinds.DeploymentRunRegistry, "run-1", record("run-1", map[string]any{"service_id": "svc-1", "environment_id": "env-1", "status": "running", "created_at": "2026-06-12T12:10:00Z"}), svcEnv},
		{kinds.PolicyRegistry, "policy-1", record("policy-1", map[string]any{"name": "Default policy", "status": "active"}), nil},
		{kinds.LLMRouteRegistry, "llm-route-1", record("llm-route-1", map[string]any{"route_id": "llm-route-1", "name": "chat-default", "model": "llama-3.1"}), nostr.Tags{{"route", "llm-route-1"}}},
		{kinds.LLMRouteState, "llm-route-1:env-1", record("llm-route-1:env-1", map[string]any{"route_id": "llm-route-1", "environment_id": "env-1", "status": "live"}), nostr.Tags{{"route", "llm-route-1"}, {"environment", "env-1"}}},
		{kinds.BackupRepositoryRegistry, "backup-repo-1", record("backup-repo-1", map[string]any{"name": "primary backups", "status": "ready"}), nil},
		{kinds.BackupPolicyRegistry, "backup-policy-1", record("backup-policy-1", map[string]any{"name": "daily", "status": "active"}), nil},
		{kinds.BackupRecipeRegistry, "backup-recipe-1", record("backup-recipe-1", map[string]any{"name": "postgres", "status": "ready"}), nil},
		{kinds.BackupDefinitionRegistry, "backup-def-1", record("backup-def-1", map[string]any{"name": "checkout db", "status": "enabled"}), nil},
		{kinds.BackupRunState, "backup-run-1", record("backup-run-1", map[string]any{"definition_id": "backup-def-1", "status": "succeeded", "created_at": "2026-06-12T09:00:00Z"}), nil},
		{kinds.BackupVerificationState, "backup-verify-1", record("backup-verify-1", map[string]any{"run_id": "backup-run-1", "status": "passed"}), nil},
		{kinds.BackupRestoreState, "backup-restore-1", record("backup-restore-1", map[string]any{"run_id": "backup-run-1", "status": "available"}), nil},
		{kinds.BackupRuntimeObservationState, "backup-obs-1", record("backup-obs-1", map[string]any{"repository_id": "backup-repo-1", "health": "healthy"}), nil},
		{kinds.MLModelRegistry, "model-1", record("model-1", map[string]any{"name": "fraud-detector", "status": "ready"}), nil},
		{kinds.MLModelVersionRegistry, "model-version-1", record("model-version-1", map[string]any{"model_id": "model-1", "version": "v1", "status": "ready"}), nil},
		{kinds.MLInferenceEndpointRegistry, "ml-endpoint-1", record("ml-endpoint-1", map[string]any{"name": "fraud-endpoint", "model_id": "model-1", "status": "ready"}), nil},
		{kinds.MLInferenceEndpointState, "ml-endpoint-state-1", record("ml-endpoint-state-1", map[string]any{"endpoint_id": "ml-endpoint-1", "status": "live"}), nil},
	}

	assignment := domain.WorkerAssignmentState{
		WorkerPubKey:      workerPubkey,
		ActiveAssignments: []domain.WorkerAssignment{{Type: domain.WorkerAssignmentService, WorkloadID: "svc-1", Status: "assigned", Movable: true, UpdatedAt: seededAt}},
		UpdatedAt:         seededAt,
	}
	drain := domain.WorkerDrainStatus{WorkerPubKey: workerPubkey, RemainingAssignments: []domain.WorkerAssignment{}, PinnedBlockers: []domain.WorkerAssignment{}, SchedulingState: domain.WorkerSchedulingActive, SafeToEnterMaintenance: true, SafeToDisable: true, UpdatedAt: seededAt}
	eligibility := domain.WorkerEligibilityPreview{PreviewID: "eligibility-1", WorkloadType: "service", EligibleWorkers: []domain.WorkerEligibilityCandidate{{WorkerPubKey: workerPubkey, WorkerName: "worker-one", Eligible: true, Score: 1, Reason: "capacity available"}}, RejectedWorkers: []domain.WorkerEligibilityCandidate{}, RankingScores: []domain.WorkerEligibilityCandidate{}, UpdatedAt: seededAt}
	seeds = append(seeds,
		controlStateSeed{kinds.CPStateFamilyWorkerAssignment.LegacyKind(), workerPubkey, assignment, nostr.Tags{{"worker", workerPubkey}, {"workload", "svc-1"}, {"status", "assigned"}}},
		controlStateSeed{kinds.CPStateFamilyWorkerDrain.LegacyKind(), workerPubkey, drain, nostr.Tags{{"worker", workerPubkey}, {"scheduling_state", string(drain.SchedulingState)}}},
		controlStateSeed{kinds.CPStateFamilyWorkerEligibility.LegacyKind(), eligibility.PreviewID, eligibility, nostr.Tags{{"worker", workerPubkey}}},
	)

	dnsSeeds, err := dnsStateSeeds(workerPubkey, seededAt)
	if err != nil {
		return nil, err
	}
	return append(seeds, dnsSeeds...), nil
}

// dnsStateSeeds mirrors the projector's DNS zone, endpoint, policy and
// backend records, including the single-letter t topics consumers REQ on.
func dnsStateSeeds(workerPubkey string, seededAt time.Time) ([]controlStateSeed, error) {
	updatedAt := seededAt.UTC().Format(time.RFC3339)
	zone := domain.DNSZone{Name: seedDNSZone, Visibility: domain.ZoneVisibilityInternal, BackendRef: seedDNSBackendRef, TTL: 60, Authoritative: true}
	if err := domain.ValidateDNSZone(&zone); err != nil {
		return nil, err
	}
	port := 443
	endpoint := domain.DNSEndpoint{
		WorkerPubkey:   workerPubkey,
		Family:         domain.DNSEndpointFamilyService,
		Name:           "checkout-api",
		Environment:    "production",
		Zone:           seedDNSZone,
		FQDN:           seedDNSEndpointFQDN,
		Protocol:       "https",
		Address:        "fd00::1",
		Port:           &port,
		Capabilities:   []string{"https"},
		Health:         domain.HealthStatusHealthy,
		DriftStatus:    domain.DriftStatusInSync,
		Source:         "fips",
		Metadata:       map[string]any{"mesh": "fips", "projection_status": "projected"},
		MaterializedAt: seededAt.UTC(),
	}
	if err := domain.ValidateDNSEndpoint(&endpoint); err != nil {
		return nil, err
	}
	return []controlStateSeed{
		{kinds.CPStateFamilyDNSZone.LegacyKind(), nostradapter.DNSZoneDTag(zone.Name),
			map[string]any{"name": zone.Name, "visibility": string(zone.Visibility), "backend_ref": zone.BackendRef, "ttl": zone.TTL, "deleted": false, "updated_at": updatedAt},
			nostr.Tags{{"zone", zone.Name}, {"backend", zone.BackendRef}, {"visibility", string(zone.Visibility)}, {"t", kinds.DNSZoneTopic}, {"t", "bahia"}}},
		{kinds.CPStateFamilyDNSEndpoint.LegacyKind(), endpoint.Coordinate, endpoint, nostradapter.DNSEndpointTags(endpoint)},
		{kinds.CPStateFamilyDNSPolicy.LegacyKind(), nostradapter.DNSPolicyDTag(seedDNSPolicyID),
			map[string]any{"id": seedDNSPolicyID.String(), "name": "public policy", "zone_id": nil, "environment_id": nil, "rules": []any{}, "enabled": true, "deleted": false, "created_at": updatedAt, "updated_at": updatedAt},
			nostr.Tags{{"policy", seedDNSPolicyID.String()}, {"enabled", "true"}, {"t", kinds.DNSPolicyTopic}, {"t", "bahia"}}},
		{kinds.CPStateFamilyDNSBackend.LegacyKind(), nostradapter.DNSBackendDTag(seedDNSBackendRef),
			map[string]any{"ref": seedDNSBackendRef, "type": string(domain.DNSBackendTypeCoreDNS), "health": string(domain.HealthStatusHealthy), "zones": []string{zone.Name}, "deleted": false, "updated_at": updatedAt},
			nostr.Tags{{"backend", seedDNSBackendRef}, {"type", string(domain.DNSBackendTypeCoreDNS)}, {"health", string(domain.HealthStatusHealthy)}, {"t", kinds.DNSBackendTopic}, {"t", "bahia"}, {"zone", zone.Name}}},
	}, nil
}

// capturePublisher records what a control-plane publisher would send to
// relays instead of sending it.
type capturePublisher struct{ events []nostr.Event }

func (c *capturePublisher) Publish(_ context.Context, ev nostr.Event) (int, error) {
	c.events = append(c.events, ev)
	return 1, nil
}

// workerStateEvent runs the control plane's WorkerStatePublisher, the real
// worker-state producer, and returns the signed event it would publish.
func workerStateEvent(ctx context.Context, worker *domain.Worker, author nostr.SecretKey) (nostr.Event, error) {
	capture := &capturePublisher{}
	publisher := controlplane.NewWorkerStatePublisher(capture, keyer.NewPlainKeySigner(author))
	if err := publisher.Publish(ctx, worker); err != nil {
		return nostr.Event{}, fmt.Errorf("publish worker state: %w", err)
	}
	if len(capture.events) != 1 {
		return nostr.Event{}, fmt.Errorf("worker state publisher emitted %d events, want 1", len(capture.events))
	}
	return capture.events[0], nil
}

// workerCleanupStateEvent runs controlplane.WorkerCleanupStatePublisher, the
// real cleanup-execution producer, and returns the signed event it would
// publish for one completed cleanup.
func workerCleanupStateEvent(ctx context.Context, workerPubkey string, seededAt time.Time, author nostr.SecretKey) (nostr.Event, error) {
	capture := &capturePublisher{}
	publisher := controlplane.NewWorkerCleanupStatePublisher(capture, keyer.NewPlainKeySigner(author))
	completedAt := seededAt.UTC()
	cleanup := events.WorkerCleanupEvent{
		WorkerPubKey: workerPubkey,
		CleanupMode:  "reclaimable_only",
		Reason:       "disk pressure",
		LoomJobID:    "cleanup-job-1",
		TargetFreeGB: 20,
		Status:       "completed",
		StartedAt:    completedAt.Add(-time.Minute),
		CompletedAt:  &completedAt,
	}
	if err := publisher.Publish(ctx, cleanup); err != nil {
		return nostr.Event{}, fmt.Errorf("publish worker cleanup state: %w", err)
	}
	if len(capture.events) != 1 {
		return nostr.Event{}, fmt.Errorf("worker cleanup state publisher emitted %d events, want 1", len(capture.events))
	}
	return capture.events[0], nil
}

// seedWorker is the worker whose state the relay serves.
func seedWorker(workerPubkey string, seededAt time.Time) *domain.Worker {
	return &domain.Worker{
		PubKey:              workerPubkey,
		Name:                "worker-one",
		Description:         "relay worker",
		Architecture:        "linux/amd64",
		MaxConcurrentJobs:   4,
		Status:              domain.WorkerStatusOnline,
		SchedulingState:     domain.WorkerSchedulingActive,
		Labels:              map[string]string{"zone": "a"},
		LastAdvertisementAt: seededAt.UTC(),
		UpdatedAt:           seededAt.UTC(),
	}
}
