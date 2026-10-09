package controlplane

import (
	"context"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type llmDeploymentIntentRegistryTest struct {
	intents                                   map[uuid.UUID]*domain.LLMDeploymentIntent
	creates, rollbacks, approvals, rejections int
}

func (r *llmDeploymentIntentRegistryTest) CreateRoute(context.Context, *domain.LLMRoute) error {
	return nil
}
func (r *llmDeploymentIntentRegistryTest) GetRoute(_ context.Context, id uuid.UUID) (*domain.LLMRoute, error) {
	return &domain.LLMRoute{ID: id, UpdatedAt: time.Unix(1790985600, 0).UTC()}, nil
}
func (r *llmDeploymentIntentRegistryTest) UpdateRoute(context.Context, *domain.LLMRoute) error {
	return nil
}
func (r *llmDeploymentIntentRegistryTest) CreateRelease(context.Context, *domain.LLMRelease) error {
	return nil
}
func (r *llmDeploymentIntentRegistryTest) CreateDeploymentIntent(_ context.Context, intent *domain.LLMDeploymentIntent) error {
	r.creates++
	return nil
}
func (r *llmDeploymentIntentRegistryTest) GetDeploymentIntent(_ context.Context, id uuid.UUID) (*domain.LLMDeploymentIntent, error) {
	return r.intents[id], nil
}
func (r *llmDeploymentIntentRegistryTest) ApproveDeploymentIntent(context.Context, uuid.UUID) error {
	r.approvals++
	return nil
}
func (r *llmDeploymentIntentRegistryTest) RejectDeploymentIntent(context.Context, uuid.UUID) error {
	r.rejections++
	return nil
}
func (r *llmDeploymentIntentRegistryTest) RollbackWithMetadata(context.Context, uuid.UUID, uuid.UUID, string, map[string]any) (*domain.LLMDeploymentIntent, error) {
	r.rollbacks++
	return &domain.LLMDeploymentIntent{}, nil
}

func TestLLMDeploymentLifecycleRemainsPausedOnReplay(t *testing.T) {
	for _, tc := range []struct {
		op      string
		content map[string]any
	}{
		{"deploy", map[string]any{"route_id": uuid.NewString(), "environment_id": uuid.NewString(), "release_id": uuid.NewString()}},
		{"rollback", map[string]any{"route_id": uuid.NewString(), "environment_id": uuid.NewString()}},
		{"approve", map[string]any{"deployment_intent_id": uuid.NewString()}},
		{"reject", map[string]any{"deployment_intent_id": uuid.NewString()}},
	} {
		t.Run(tc.op, func(t *testing.T) {
			actor := nostr.Generate()
			store := openTestStore(t)
			registry := &llmDeploymentIntentRegistryTest{}
			statuses := &statusCollector{}
			processor := NewIntentProcessor(NewTrustSet([]string{actor.Public().Hex()}, zap.NewNop()), store,
				NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
				IntentProcessorConfig{EnabledDomains: map[string]bool{"llm": true}}, zap.NewNop())
			processor.RegisterHandler("llm", NewLLMRouteIntentHandler(LLMRouteIntentHandlerConfig{
				Routes: registry, DeploymentUnavailableReason: "canonical executor unavailable", Logger: zap.NewNop(),
			}))
			coordinate := tc.content["deployment_intent_id"]
			if coordinate == nil {
				coordinate = tc.content["route_id"].(string) + ":" + tc.content["environment_id"].(string)
			}
			request := signedLLMLifecycleIntent(t, actor, tc.op, coordinate.(string), tc.content, time.Now().Add(-time.Minute))
			_, err := store.SaveEvent(*request.Event)
			require.NoError(t, err)
			for range 2 {
				require.ErrorContains(t, processor.ProcessInProcess(t.Context(), request), "LLM deployment paused")
			}
			require.Len(t, statuses.events, 2)
			for _, status := range statuses.events {
				require.Equal(t, "rejected", tagValueNostr(status.Tags, "status"))
			}
			require.Zero(t, registry.creates+registry.rollbacks+registry.approvals+registry.rejections)
		})
	}
}

type runtimeIntentResourcesTest struct {
	service     *domain.Service
	environment *domain.Environment
}

func (r runtimeIntentResourcesTest) GetService(context.Context, uuid.UUID) (*domain.Service, error) {
	return r.service, nil
}
func (r runtimeIntentResourcesTest) GetEnvironment(context.Context, uuid.UUID) (*domain.Environment, error) {
	return r.environment, nil
}

type runtimeIntentLifecycleTest struct{ deploys, restarts, stops int }

func (r *runtimeIntentLifecycleTest) DeployWithStatus(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID, service.DeployStatusCallback) (*domain.RuntimeObservation, error) {
	r.deploys++
	return &domain.RuntimeObservation{}, nil
}
func (r *runtimeIntentLifecycleTest) Restart(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error) {
	r.restarts++
	return &domain.RuntimeObservation{}, nil
}
func (r *runtimeIntentLifecycleTest) Stop(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error) {
	r.stops++
	return &domain.RuntimeObservation{}, nil
}

func TestRuntimeIntentOperations(t *testing.T) {
	ctx := context.Background()
	serviceID, environmentID := uuid.New(), uuid.New()
	for _, op := range []string{"deploy", "restart", "stop"} {
		t.Run(op, func(t *testing.T) {
			lifecycle := &runtimeIntentLifecycleTest{}
			handler := &DeploymentIntentHandler{
				resources: runtimeIntentResourcesTest{service: &domain.Service{ID: serviceID, OrgID: testOrgID(), UpdatedAt: time.Unix(1790985600, 0).UTC()}, environment: &domain.Environment{ID: environmentID, OrgID: testOrgID()}},
				runtime:   lifecycle,
			}
			statuses := &statusCollector{}
			proc := NewIntentProcessor(NewTrustSet([]string{"0000000000000000000000000000000000000000000000000000000000000001"}, zap.NewNop(), WithBootstrapOwners(map[string]string{testOrgID().String(): testPubkey})), openTestStore(t), NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()), IntentProcessorConfig{EnabledDomains: map[string]bool{"runtime": true}}, zap.NewNop())
			proc.RegisterHandler("runtime", handler)
			content := map[string]any{"service_id": serviceID.String(), "environment_id": environmentID.String()}
			intent := &Intent{Domain: "runtime", Op: op, OrgID: testOrgID(), Actor: testPubkey, IntentID: "runtime-" + op, Coordinate: serviceID.String() + ":" + environmentID.String(), Content: content}
			require.NoError(t, proc.ProcessInProcess(ctx, intent))
			require.Len(t, statuses.events, 1)
			require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
			require.NoError(t, proc.ProcessInProcess(ctx, intent))
			require.Equal(t, 1, lifecycle.deploys+lifecycle.restarts+lifecycle.stops)
			denied := *intent
			denied.IntentID = "denied-" + op
			denied.Actor = "0000000000000000000000000000000000000000000000000000000000000001"
			require.Error(t, proc.ProcessInProcess(ctx, &denied))
			require.Equal(t, 1, lifecycle.deploys+lifecycle.restarts+lifecycle.stops)
			require.Len(t, statuses.events, 2)
			require.Equal(t, "rejected", tagValueNostr(statuses.events[1].Tags, "status"))
			stale := *intent
			stale.IntentID = "stale-runtime-" + op
			revision := time.Unix(1790985500, 0).UTC()
			stale.ExpectedUpdatedAt = &revision
			require.Error(t, proc.ProcessInProcess(ctx, &stale))
			require.Equal(t, 1, lifecycle.deploys+lifecycle.restarts+lifecycle.stops)
			require.Equal(t, "conflict", tagValueNostr(statuses.events[2].Tags, "status"))
			matching := *intent
			matching.IntentID = "matching-runtime-" + op
			matching.Content = map[string]any{"service_id": serviceID.String(), "environment_id": environmentID.String(),
				"expected_updated_at": time.Unix(1790985600, 0).UTC().Format(time.RFC3339Nano)}
			require.NoError(t, proc.ProcessInProcess(ctx, &matching))
			require.Equal(t, "accepted", tagValueNostr(statuses.events[3].Tags, "status"))
			require.Equal(t, 2, lifecycle.deploys+lifecycle.restarts+lifecycle.stops)
		})
	}
}

func TestDeploymentIntentHandlerPermissions(t *testing.T) {
	handler := &DeploymentIntentHandler{}
	for _, tc := range []struct {
		op         string
		permission domain.Permission
	}{
		{"create", domain.PermWriteDeployments},
		{"rollback", domain.PermWriteDeployments},
		{"deploy", domain.PermWriteDeployments},
		{"restart", domain.PermWriteDeployments},
		{"stop", domain.PermWriteDeployments},
		{"approve", domain.PermApproveDeployments},
		{"reject", domain.PermApproveDeployments},
	} {
		require.Equal(t, tc.permission, handler.PermissionFor(tc.op), tc.op)
	}
}

type deploymentCanonicalRecorder struct{ intents int }

func (*deploymentCanonicalRecorder) PublishBuildRegistry(context.Context, *domain.Build, bool) error {
	return nil
}
func (*deploymentCanonicalRecorder) PublishArtifactRegistry(context.Context, *domain.Artifact, bool) error {
	return nil
}
func (r *deploymentCanonicalRecorder) PublishDeploymentIntentRegistry(context.Context, *domain.DeploymentIntent, bool) error {
	r.intents++
	return nil
}
func (*deploymentCanonicalRecorder) PublishDeploymentRunRegistry(context.Context, *domain.DeploymentRun, bool) error {
	return nil
}

func TestDeploymentDecisionIntentPublishesCanonicalOnce(t *testing.T) {
	for _, op := range []string{"approve", "reject"} {
		t.Run(op, func(t *testing.T) {
			ctx := context.Background()
			signedEvent := makeContextVMEvent(t, testRequesterKey, `{}`)
			actor := signedEvent.PubKey.Hex()
			fixture := newRouteAttachFixture(t, false, false)
			svc, err := fixture.handlers.registry.GetService(ctx, fixture.serviceID)
			require.NoError(t, err)
			targetID := uuid.New()
			revision := time.Unix(1790985600, 0).UTC()
			target := &domain.DeploymentIntent{
				ID: targetID, ServiceID: fixture.serviceID, EnvironmentID: fixture.environmentID,
				DeploymentUnitID: &fixture.unitID, ArtifactID: fixture.current.ArtifactID,
				RequestedBy: actor, SourceKind: domain.SourceKindManual,
				ApprovalStatus: domain.ApprovalStatusPending, Status: domain.IntentStatusPending,
				DesiredState: fixture.current.DesiredState, DesiredHash: fixture.current.DesiredHash,
				UpdatedAt: revision,
			}
			fixture.intentRepo.intents[targetID] = target
			canonical := &deploymentCanonicalRecorder{}
			fixture.handlers.registry.SetCPStatePublisher(canonical)
			statuses := &statusCollector{}
			proc := NewIntentProcessor(NewTrustSet(nil, zap.NewNop(), WithBootstrapOwners(map[string]string{svc.OrgID.String(): actor})), openTestStore(t), NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()), IntentProcessorConfig{EnabledDomains: map[string]bool{"deployment": true}}, zap.NewNop())
			proc.RegisterHandler("deployment", &DeploymentIntentHandler{service: fixture.handlers, resources: fixture.handlers.registry})
			intent := &Intent{Domain: "deployment", Op: op, OrgID: svc.OrgID, Actor: actor, Event: signedEvent, IntentID: "decision-" + op, Coordinate: targetID.String(), Content: map[string]any{"deployment_intent_id": targetID.String(), "expected_updated_at": revision.Format(time.RFC3339Nano)}}
			expected := revision
			intent.ExpectedUpdatedAt = &expected
			require.NoError(t, proc.ProcessInProcess(ctx, intent))
			require.Equal(t, 1, canonical.intents)
			require.Len(t, statuses.events, 1)
			require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
			require.NoError(t, proc.ProcessInProcess(ctx, intent))
			require.Equal(t, 1, canonical.intents)
			stale := *intent
			stale.IntentID = "stale-decision-" + op
			staleRevision := revision.Add(-time.Second)
			stale.ExpectedUpdatedAt = &staleRevision
			stale.Content = map[string]any{"deployment_intent_id": targetID.String(), "expected_updated_at": revision.Add(-time.Second).Format(time.RFC3339Nano)}
			require.Error(t, proc.ProcessInProcess(ctx, &stale))
			require.Equal(t, 1, canonical.intents)
			require.Equal(t, "conflict", tagValueNostr(statuses.events[1].Tags, "status"))
		})
	}
}

type deploymentMutationFixture struct {
	handler                                             *DeploymentIntentHandler
	registry                                            *service.RegistryService
	intents                                             *testDeploymentIntentRepo
	runs                                                *testDeploymentRunRepo
	canonical                                           *deploymentCanonicalRecorder
	processor                                           *IntentProcessor
	statuses                                            *statusCollector
	actor                                               string
	event                                               *nostr.Event
	orgID, serviceID, environmentID, unitID, artifactID uuid.UUID
	desiredHash                                         string
}

func newDeploymentMutationFixture(t *testing.T) *deploymentMutationFixture {
	t.Helper()
	ctx := context.Background()
	orgID, serviceID, environmentID, unitID, artifactID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	managed := &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion,
		ServiceName:   "api", Ports: []string{"127.0.0.1:18080:8080"}, RestartPolicy: "unless-stopped",
	}
	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, OrgID: orgID, Name: "api", RuntimeType: domain.RuntimeTypeCompose, RuntimeConfig: &domain.ServiceRuntimeConfig{Managed: managed}}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, OrgID: orgID, Name: "prod"}}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example/api", ImageTag: "v1", ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
	intents := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	runs := &testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}
	registry := service.NewRegistryService(svcRepo, envRepo, &testBuildRepo{}, artifactRepo, intents, runs, &testObservationRepo{}, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop())
	canonical := &deploymentCanonicalRecorder{}
	registry.SetCPStatePublisher(canonical)
	unitRepo := &routeAttachDeploymentUnitRepo{unit: &domain.DeploymentUnit{ID: unitID, EnvironmentID: environmentID, Key: "prod-compose", RuntimeType: domain.RuntimeTypeCompose, ReconcileMode: domain.ReconcileModeAutoApply, OwnershipMode: domain.OwnershipModeBahiaManaged}}
	lifecycle := service.NewRuntimeLifecycleService(registry, svcRepo, envRepo, artifactRepo, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(), service.WithRuntimeLifecycleDeploymentUnits(unitRepo))
	desired, err := lifecycle.BuildDesiredStateSnapshot(ctx, serviceID, environmentID, artifactID, &unitID)
	require.NoError(t, err)
	policy := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	signedEvent := makeContextVMEvent(t, testRequesterKey, `{}`)
	actor := signedEvent.PubKey.Hex()
	handler := &DeploymentIntentHandler{service: &encryptedServiceHandlers{registry: registry, runtimeLifecycle: lifecycle, policy: policy, logger: zap.NewNop()}, resources: registry}
	statuses := &statusCollector{}
	proc := NewIntentProcessor(NewTrustSet([]string{"0000000000000000000000000000000000000000000000000000000000000001"}, zap.NewNop(), WithBootstrapOwners(map[string]string{orgID.String(): actor})), openTestStore(t), NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()), IntentProcessorConfig{EnabledDomains: map[string]bool{"deployment": true}}, zap.NewNop())
	proc.RegisterHandler("deployment", handler)
	return &deploymentMutationFixture{handler: handler, registry: registry, intents: intents, runs: runs, canonical: canonical, processor: proc, statuses: statuses, actor: actor, event: signedEvent, orgID: orgID, serviceID: serviceID, environmentID: environmentID, unitID: unitID, artifactID: artifactID, desiredHash: desired.DesiredHash}
}

func TestDeploymentCreateIntentPublishesCanonicalOnce(t *testing.T) {
	fixture := newDeploymentMutationFixture(t)
	ctx := context.Background()
	intent := &Intent{Event: fixture.event, Domain: "deployment", Op: "create", OrgID: fixture.orgID, Actor: fixture.actor, IntentID: "deployment-create-1", Coordinate: fixture.serviceID.String() + ":" + fixture.environmentID.String(), Content: map[string]any{
		"service_id": fixture.serviceID.String(), "environment_id": fixture.environmentID.String(), "deployment_unit_id": fixture.unitID.String(), "artifact_id": fixture.artifactID.String(), "expected_desired_state_hash": fixture.desiredHash,
	}}
	require.NoError(t, fixture.processor.ProcessInProcess(ctx, intent))
	require.Len(t, fixture.intents.intents, 1)
	require.Equal(t, 1, fixture.canonical.intents)
	require.Len(t, fixture.statuses.events, 1)
	require.Equal(t, "accepted", tagValueNostr(fixture.statuses.events[0].Tags, "status"))
	require.NoError(t, fixture.processor.ProcessInProcess(ctx, intent))
	require.Len(t, fixture.intents.intents, 1)
	require.Equal(t, 1, fixture.canonical.intents)
	denied := *intent
	denied.IntentID = "denied-deployment-create"
	denied.Actor = "0000000000000000000000000000000000000000000000000000000000000001"
	require.Error(t, fixture.processor.ProcessInProcess(ctx, &denied))
	require.Equal(t, 1, fixture.canonical.intents)
	require.Equal(t, "rejected", tagValueNostr(fixture.statuses.events[1].Tags, "status"))
}

func TestDeploymentRollbackIntentFromPriorRunPublishesCanonicalOnce(t *testing.T) {
	fixture := newDeploymentMutationFixture(t)
	ctx := context.Background()
	priorID, supersedesID, runID := uuid.New(), uuid.New(), uuid.New()
	prior := &domain.DeploymentIntent{
		ID: priorID, ServiceID: fixture.serviceID, EnvironmentID: fixture.environmentID,
		DeploymentUnitID: &fixture.unitID, ArtifactID: fixture.artifactID,
		Status: domain.IntentStatusDeployed, CreatedAt: time.Unix(1790985400, 0).UTC(),
	}
	superseded := &domain.DeploymentIntent{
		ID: supersedesID, ServiceID: fixture.serviceID, EnvironmentID: fixture.environmentID,
		DeploymentUnitID: &fixture.unitID, ArtifactID: uuid.New(),
		Status: domain.IntentStatusDeployed, CreatedAt: time.Unix(1790985500, 0).UTC(), UpdatedAt: time.Unix(1790985600, 0).UTC(),
	}
	fixture.intents.intents[priorID] = prior
	fixture.intents.intents[supersedesID] = superseded
	fixture.runs.runs[runID] = &domain.DeploymentRun{ID: runID, DeploymentIntentID: priorID, Status: domain.RunStatusSucceeded}
	intent := &Intent{Event: fixture.event, Domain: "deployment", Op: "rollback", OrgID: fixture.orgID, Actor: fixture.actor, IntentID: "deployment-rollback-1", Coordinate: fixture.serviceID.String() + ":" + fixture.environmentID.String(), Content: map[string]any{
		"service_id": fixture.serviceID.String(), "environment_id": fixture.environmentID.String(), "deployment_unit_id": fixture.unitID.String(), "target_run_id": runID.String(), "supersedes_intent_id": supersedesID.String(),
	}}
	require.NoError(t, fixture.processor.ProcessInProcess(ctx, intent))
	require.Len(t, fixture.intents.intents, 3)
	require.Equal(t, 1, fixture.canonical.intents)
	require.Len(t, fixture.statuses.events, 1)
	require.Equal(t, "accepted", tagValueNostr(fixture.statuses.events[0].Tags, "status"))
	require.NoError(t, fixture.processor.ProcessInProcess(ctx, intent))
	require.Equal(t, 1, fixture.canonical.intents)
	stale := *intent
	stale.IntentID = "stale-deployment-rollback"
	revision := time.Unix(1790985500, 0).UTC()
	stale.ExpectedUpdatedAt = &revision
	require.Error(t, fixture.processor.ProcessInProcess(ctx, &stale))
	require.Equal(t, 1, fixture.canonical.intents)
	require.Equal(t, "conflict", tagValueNostr(fixture.statuses.events[1].Tags, "status"))
	denied := *intent
	denied.IntentID = "denied-deployment-rollback"
	denied.Actor = "0000000000000000000000000000000000000000000000000000000000000001"
	require.Error(t, fixture.processor.ProcessInProcess(ctx, &denied))
	require.Equal(t, 1, fixture.canonical.intents)
	require.Equal(t, "rejected", tagValueNostr(fixture.statuses.events[2].Tags, "status"))
}

func TestLLMDeployIntakeRejectsWhenExecutorPaused(t *testing.T) {
	for _, tc := range []struct{ name, reason string }{{"default", ""}, {"configured", "canonical executor unavailable"}} {
		t.Run(tc.name, func(t *testing.T) {
			actor := nostr.Generate()
			store := openTestStore(t)
			reg := &llmDeploymentIntentRegistryTest{}
			statuses := &statusCollector{}
			proc := NewIntentProcessor(NewTrustSet([]string{actor.Public().Hex()}, zap.NewNop()), store, NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()), IntentProcessorConfig{EnabledDomains: map[string]bool{"llm": true}}, zap.NewNop())
			proc.RegisterHandler("llm", NewLLMRouteIntentHandler(LLMRouteIntentHandlerConfig{Routes: reg, DeploymentUnavailableReason: tc.reason, Logger: zap.NewNop()}))
			routeID, envID := uuid.New(), uuid.New()
			intent := signedLLMLifecycleIntent(t, actor, "deploy", routeID.String()+":"+envID.String(), map[string]any{"route_id": routeID.String(), "environment_id": envID.String(), "release_id": uuid.NewString()}, time.Now().Add(-time.Minute))
			_, err := store.SaveEvent(*intent.Event)
			require.NoError(t, err)
			require.ErrorContains(t, proc.ProcessInProcess(t.Context(), intent), "LLM deployment paused")
			require.Zero(t, reg.creates)
			require.Len(t, statuses.events, 1)
			require.Equal(t, "rejected", tagValueNostr(statuses.events[0].Tags, "status"))
		})
	}
}
