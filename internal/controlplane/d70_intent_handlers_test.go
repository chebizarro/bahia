package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type d70DNSCanonical struct{ zones, policies int }

func (p *d70DNSCanonical) PublishZone(context.Context, domain.DNSZone) error { p.zones++; return nil }
func (p *d70DNSCanonical) PublishPolicy(context.Context, domain.DNSPolicy) error {
	p.policies++
	return nil
}

func d70Processor(t *testing.T, domainName, actor string, handler DomainHandler) (*IntentProcessor, *statusCollector) {
	t.Helper()
	statuses := &statusCollector{}
	status := NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop())
	knownNonFleet := "known-non-fleet-principal"
	trust := NewTrustSet([]string{actor}, zap.NewNop(), WithBootstrapOwners(map[string]string{testOrgID().String(): knownNonFleet}))
	processor := NewIntentProcessor(trust, openTestStore(t), status, IntentProcessorConfig{EnabledDomains: map[string]bool{domainName: true}}, zap.NewNop())
	processor.RegisterHandler(domainName, handler)
	return processor, statuses
}

func d70Intent(domainName, op, coordinate, actor string, content map[string]interface{}) *Intent {
	return &Intent{Domain: domainName, Op: op, Coordinate: coordinate, IntentID: uuid.NewString(), OrgID: testOrgID(), Actor: actor, Content: content}
}

func assertD70AcceptedReplayAndUnauthorized(t *testing.T, p *IntentProcessor, statuses *statusCollector, intent *Intent, effects func() int) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, p.ProcessInProcess(ctx, intent))
	require.Len(t, statuses.events, 1)
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	require.Equal(t, nostr.Kind(30315), statuses.events[0].Kind)
	firstEffects := effects()
	require.Greater(t, firstEffects, 0)
	require.NoError(t, p.ProcessInProcess(ctx, intent))
	require.Equal(t, firstEffects, effects(), "replay must not repeat a mutation or canonical publish")
	require.Len(t, statuses.events, 1, "replay must not publish another status")
	unauthorized := *intent
	unauthorized.IntentID = uuid.NewString()
	unauthorized.Actor = "known-non-fleet-principal"
	require.ErrorContains(t, p.ProcessInProcess(ctx, &unauthorized), "insufficient permission")
	require.Equal(t, firstEffects, effects())
	require.Len(t, statuses.events, 2)
	require.Equal(t, "rejected", tagValueNostr(statuses.events[1].Tags, "status"))
}

func TestD70DNSIntentSupportedMutations(t *testing.T) {
	actor := testPubkey
	t.Run("zone-create", func(t *testing.T) {
		op := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{}, backends: map[string]bool{"primary": true}}}
		canonical := &d70DNSCanonical{}
		p, status := d70Processor(t, "dns", actor, NewDNSIntentHandler(op, canonical))
		intent := d70Intent("dns", "zone-create", "zone:new.example", actor, map[string]interface{}{"name": "new.example", "visibility": "internal", "backend_ref": "primary", "ttl": 60})
		assertD70AcceptedReplayAndUnauthorized(t, p, status, intent, func() int { return len(op.zonesCreated) + canonical.zones })
		require.Len(t, op.reconciled, 1)
		require.Equal(t, 1, canonical.zones)
	})
	t.Run("policy-apply", func(t *testing.T) {
		repo := &recordingDNSPolicyRepository{}
		op := &recordingDNSOperator{policyRepo: repo}
		canonical := &d70DNSCanonical{}
		p, status := d70Processor(t, "dns", actor, NewDNSIntentHandler(op, canonical))
		id := uuid.New()
		intent := d70Intent("dns", "policy-apply", "dnspolicy:"+id.String(), actor, map[string]interface{}{"id": id.String(), "name": "internal", "enabled": true, "rules": []interface{}{map[string]interface{}{"match": map[string]interface{}{"environment": "prod"}, "action": map[string]interface{}{"ttl_override": 60}}}})
		assertD70AcceptedReplayAndUnauthorized(t, p, status, intent, func() int { return len(repo.created) + canonical.policies })
		require.Equal(t, 1, op.reconcileAll)
		require.Equal(t, 1, canonical.policies)
	})
	t.Run("record-set", func(t *testing.T) {
		op := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{"new.example": true}}}
		p, status := d70Processor(t, "dns", actor, NewDNSIntentHandler(op, &d70DNSCanonical{}))
		id := uuid.New()
		intent := d70Intent("dns", "record-set", "dns-override:"+id.String(), actor, map[string]interface{}{"id": id.String(), "zone_name": "new.example", "record_name": "api", "record_type": "A", "value": "192.0.2.1", "ttl": 60, "reason": "operator pin"})
		assertD70AcceptedReplayAndUnauthorized(t, p, status, intent, func() int { return len(op.overridesCreated) + len(op.reconciled) })
		require.Len(t, op.reconciled, 1, "reconcile is the canonical endpoint publication path")
	})
	t.Run("override-retire", func(t *testing.T) {
		op := newRetirableOverrideStore(time.Now().UTC())
		id := uuid.New()
		seedAstilleroOverride(t, op, id, time.Now().Add(-time.Hour))
		p, status := d70Processor(t, "dns", actor, NewDNSIntentHandler(op, &d70DNSCanonical{}))
		intent := d70Intent("dns", "override-retire", "dns-override:"+id.String(), actor, map[string]interface{}{"override_id": id.String(), "reason": "end pin"})
		assertD70AcceptedReplayAndUnauthorized(t, p, status, intent, func() int { return op.expireCalls + len(op.reconciled) })
		require.Equal(t, 1, op.expireCalls)
	})
}

func TestD70DNSIntentRejectsUnsupportedRevision(t *testing.T) {
	operator := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{}, backends: map[string]bool{"primary": true}}}
	canonical := &d70DNSCanonical{}
	processor, statuses := d70Processor(t, "dns", testPubkey, NewDNSIntentHandler(operator, canonical))
	intent := d70Intent("dns", "zone-create", "zone:new.example", testPubkey,
		map[string]interface{}{"name": "new.example", "visibility": "internal", "backend_ref": "primary", "ttl": 60,
			"expected_updated_at": "2026-10-03T09:12:13Z"})
	require.ErrorContains(t, processor.ProcessInProcess(context.Background(), intent), "expected_updated_at is not supported")
	require.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
	require.Empty(t, operator.zonesCreated)
	require.Zero(t, canonical.zones)
}

func TestD70DNSIntentUnsupportedCRUDRejected(t *testing.T) {
	p, statuses := d70Processor(t, "dns", testPubkey, NewDNSIntentHandler(&recordingDNSOperator{}, &d70DNSCanonical{}))
	for _, op := range []string{"zone-update", "zone-delete", "endpoint-create", "endpoint-update", "endpoint-delete", "backend-create", "backend-update", "backend-delete", "policy-update", "policy-delete"} {
		intent := d70Intent("dns", op, "dns-gap:"+op, testPubkey, map[string]interface{}{"id": uuid.NewString()})
		require.ErrorContains(t, p.ProcessInProcess(context.Background(), intent), "unsupported op: dns "+op)
		require.Equal(t, "rejected", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
		require.Contains(t, statuses.events[len(statuses.events)-1].Content, "no durable mutation path")
	}
}

type d70MLRepo struct {
	repository.MLRegistryRepository
	models    map[uuid.UUID]*domain.MLModel
	versions  map[uuid.UUID]*domain.MLModelVersion
	endpoints map[uuid.UUID]*domain.MLInferenceEndpoint
	writes    int
}

func newD70MLRepo() *d70MLRepo {
	return &d70MLRepo{models: map[uuid.UUID]*domain.MLModel{}, versions: map[uuid.UUID]*domain.MLModelVersion{}, endpoints: map[uuid.UUID]*domain.MLInferenceEndpoint{}}
}
func (r *d70MLRepo) UpsertModel(_ context.Context, v *domain.MLModel) error {
	cp := *v
	cp.UpdatedAt = time.Now().UTC()
	r.models[v.ID] = &cp
	r.writes++
	return nil
}
func (r *d70MLRepo) GetModel(_ context.Context, id uuid.UUID) (*domain.MLModel, error) {
	return r.models[id], nil
}
func (r *d70MLRepo) UpsertModelVersion(_ context.Context, v *domain.MLModelVersion) error {
	cp := *v
	r.versions[v.ID] = &cp
	r.writes++
	return nil
}
func (r *d70MLRepo) GetModelVersion(_ context.Context, id uuid.UUID) (*domain.MLModelVersion, error) {
	return r.versions[id], nil
}
func (r *d70MLRepo) UpsertInferenceEndpoint(_ context.Context, v *domain.MLInferenceEndpoint) error {
	cp := *v
	cp.UpdatedAt = time.Now().UTC()
	r.endpoints[v.ID] = &cp
	r.writes++
	return nil
}
func (r *d70MLRepo) GetInferenceEndpoint(_ context.Context, id uuid.UUID) (*domain.MLInferenceEndpoint, error) {
	return r.endpoints[id], nil
}

type d70MLCanonical struct {
	models, versions, endpoints int
	fail                        error
}

func (p *d70MLCanonical) PublishModel(context.Context, *domain.MLModel) error {
	p.models++
	return p.fail
}
func (p *d70MLCanonical) PublishModelVersion(context.Context, *domain.MLModelVersion) error {
	p.versions++
	return p.fail
}
func (p *d70MLCanonical) PublishEndpoint(context.Context, *domain.MLInferenceEndpoint) error {
	p.endpoints++
	return p.fail
}
func (p *d70MLCanonical) PublishEndpointState(context.Context, *domain.MLInferenceState) error {
	return nil
}
func (p *d70MLCanonical) PublishProvenanceGraph(context.Context, *domain.MLArtifactRef) error {
	return nil
}
func (p *d70MLCanonical) PublishCapabilityProfile(context.Context, *domain.Worker) error { return nil }
func (p *d70MLCanonical) count() int                                                     { return p.models + p.versions + p.endpoints }

func TestD70MLIntentSupportedUpserts(t *testing.T) {
	actor := testPubkey
	for _, tc := range []struct {
		op, kind string
		update   bool
	}{{"model-create", "model", false}, {"model-update", "model", true}, {"version-create", "version", false}, {"version-update", "version", true}, {"endpoint-create", "endpoint", false}, {"endpoint-update", "endpoint", true}} {
		t.Run(tc.op, func(t *testing.T) {
			repo, canonical := newD70MLRepo(), &d70MLCanonical{}
			registry := service.NewMLRegistryService(repo, &events.NoopPublisher{}, zap.NewNop())
			registry.SetMLCPStatePublisher(canonical)
			p, status := d70Processor(t, "ml", actor, NewMLIntentHandler(registry))
			id := uuid.New()
			var coordinate string
			var content map[string]interface{}
			switch tc.kind {
			case "model":
				coordinate = "model:sample"
				content = map[string]interface{}{"id": id.String(), "slug": "sample", "name": "Sample"}
				if tc.update {
					repo.models[id] = &domain.MLModel{ID: id, Slug: "sample", Name: "Old", UpdatedAt: time.Now().UTC()}
				}
			case "version":
				modelID := uuid.New()
				repo.models[modelID] = &domain.MLModel{ID: modelID, Slug: "sample", Name: "Sample"}
				coordinate = "model-version:" + id.String()
				content = map[string]interface{}{"id": id.String(), "model_id": modelID.String(), "version": "v1", "source": map[string]interface{}{"uri": "s3://model/v1"}}
				if tc.update {
					repo.versions[id] = &domain.MLModelVersion{ID: id, ModelID: modelID, Version: "v1", Source: domain.MLSourceRef{URI: "s3://old"}}
				}
			case "endpoint":
				coordinate = "endpoint:" + id.String()
				content = map[string]interface{}{"id": id.String(), "name": "inference", "environment_id": uuid.NewString()}
				if tc.update {
					repo.endpoints[id] = &domain.MLInferenceEndpoint{ID: id, Name: "inference", EnvironmentID: uuid.MustParse(content["environment_id"].(string)), UpdatedAt: time.Now().UTC()}
				}
			}
			intent := d70Intent("ml", tc.op, coordinate, actor, content)
			assertD70AcceptedReplayAndUnauthorized(t, p, status, intent, func() int { return repo.writes + canonical.count() })
			require.Equal(t, 1, repo.writes)
			require.Equal(t, 1, canonical.count(), "ML registry must invoke canonical publisher once")
		})
	}
}

func TestD70MLIntentUnsupportedDeletesAndConflict(t *testing.T) {
	repo, canonical := newD70MLRepo(), &d70MLCanonical{}
	registry := service.NewMLRegistryService(repo, nil, zap.NewNop())
	registry.SetMLCPStatePublisher(canonical)
	p, statuses := d70Processor(t, "ml", testPubkey, NewMLIntentHandler(registry))
	for _, op := range []string{"model-delete", "version-delete", "endpoint-delete"} {
		intent := d70Intent("ml", op, "ml-gap:"+op, testPubkey, map[string]interface{}{"id": uuid.NewString()})
		require.ErrorContains(t, p.ProcessInProcess(context.Background(), intent), "unsupported op: ml "+op)
		require.Equal(t, "rejected", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
	}
	id := uuid.New()
	repo.models[id] = &domain.MLModel{ID: id, Slug: "sample", Name: "old", UpdatedAt: time.Now().UTC()}
	stale := time.Now().Add(-time.Hour).UTC()
	intent := d70Intent("ml", "model-update", "model:sample", testPubkey, map[string]interface{}{"id": id.String(), "slug": "sample", "name": "new"})
	intent.ExpectedUpdatedAt = &stale
	require.Error(t, p.ProcessInProcess(context.Background(), intent))
	require.Equal(t, "conflict", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
	require.Equal(t, 0, repo.writes)
	matchingModel := d70Intent("ml", "model-update", "model:sample", testPubkey,
		map[string]interface{}{"id": id.String(), "slug": "sample", "name": "new", "expected_updated_at": repo.models[id].UpdatedAt.Format(time.RFC3339Nano)})
	require.NoError(t, p.ProcessInProcess(context.Background(), matchingModel))
	require.Equal(t, "accepted", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
	require.Equal(t, 1, repo.writes)
	endpointID, envID := uuid.New(), uuid.New()
	repo.endpoints[endpointID] = &domain.MLInferenceEndpoint{ID: endpointID, Name: "inference", EnvironmentID: envID, UpdatedAt: time.Now().UTC()}
	endpoint := d70Intent("ml", "endpoint-update", "endpoint:"+endpointID.String(), testPubkey,
		map[string]interface{}{"id": endpointID.String(), "name": "inference", "environment_id": envID.String()})
	endpoint.ExpectedUpdatedAt = &stale
	require.Error(t, p.ProcessInProcess(context.Background(), endpoint))
	require.Equal(t, "conflict", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
	require.Equal(t, 1, repo.writes)
	matchingEndpoint := d70Intent("ml", "endpoint-update", "endpoint:"+endpointID.String(), testPubkey,
		map[string]interface{}{"id": endpointID.String(), "name": "inference", "environment_id": envID.String(),
			"expected_updated_at": repo.endpoints[endpointID].UpdatedAt.Format(time.RFC3339Nano)})
	require.NoError(t, p.ProcessInProcess(context.Background(), matchingEndpoint))
	require.Equal(t, "accepted", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
	require.Equal(t, 2, repo.writes)
}

func TestD70MLCanonicalFailureRejectsIntent(t *testing.T) {
	repo := newD70MLRepo()
	canonical := &d70MLCanonical{fail: errors.New("relay rejected canonical record")}
	registry := service.NewMLRegistryService(repo, nil, zap.NewNop())
	registry.SetMLCPStatePublisher(canonical)
	p, statuses := d70Processor(t, "ml", testPubkey, NewMLIntentHandler(registry))
	id := uuid.New()
	intent := d70Intent("ml", "model-create", "model:failed", testPubkey, map[string]interface{}{"id": id.String(), "slug": "failed", "name": "Failed"})
	require.ErrorContains(t, p.ProcessInProcess(context.Background(), intent), "relay rejected canonical record")
	require.Len(t, statuses.events, 1)
	require.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
	require.Equal(t, 1, repo.writes)
	require.Equal(t, 1, canonical.models)
}

func TestD70WorkerIntentSchedulingLabelsAndCleanup(t *testing.T) {
	actor := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	workerPubkey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	for _, tc := range []struct {
		op              string
		initial, target domain.WorkerSchedulingState
	}{
		{"cordon", domain.WorkerSchedulingActive, domain.WorkerSchedulingCordoned},
		{"uncordon", domain.WorkerSchedulingCordoned, domain.WorkerSchedulingActive},
		{"drain", domain.WorkerSchedulingActive, domain.WorkerSchedulingDraining},
		{"undrain", domain.WorkerSchedulingDraining, domain.WorkerSchedulingActive},
		{"maintenance-enter", domain.WorkerSchedulingActive, domain.WorkerSchedulingMaintenance},
		{"maintenance-exit", domain.WorkerSchedulingMaintenance, domain.WorkerSchedulingActive},
	} {
		t.Run(tc.op, func(t *testing.T) {
			capture := &captureNostrPublisher{published: 1}
			repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker", Status: domain.WorkerStatusOnline, SchedulingState: tc.initial})
			reactor := newWorkerHandlerTestReactor(t, actor, capture, repo)
			source := &fakeReadModelSource{drain: &domain.WorkerDrainStatus{WorkerPubKey: workerPubkey, SchedulingState: tc.target}}
			reactor.workerReadModelPublisher = newTestWorkerReadModelPublisher(capture, source)
			p, statuses := d70Processor(t, "worker", actor, NewWorkerIntentHandler(reactor))
			intent := d70Intent("worker", tc.op, "worker:"+workerPubkey, actor, map[string]interface{}{"worker_pubkey": workerPubkey, "scheduling_state": string(tc.target), "labels": map[string]interface{}{}})
			assertD70AcceptedReplayAndUnauthorized(t, p, statuses, intent, func() int { return len(capture.events) })
			worker, err := repo.GetByPubKey(context.Background(), workerPubkey)
			require.NoError(t, err)
			require.Equal(t, tc.target, worker.SchedulingState)
			require.GreaterOrEqual(t, len(capture.events), 2, "worker state and read model must publish")
		})
	}
	t.Run("labels-update", func(t *testing.T) {
		capture := &captureNostrPublisher{published: 1}
		repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker", Status: domain.WorkerStatusOnline})
		p, statuses := d70Processor(t, "worker", actor, NewWorkerIntentHandler(newWorkerHandlerTestReactor(t, actor, capture, repo)))
		intent := d70Intent("worker", "labels-update", "worker:"+workerPubkey, actor, map[string]interface{}{"worker_pubkey": workerPubkey, "labels": map[string]interface{}{"region": "west"}, "scheduling_state": "active"})
		assertD70AcceptedReplayAndUnauthorized(t, p, statuses, intent, func() int { return len(capture.events) })
		worker, _ := repo.GetByPubKey(context.Background(), workerPubkey)
		require.Equal(t, "west", worker.Labels["region"])
	})
	t.Run("cleanup", func(t *testing.T) {
		capture := &captureNostrPublisher{published: 1}
		repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker", Status: domain.WorkerStatusOnline, LastAdvertisementAt: time.Now().UTC(), Software: []domain.WorkerSoftware{{Name: "bash"}, {Name: "docker"}}, Pressure: &domain.WorkerPressureAssessment{CapacityClass: domain.WorkerCapacityOpen, OverallLevel: domain.WorkerPressureNominal}})
		release := make(chan struct{})
		t.Cleanup(func() { close(release) })
		loom := &controlplaneCleanupLoomFake{releasePoll: release}
		orchestrator := service.NewWorkerCleanupOrchestrator(repo, nil, loom, &events.NoopPublisher{}, service.WorkerCleanupConfig{RequiredSoftware: []string{"bash", "docker"}}, zap.NewNop())
		reactor := newWorkerHandlerTestReactor(t, actor, capture, repo)
		reactor.workerCleanupOrchestrator = orchestrator
		p, statuses := d70Processor(t, "worker", actor, NewWorkerIntentHandler(reactor))
		intent := d70Intent("worker", "cleanup", "worker:"+workerPubkey, actor, map[string]interface{}{"worker_pubkey": workerPubkey, "cleanup_mode": service.CleanupModeReclaimableOnly, "scheduling_state": "active", "labels": map[string]interface{}{}})
		assertD70AcceptedReplayAndUnauthorized(t, p, statuses, intent, func() int { return len(capture.events) })
	})
}

func TestD70WorkerLatestIntentReconcilesMissedSchedulingAndLabels(t *testing.T) {
	actor := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	workerPubkey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	capture := &captureNostrPublisher{published: 1}
	repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker", SchedulingState: domain.WorkerSchedulingActive})
	p, statuses := d70Processor(t, "worker", actor, NewWorkerIntentHandler(newWorkerHandlerTestReactor(t, actor, capture, repo)))
	// Only the newest replaceable intent is retained after offline catch-up.
	// A labels-update still carries the desired cordoned scheduling state.
	intent := d70Intent("worker", "labels-update", "worker:"+workerPubkey, actor,
		map[string]interface{}{"worker_pubkey": workerPubkey, "scheduling_state": "cordoned", "labels": map[string]interface{}{"role": "inference"}})
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	worker, err := repo.GetByPubKey(context.Background(), workerPubkey)
	require.NoError(t, err)
	require.Equal(t, domain.WorkerSchedulingCordoned, worker.SchedulingState)
	require.Equal(t, "inference", worker.Labels["role"])
	require.Len(t, capture.events, 1)
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
}

func TestD70WorkerIntentConflictAndUnsupported(t *testing.T) {
	actor := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	workerPubkey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	capture := &captureNostrPublisher{published: 1}
	repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker", SchedulingState: domain.WorkerSchedulingActive, UpdatedAt: time.Now().UTC()})
	p, statuses := d70Processor(t, "worker", actor, NewWorkerIntentHandler(newWorkerHandlerTestReactor(t, actor, capture, repo)))
	stale := time.Now().Add(-time.Hour).UTC()
	intent := d70Intent("worker", "cordon", "worker:"+workerPubkey, actor, map[string]interface{}{"worker_pubkey": workerPubkey, "scheduling_state": "cordoned", "labels": map[string]interface{}{}})
	intent.ExpectedUpdatedAt = &stale
	require.Error(t, p.ProcessInProcess(context.Background(), intent))
	require.Equal(t, "conflict", tagValueNostr(statuses.events[0].Tags, "status"))
	require.Empty(t, capture.events)
	unsupported := d70Intent("worker", "disable", "worker:"+workerPubkey, actor, map[string]interface{}{"worker_pubkey": workerPubkey})
	require.ErrorContains(t, p.ProcessInProcess(context.Background(), unsupported), "unsupported op: worker disable")
	require.Equal(t, "rejected", tagValueNostr(statuses.events[1].Tags, "status"))
	worker, err := repo.GetByPubKey(context.Background(), workerPubkey)
	require.NoError(t, err)
	matching := d70Intent("worker", "cordon", "worker:"+workerPubkey, actor,
		map[string]interface{}{"worker_pubkey": workerPubkey, "scheduling_state": "cordoned", "labels": map[string]interface{}{},
			"expected_updated_at": worker.UpdatedAt.Format(time.RFC3339Nano)})
	require.NoError(t, p.ProcessInProcess(context.Background(), matching))
	require.Equal(t, "accepted", tagValueNostr(statuses.events[2].Tags, "status"))
	require.Len(t, capture.events, 1)
}

func TestD70IntentContentRoundTrip(t *testing.T) {
	// JSON maps are what ParseIntent and ContextVM dual dispatch pass to handlers.
	content := map[string]interface{}{"worker_pubkey": strings.Repeat("a", 64), "labels": map[string]interface{}{"region": "west"}}
	encoded, err := json.Marshal(content)
	require.NoError(t, err)
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	labels, err := workerIntentLabels(decoded)
	require.NoError(t, err)
	require.Equal(t, "west", labels["region"])
}

func TestD70DNSContextVMDualDispatchUsesIntentPipeline(t *testing.T) {
	actor := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	op := &recordingDNSPersistentOperator{recordingDNSOperator: &recordingDNSOperator{zones: map[string]bool{}, backends: map[string]bool{"primary": true}}}
	canonical := &d70DNSCanonical{}
	p, statuses := d70Processor(t, "dns", actor, NewDNSIntentHandler(op, canonical))
	h := dnsContextVMHandlers{operator: op, enabled: true, intentProcessor: p}
	request := ContextVMRequest{Event: &nostr.Event{ID: testNostrID("d70-dns-contextvm"), PubKey: testNostrPubKeyFromHex(t, actor)}, ProgressToken: "d70-dns-zone", RPC: ContextVMJSONRPCRequest{Params: json.RawMessage(`{"name":"dual.example","visibility":"internal","backend_ref":"primary","ttl":60}`)}}
	result, err := h.zoneCreate(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.(map[string]any)["status"])
	require.Len(t, op.zonesCreated, 1)
	require.Equal(t, 1, canonical.zones)
	require.Len(t, statuses.events, 1)
	_, err = h.zoneCreate(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, op.zonesCreated, 1)
	require.Equal(t, 1, canonical.zones)
}

func TestD70WorkerContextVMDualDispatchUsesIntentPipeline(t *testing.T) {
	actor := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	workerPubkey := testNostrPubKeyHexFromPrivateKey(t, nostr.Generate().Hex())
	capture := &captureNostrPublisher{published: 1}
	repo := newMemoryWorkerRepo(domain.Worker{PubKey: workerPubkey, Name: "worker", SchedulingState: domain.WorkerSchedulingActive})
	reactor := newWorkerHandlerTestReactor(t, actor, capture, repo)
	p, statuses := d70Processor(t, "worker", actor, NewWorkerIntentHandler(reactor))
	h := workerContextVMHandlers{intentProcessor: p}
	request := ContextVMRequest{Event: &nostr.Event{ID: testNostrID("d70-worker-contextvm"), PubKey: testNostrPubKeyFromHex(t, actor)}, ProgressToken: "d70-worker-cordon", RPC: ContextVMJSONRPCRequest{Params: json.RawMessage(`{"worker_pubkey":"` + workerPubkey + `"}`)}}
	result, err := h.cordon(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.(map[string]any)["status"])
	worker, err := repo.GetByPubKey(context.Background(), workerPubkey)
	require.NoError(t, err)
	require.Equal(t, domain.WorkerSchedulingCordoned, worker.SchedulingState)
	require.Len(t, capture.events, 1, "dual dispatch must not publish a second worker command")
	require.Len(t, statuses.events, 1)
	_, err = h.cordon(context.Background(), request)
	require.NoError(t, err)
	require.Len(t, capture.events, 1)

	// The three reactor-owned ContextVM methods use the same processor path.
	reactor.intentProcessor = p
	request.ProgressToken = "d70-worker-uncordon"
	request.Event.ID = testNostrID("d70-worker-uncordon")
	result, err = reactor.handleWorkerUncordonRequest(context.Background(), request)
	require.NoError(t, err)
	require.Equal(t, "succeeded", result.(map[string]any)["status"])
	worker, err = repo.GetByPubKey(context.Background(), workerPubkey)
	require.NoError(t, err)
	require.Equal(t, domain.WorkerSchedulingActive, worker.SchedulingState)
	require.Len(t, capture.events, 2)
	require.Len(t, statuses.events, 2)
}
