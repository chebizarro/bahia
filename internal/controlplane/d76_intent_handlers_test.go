package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/service"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

func d76Processor(t *testing.T, name, actor string, handler DomainHandler) (*IntentProcessor, *statusCollector) {
	t.Helper()
	statuses := &statusCollector{}
	trust := NewTrustSet([]string{"known-denied"}, zap.NewNop(),
		WithBootstrapOwners(map[string]string{testOrgID().String(): actor}))
	p := NewIntentProcessor(trust, openTestStore(t),
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{name: true}}, zap.NewNop())
	p.RegisterHandler(name, handler)
	return p, statuses
}

func TestD76ArtifactRegisterIntentAcceptedReplayUnauthorized(t *testing.T) {
	org, serviceID, buildID, artifactID := testOrgID(), uuid.New(), uuid.New(), uuid.New()
	registry := &fakeEncryptedRegistryMutations{}
	handler := NewArtifactIntentHandler(registry, &testServiceRepo{service: &domain.Service{ID: serviceID, OrgID: org}})
	p, statuses := d76Processor(t, "artifact", testPubkey, handler)
	intent := d70Intent("artifact", "register", "artifact:"+artifactID.String(), testPubkey, map[string]any{
		"id": artifactID.String(), "build_id": buildID.String(), "service_id": serviceID.String(),
		"image_repo": "registry.example/api", "image_tag": "v1",
		"image_digest": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	})
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.Len(t, registry.artifacts, 1)
	require.Equal(t, artifactID, registry.artifacts[0].ID)
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.Len(t, registry.artifacts, 1)
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-denied"
	require.ErrorContains(t, p.ProcessInProcess(context.Background(), &denied), "insufficient permission")
	require.Len(t, registry.artifacts, 1)
}

func TestD76ObservedArtifactIntentAcceptedReplayUnauthorized(t *testing.T) {
	org, serviceID, environmentID := testOrgID(), uuid.New(), uuid.New()
	digest := "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	registry := &fakeEncryptedRegistryMutations{environments: map[uuid.UUID]*domain.Environment{
		environmentID: {ID: environmentID, OrgID: org},
	}}
	handler := NewArtifactIntentHandler(registry, &testServiceRepo{service: &domain.Service{ID: serviceID, OrgID: org}})
	p, statuses := d76Processor(t, "artifact", testPubkey, handler)
	intent := d70Intent("artifact", "import-observed", artifactImportCoordinate(serviceID, environmentID, digest), testPubkey, map[string]any{
		"service_id": serviceID.String(), "environment_id": environmentID.String(),
		"image_repo": "registry.example/api", "image_tag": "v1", "image_digest": digest,
	})
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.Len(t, registry.importCalls, 1)
	require.Equal(t, testPubkey, registry.importCalls[0].RequestedBy)
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.Len(t, registry.importCalls, 1)
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-denied"
	require.ErrorContains(t, p.ProcessInProcess(context.Background(), &denied), "insufficient permission")
	require.Len(t, registry.importCalls, 1)
}

func TestD76AdoptionImportIntentAcceptedReplayUnauthorized(t *testing.T) {
	adoption := &stubAdoptionOperatorService{importResp: []service.AdoptionImportResult{{TargetName: "prod", Status: "imported"}}}
	p, statuses := d76Processor(t, "adoption", testPubkey, NewAdoptionIntentHandler(adoption, []string{testPubkey}))
	intent := d70Intent("adoption", "import", "adoption:"+testOrgID().String(), testPubkey, map[string]any{
		"org_id":     testOrgID().String(),
		"targets":    []any{map[string]any{"name": "prod", "endpoint_ref": "docker-prod"}},
		"import_all": true,
	})
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.True(t, adoption.importCalled)
	require.Equal(t, testOrgID(), adoption.importReq.OrgID)
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	adoption.importCalled = false
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.False(t, adoption.importCalled)
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-denied"
	require.ErrorContains(t, p.ProcessInProcess(context.Background(), &denied), "authorized adoption list")
	require.False(t, adoption.importCalled)
}

func TestD76DNSDriftRemediateIntentAcceptedReplayUnauthorized(t *testing.T) {
	operator := &recordingDNSOperator{}
	p, statuses := d70Processor(t, "dns", testPubkey, NewDNSIntentHandler(operator, nil))
	intent := d70Intent("dns", "drift-remediate", "dns-remediate:prod.example", testPubkey,
		map[string]any{"zone": "prod.example"})
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.Equal(t, []string{"prod.example"}, operator.reconciled)
	require.Equal(t, "accepted", tagValueNostr(statuses.events[0].Tags, "status"))
	var status map[string]any
	require.NoError(t, json.Unmarshal([]byte(statuses.events[0].Content), &status))
	require.Equal(t, "prod.example", status["data"].(map[string]any)["zone"])
	require.NoError(t, p.ProcessInProcess(context.Background(), intent))
	require.Len(t, operator.reconciled, 1)
	denied := *intent
	denied.IntentID = uuid.NewString()
	denied.Actor = "known-non-fleet-principal"
	require.ErrorContains(t, p.ProcessInProcess(context.Background(), &denied), "insufficient permission")
	require.Len(t, operator.reconciled, 1)
}

func TestD76RouteAttachIntentDualDispatchReplayConflictUnauthorized(t *testing.T) {
	f := newRouteAttachFixture(t, false, false)
	request := f.request(t, f.serviceID, validRouteAttachRequest())
	actor := request.Event.PubKey.Hex()
	svc, err := f.handlers.registry.GetService(context.Background(), f.serviceID)
	require.NoError(t, err)
	cfg := EncryptedServiceHandlersConfig{
		Registry: f.handlers.registry, Policy: f.handlers.policy, PublicRoutes: f.handlers.publicRoutes,
		Services: f.handlers.authorizer.services.(*testServiceRepo), DeploymentUnits: f.unitRepo,
		Logger: zap.NewNop(),
	}
	handler := NewDeploymentIntentHandler(cfg, nil)
	statuses := &statusCollector{}
	trust := NewTrustSet([]string{"known-denied"}, zap.NewNop(),
		WithBootstrapOwners(map[string]string{svc.OrgID.String(): actor}))
	p := NewIntentProcessor(trust, openTestStore(t),
		NewIntentStatusPublisher(statuses.publish, &testSigner{}, zap.NewNop()),
		IntentProcessorConfig{EnabledDomains: map[string]bool{"deployment": true}}, zap.NewNop())
	p.RegisterHandler("deployment", handler)
	before := len(f.intentRepo.intents)
	content := map[string]any{}
	require.NoError(t, json.Unmarshal(request.RPC.Params, &content))
	makeIntent := func(actor string) *Intent {
		return &Intent{Domain: "deployment", Op: "route-attach", OrgID: svc.OrgID,
			IntentID: uuid.NewString(), Coordinate: "deployment-route:" + f.serviceID.String() + ":" + f.environmentID.String(),
			Content: content, Actor: actor}
	}
	denied := makeIntent("known-denied")
	require.ErrorContains(t, p.ProcessInProcess(context.Background(), denied), "insufficient permission")
	require.Len(t, f.intentRepo.intents, before)
	stale := makeIntent(actor)
	stale.Content = map[string]any{}
	for key, value := range content {
		stale.Content[key] = value
	}
	stale.Content["expected_updated_at"] = "2020-01-01T00:00:00Z"
	require.Error(t, p.ProcessInProcess(context.Background(), stale))
	require.Equal(t, "conflict", tagValueNostr(statuses.events[len(statuses.events)-1].Tags, "status"))
	require.Len(t, f.intentRepo.intents, before)
	currentRevision := f.current.UpdatedAt.UTC().Format(time.RFC3339Nano)
	accepted := makeIntent(actor)
	accepted.Content = map[string]any{}
	for key, value := range content {
		accepted.Content[key] = value
	}
	accepted.Content["expected_updated_at"] = currentRevision
	accepted.Content["intent_id"] = accepted.IntentID
	require.NoError(t, p.ProcessInProcess(context.Background(), accepted))
	require.Len(t, f.intentRepo.intents, before+1)
}

func TestD76DeploymentPreviewIntentPublishesBoundedPlanAndReplays(t *testing.T) {
	orgID, serviceID, environmentID, artifactID, unitID := testOrgID(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	managed := &domain.ManagedRuntimeConfig{
		SchemaVersion: domain.ManagedRuntimeConfigSchemaVersion, ServiceName: "api",
		Ports: []string{"127.0.0.1:18080:8080"}, RestartPolicy: "unless-stopped",
		Command: []string{strings.Repeat("x", 3000), strings.Repeat("y", 3000), strings.Repeat("z", 3000), strings.Repeat("w", 3000), strings.Repeat("v", 3000)},
	}
	svcRepo := &testServiceRepo{service: &domain.Service{
		ID: serviceID, OrgID: orgID, Name: "api", RuntimeType: domain.RuntimeTypeCompose,
		RuntimeConfig: &domain.ServiceRuntimeConfig{Managed: managed},
	}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{
		ID: environmentID, OrgID: orgID, Name: "prod",
		RuntimeConfig:  map[string]any{"type": "docker", "host_alias": "prod", "management_mode": "direct_runtime"},
		DeployStrategy: domain.DeployStrategyReplace,
	}}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{
		ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example/api", ImageTag: "v1",
		ImageDigest: "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	registry := service.NewRegistryService(
		svcRepo, envRepo, &testBuildRepo{}, artifactRepo, intentRepo,
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}, &testObservationRepo{},
		stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
	)
	unitRepo := &routeAttachDeploymentUnitRepo{unit: &domain.DeploymentUnit{
		ID: unitID, EnvironmentID: environmentID, Key: "prod-compose", RuntimeType: domain.RuntimeTypeCompose,
		OwnershipMode: domain.OwnershipModeBahiaManaged, ReconcileMode: domain.ReconcileModeAutoApply,
	}}
	lifecycle := service.NewRuntimeLifecycleService(
		registry, svcRepo, envRepo, artifactRepo, stateRepo, nil, &events.NoopPublisher{}, zap.NewNop(),
		service.WithRuntimeLifecycleDeploymentUnits(unitRepo),
	)
	cfg := EncryptedServiceHandlersConfig{
		Registry: registry, RuntimeLifecycle: lifecycle,
		Policy:   service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop()),
		Services: svcRepo, DeploymentUnits: unitRepo, RBAC: encryptedAdminRBAC(t, orgID), Logger: zap.NewNop(),
	}
	requestEvent := makeContextVMEvent(t, testRequesterKey, "{}")
	actor := requestEvent.PubKey.Hex()
	p, statuses := d76Processor(t, "deployment", actor, NewDeploymentIntentHandler(cfg, nil))
	params, err := json.Marshal(dto.ServiceDeployPreviewRequest{
		ServiceID: serviceID, EnvironmentID: environmentID, DeploymentUnitID: &unitID,
		ArtifactID: artifactID, ManagedRuntimeConfig: managed,
	})
	require.NoError(t, err)
	content := map[string]any{}
	require.NoError(t, json.Unmarshal(params, &content))
	preview := &Intent{Event: requestEvent, Domain: "deployment", Op: "preview", OrgID: orgID,
		IntentID: requestEvent.ID.Hex(), Coordinate: "deployment-preview:" + serviceID.String() + ":" + environmentID.String(),
		Content: content, Actor: actor}
	require.NoError(t, p.ProcessInProcess(context.Background(), preview))
	result := preview.Result
	require.NotEmpty(t, result["desired_state_hash"])
	require.Empty(t, intentRepo.intents, "preview must not create a deployment")
	require.Len(t, statuses.events, 1)
	var status map[string]any
	require.NoError(t, json.Unmarshal([]byte(statuses.events[0].Content), &status))
	require.NotNil(t, status["data"].(map[string]any)["desired_state_summary"])
	require.Equal(t, true, status["data"].(map[string]any)["plan_truncated"])
	require.Less(t, len(statuses.events[0].Content), 14*1024)
	require.NotContains(t, statuses.events[0].Content, strings.Repeat("x", 100))
	require.NotContains(t, status["data"].(map[string]any), "desired_state")
	require.Nil(t, p.ProcessedIntent(requestEvent.ID.Hex()).Result, "full preview must not be persisted in the plaintext marker")
	require.NoError(t, p.ProcessInProcess(context.Background(), preview))
	require.Len(t, statuses.events, 1)
	denied := &Intent{Domain: "deployment", Op: "preview", OrgID: orgID,
		IntentID: uuid.NewString(), Coordinate: "deployment-preview:" + serviceID.String() + ":" + environmentID.String(),
		Content: content, Actor: "known-denied"}
	require.ErrorContains(t, p.ProcessInProcess(context.Background(), denied), "insufficient permission")
	require.Empty(t, intentRepo.intents)
	relayContent := map[string]any{}
	for key, value := range content {
		relayContent[key] = value
	}
	relayID := uuid.NewString()
	relayContent["intent_id"] = relayID
	relay := &Intent{Domain: "deployment", Op: "preview", OrgID: orgID,
		IntentID: relayID, Coordinate: denied.Coordinate, Content: relayContent, Actor: actor}
	require.NoError(t, p.ProcessInProcess(context.Background(), relay))
	require.NotEmpty(t, relay.Result["desired_state_hash"])
	require.Empty(t, intentRepo.intents)
}
