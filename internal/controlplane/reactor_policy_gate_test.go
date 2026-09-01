package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/events"
	"github.com/openagentsinc/bahia/internal/repository"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

func TestHandleDeployRequestInvokesRuntimeLifecycleAndPersistsDesiredState(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	environmentID := uuid.New()
	artifactID := uuid.New()
	obsID := uuid.New()

	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod", Protected: false}}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example.com/api", ImageTag: "v1", ImageDigest: "sha256:abc"}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	runRepo := &testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}

	registry := service.NewRegistryService(
		svcRepo,
		envRepo,
		&testBuildRepo{},
		artifactRepo,
		intentRepo,
		runRepo,
		&testObservationRepo{},
		stateRepo,
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)
	policyService := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	desired := &domain.DesiredServiceSpec{ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID, StableServiceKey: "api", ImageRef: "registry.example.com/api@sha256:abc"}
	desired.ComputeDesiredHash()
	runtimeStub := &stubRuntimeLifecycleOperatorService{
		desiredState: desired,
		deployResp:   &domain.RuntimeObservation{ID: obsID, ServiceID: serviceID, EnvironmentID: environmentID, HealthStatus: domain.HealthStatusHealthy, Source: "direct_runtime"},
	}
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{AuthorizedPubkeys: []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}}, capture, registry, policyService, runtimeStub)

	request := &nostr.Event{
		ID:      testNostrID("deploy-request"),
		PubKey:  testNostrPubKeyFromPrivateKey(t, testRequesterKey),
		Kind:    KindDeployRequest,
		Content: fmt.Sprintf(`{"service_id":"%s","environment_id":"%s","artifact_id":"%s"}`, serviceID, environmentID, artifactID),
	}

	reactor.handleDeployRequest(ctx, request)

	if !runtimeStub.deployCalled {
		t.Fatal("5961 deploy request did not invoke RuntimeLifecycleService.DeployWithStatus")
	}
	if runtimeStub.deployServiceID != serviceID || runtimeStub.deployEnvID != environmentID || runtimeStub.deployArtifact == nil || *runtimeStub.deployArtifact != artifactID {
		t.Fatalf("runtime deploy call mismatch: %#v", runtimeStub)
	}
	if got := len(intentRepo.intents); got != 1 {
		t.Fatalf("deployment intents created = %d, want 1", got)
	}
	var persisted *domain.DeploymentIntent
	for _, intent := range intentRepo.intents {
		persisted = intent
	}
	if persisted.DesiredState == nil || persisted.DesiredHash != desired.DesiredHash {
		t.Fatalf("persisted desired state/hash mismatch: state=%#v hash=%q want %q", persisted.DesiredState, persisted.DesiredHash, desired.DesiredHash)
	}
	if got := len(runRepo.runs); got != 1 {
		t.Fatalf("deployment runs created = %d, want 1", got)
	}
	for _, deploymentRun := range runRepo.runs {
		if deploymentRun.ApplyMetadata["desired_hash"] != desired.DesiredHash {
			t.Fatalf("run apply desired_hash = %#v, want %q", deploymentRun.ApplyMetadata["desired_hash"], desired.DesiredHash)
		}
		if deploymentRun.Status != domain.RunStatusSucceeded {
			t.Fatalf("run status = %q, want %q", deploymentRun.Status, domain.RunStatusSucceeded)
		}
	}
	if len(capture.events) == 0 || capture.events[len(capture.events)-1].Kind != KindContextVMMessage {
		t.Fatalf("expected final ContextVM deployment result, got %#v", capture.events)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, capture.events[len(capture.events)-1].Tags, "desired_hash", desired.DesiredHash)
}

func TestHandleEventDispatchesContextVMServiceDeployRequest(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	environmentID := uuid.New()
	artifactID := uuid.New()

	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod", Protected: false}}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example.com/api", ImageTag: "v1", ImageDigest: "sha256:abc"}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	runRepo := &testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}
	registry := service.NewRegistryService(
		svcRepo,
		envRepo,
		&testBuildRepo{},
		artifactRepo,
		intentRepo,
		runRepo,
		&testObservationRepo{},
		&testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}},
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)
	policyService := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	desired := &domain.DesiredServiceSpec{ServiceID: serviceID, EnvironmentID: environmentID, ArtifactID: artifactID, StableServiceKey: "api", ImageRef: "registry.example.com/api@sha256:abc"}
	desired.ComputeDesiredHash()
	runtimeStub := &stubRuntimeLifecycleOperatorService{
		desiredState: desired,
		deployResp:   &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID, HealthStatus: domain.HealthStatusHealthy, Source: "direct_runtime"},
	}
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{AuthorizedPubkeys: []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}}, capture, registry, policyService, runtimeStub)

	dTag := "service-deploy:contextvm-test"
	event := &nostr.Event{
		Kind:      KindContextVMMessage,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", dTag},
			{"method", ContextVMMethodServiceDeploy},
			{"contextvm", ContextVMWireVersion},
			{"service", serviceID.String()},
			{"environment", environmentID.String()},
			{"artifact", artifactID.String()},
		},
		Content: fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":"service/deploy","params":{"service_id":"%s","environment_id":"%s","artifact_id":"%s","_meta":{"progressToken":%q}}}`, dTag, serviceID, environmentID, artifactID, dTag),
	}
	if err := event.Sign(testNostrSecretKey(t, testRequesterKey)); err != nil {
		t.Fatalf("sign ContextVM deploy event: %v", err)
	}

	reactor.handleEvent(ctx, event)

	deadline := time.Now().Add(2 * time.Second)
	for !runtimeStub.deployCalled && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !runtimeStub.deployCalled {
		t.Fatal("canonical ContextVM service/deploy event did not invoke RuntimeLifecycleService.DeployWithStatus")
	}
	if got := len(intentRepo.intents); got != 1 {
		t.Fatalf("deployment intents created = %d, want 1", got)
	}
	if got := len(runRepo.runs); got != 1 {
		t.Fatalf("deployment runs created = %d, want 1", got)
	}
}

func TestHandleEventContextVMServiceDeployWithDeploymentUnitCreatesIntentForWorkflow(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	environmentID := uuid.New()
	artifactID := uuid.New()
	deploymentUnitID := uuid.New()

	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod", Protected: false}}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageRepo: "registry.example.com/api", ImageTag: "v1", ImageDigest: "sha256:abc"}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	runRepo := &testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}
	registry := service.NewRegistryService(
		svcRepo,
		envRepo,
		&testBuildRepo{},
		artifactRepo,
		intentRepo,
		runRepo,
		&testObservationRepo{},
		stateRepo,
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)
	policyService := service.NewPolicyService(&testPolicyRepo{}, &testSignatureRepo{hasVerifiedSignature: true}, &testSBOMRepo{}, zap.NewNop())
	runtimeStub := &stubRuntimeLifecycleOperatorService{}
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{AuthorizedPubkeys: []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}}, capture, registry, policyService, runtimeStub)

	dTag := "service-deploy:contextvm-unit-test"
	event := &nostr.Event{
		Kind:      KindContextVMMessage,
		CreatedAt: nostr.Now(),
		Tags: nostr.Tags{
			{"d", dTag},
			{"method", ContextVMMethodServiceDeploy},
			{"contextvm", ContextVMWireVersion},
			{"service", serviceID.String()},
			{"environment", environmentID.String()},
			{"artifact", artifactID.String()},
			{"deployment_unit", deploymentUnitID.String()},
		},
		Content: fmt.Sprintf(`{"jsonrpc":"2.0","id":%q,"method":"service/deploy","params":{"service_id":"%s","environment_id":"%s","artifact_id":"%s","deployment_unit_id":"%s","_meta":{"progressToken":%q}}}`, dTag, serviceID, environmentID, artifactID, deploymentUnitID, dTag),
	}
	if err := event.Sign(testNostrSecretKey(t, testRequesterKey)); err != nil {
		t.Fatalf("sign ContextVM deploy event: %v", err)
	}

	reactor.handleEvent(ctx, event)

	deadline := time.Now().Add(2 * time.Second)
	for len(intentRepo.intents) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if runtimeStub.deployCalled {
		t.Fatal("explicit deployment-unit deploy should be handed to the workflow coordinator, not direct runtime")
	}
	if got := len(intentRepo.intents); got != 1 {
		t.Fatalf("deployment intents created = %d, want 1", got)
	}
	var persisted *domain.DeploymentIntent
	for _, intent := range intentRepo.intents {
		persisted = intent
	}
	if persisted.DeploymentUnitID == nil || *persisted.DeploymentUnitID != deploymentUnitID {
		t.Fatalf("deployment intent unit = %v, want %s", persisted.DeploymentUnitID, deploymentUnitID)
	}
	state := stateRepo.states[serviceID.String()+":"+environmentID.String()]
	if state == nil || state.DeploymentUnitID == nil || *state.DeploymentUnitID != deploymentUnitID {
		t.Fatalf("environment service state unit = %v, want %s", state, deploymentUnitID)
	}
	if got := len(runRepo.runs); got != 0 {
		t.Fatalf("direct deployment runs created = %d, want 0", got)
	}
}

func TestHandleEventDispatchesArtifactRegisterRequest(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	buildID := uuid.New()
	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: uuid.New(), Name: "prod"}}
	buildRepo := &testBuildRepo{build: &domain.Build{ID: buildID, ServiceID: serviceID}}
	artifactRepo := &testArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{}}
	registry := service.NewRegistryService(
		svcRepo,
		envRepo,
		buildRepo,
		artifactRepo,
		&testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}},
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}},
		&testObservationRepo{},
		&testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}},
		&service.NoopImageVerifier{},
		&events.NoopPublisher{},
		zap.NewNop(),
	)
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{AuthorizedPubkeys: []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}}, capture, registry, nil)

	event := &nostr.Event{
		Kind:      KindArtifactRegister,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"service", serviceID.String()}, {"build", buildID.String()}, {"digest", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}},
		Content:   fmt.Sprintf(`{"build_id":"%s","service_id":"%s","image_repo":"docker.io/library/busybox","image_tag":"latest","image_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","scan_status":"clean"}`, buildID, serviceID),
	}
	if err := event.Sign(testNostrSecretKey(t, testRequesterKey)); err != nil {
		t.Fatalf("sign artifact register event: %v", err)
	}

	reactor.handleEvent(ctx, event)

	deadline := time.Now().Add(2 * time.Second)
	for len(artifactRepo.artifacts) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := len(artifactRepo.artifacts); got != 1 {
		t.Fatalf("artifacts created = %d, want 1", got)
	}
}

func TestHandleRollbackRequestExecutesSharedDesiredStateDeployPath(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	environmentID := uuid.New()
	previousArtifactID := uuid.New()
	currentArtifactID := uuid.New()
	previousIntentID := uuid.New()
	currentIntentID := uuid.New()
	obsID := uuid.New()

	previousDesired := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        serviceID,
		EnvironmentID:    environmentID,
		ArtifactID:       previousArtifactID,
		StableServiceKey: "api",
		ImageRef:         "registry.example.com/api@sha256:previous",
	}
	previousDesired.ComputeDesiredHash()

	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod", Protected: false}}
	artifactRepo := &testArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
		previousArtifactID: {ID: previousArtifactID, ServiceID: serviceID, ImageRepo: "registry.example.com/api", ImageTag: "v1", ImageDigest: "sha256:previous"},
		currentArtifactID:  {ID: currentArtifactID, ServiceID: serviceID, ImageRepo: "registry.example.com/api", ImageTag: "v2", ImageDigest: "sha256:current"},
	}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		previousIntentID: {
			ID:            previousIntentID,
			ServiceID:     serviceID,
			EnvironmentID: environmentID,
			ArtifactID:    previousArtifactID,
			RequestedBy:   "operator",
			SourceKind:    domain.SourceKindManual,
			Status:        domain.IntentStatusDeployed,
			DesiredState:  previousDesired,
			DesiredHash:   previousDesired.DesiredHash,
			CreatedAt:     time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
		},
		currentIntentID: {
			ID:            currentIntentID,
			ServiceID:     serviceID,
			EnvironmentID: environmentID,
			ArtifactID:    currentArtifactID,
			RequestedBy:   "operator",
			SourceKind:    domain.SourceKindManual,
			Status:        domain.IntentStatusDeployed,
			CreatedAt:     time.Date(2026, 5, 26, 12, 1, 0, 0, time.UTC),
		},
	}}
	runRepo := &testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{
		serviceID.String() + ":" + environmentID.String(): {
			ServiceID:         serviceID,
			EnvironmentID:     environmentID,
			DesiredArtifactID: &currentArtifactID,
			DesiredIntentID:   &currentIntentID,
		},
	}}

	registry := service.NewRegistryService(
		svcRepo,
		envRepo,
		&testBuildRepo{},
		artifactRepo,
		intentRepo,
		runRepo,
		&testObservationRepo{},
		stateRepo,
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)
	runtimeStub := &stubRuntimeLifecycleOperatorService{
		desiredState: previousDesired,
		deployResp:   &domain.RuntimeObservation{ID: obsID, ServiceID: serviceID, EnvironmentID: environmentID, HealthStatus: domain.HealthStatusHealthy, Source: "direct_runtime"},
		emitSteps:    true,
	}
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{AuthorizedPubkeys: []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}}, capture, registry, nil, runtimeStub)

	request := &nostr.Event{
		ID:      testNostrID("rollback-request"),
		PubKey:  testNostrPubKeyFromPrivateKey(t, testRequesterKey),
		Kind:    KindRollbackRequest,
		Content: fmt.Sprintf(`{"service_id":"%s","environment_id":"%s"}`, serviceID, environmentID),
	}

	reactor.handleRollbackRequest(ctx, request)

	if !runtimeStub.deployCalled {
		t.Fatal("rollback request did not execute RuntimeLifecycleService.DeployWithStatus")
	}
	if runtimeStub.deployServiceID != serviceID || runtimeStub.deployEnvID != environmentID || runtimeStub.deployArtifact == nil || *runtimeStub.deployArtifact != previousArtifactID {
		t.Fatalf("rollback deploy call mismatch: %#v", runtimeStub)
	}

	var rollbackIntent *domain.DeploymentIntent
	for _, intent := range intentRepo.intents {
		if intent.SourceKind == domain.SourceKindRollback {
			rollbackIntent = intent
			break
		}
	}
	if rollbackIntent == nil {
		t.Fatal("rollback intent was not persisted")
	}
	if rollbackIntent.ArtifactID != previousArtifactID || rollbackIntent.DesiredHash != previousDesired.DesiredHash || rollbackIntent.DesiredState == nil {
		t.Fatalf("rollback intent did not carry previous desired snapshot/hash: %#v", rollbackIntent)
	}
	if rollbackIntent.Status != domain.IntentStatusDeployed {
		t.Fatalf("rollback intent status = %q, want %q", rollbackIntent.Status, domain.IntentStatusDeployed)
	}

	if got := len(runRepo.runs); got != 1 {
		t.Fatalf("deployment runs created = %d, want 1", got)
	}
	for _, deploymentRun := range runRepo.runs {
		if deploymentRun.Status != domain.RunStatusSucceeded {
			t.Fatalf("rollback run status = %q, want %q", deploymentRun.Status, domain.RunStatusSucceeded)
		}
		if deploymentRun.ApplyMetadata["desired_hash"] != previousDesired.DesiredHash {
			t.Fatalf("rollback run desired_hash = %#v, want %q", deploymentRun.ApplyMetadata["desired_hash"], previousDesired.DesiredHash)
		}
		if deploymentRun.ApplyMetadata["source_kind"] != string(domain.SourceKindRollback) {
			t.Fatalf("rollback run source_kind = %#v", deploymentRun.ApplyMetadata["source_kind"])
		}
	}

	updatedState := stateRepo.states[serviceID.String()+":"+environmentID.String()]
	if updatedState == nil || updatedState.DesiredHash != previousDesired.DesiredHash || updatedState.DesiredRuntimeState == nil {
		t.Fatalf("state did not retain rollback desired metadata: %#v", updatedState)
	}

	steps := map[string]bool{}
	var final *nostr.Event
	for i := range capture.events {
		ev := capture.events[i]
		if ev.Kind == KindNIP38Status {
			for _, tag := range ev.Tags {
				if len(tag) >= 2 && tag[0] == "step" {
					steps[tag[1]] = true
				}
			}
		}
		if ev.Kind == KindContextVMMessage {
			final = &capture.events[i]
		}
	}
	for _, step := range []string{"creating_rollback_intent", "applying_desired_state", string(service.DeployStepBuildingDesiredState), string(service.DeployStepLockingEnvironment), string(service.DeployStepRendering), string(service.DeployStepApplying), string(service.DeployStepObserving), string(service.DeployStepProjecting)} {
		if !steps[step] {
			t.Fatalf("missing rollback status step %q in events %#v", step, capture.events)
		}
	}
	if final == nil {
		t.Fatal("rollback did not publish terminal ContextVM result")
	}
	assertReactorTag(t, final.Tags, "desired_hash", previousDesired.DesiredHash)
	assertNoLegacyStatusResultEvents(t, capture.events)
}

func TestHandleRollbackRequestPersistsFallbackDesiredStateBeforeCompletingRun(t *testing.T) {
	fixture := newRollbackRequestTestFixture(t, false)

	fixture.reactor.handleRollbackRequest(context.Background(), fixture.request)

	if !fixture.runtime.deployCalled {
		t.Fatal("rollback should deploy after building fallback desired state")
	}
	rollbackIntent := fixture.rollbackIntent(t)
	if rollbackIntent.Status != domain.IntentStatusDeployed {
		t.Fatalf("rollback intent status = %q, want %q", rollbackIntent.Status, domain.IntentStatusDeployed)
	}
	if rollbackIntent.DesiredState == nil || rollbackIntent.DesiredHash != fixture.previousDesired.DesiredHash {
		t.Fatalf("rollback intent did not persist fallback desired state/hash: %#v", rollbackIntent)
	}
	updatedState := fixture.state.states[fixture.serviceID.String()+":"+fixture.environmentID.String()]
	if updatedState == nil || updatedState.DesiredRuntimeState == nil || updatedState.DesiredHash != fixture.previousDesired.DesiredHash {
		t.Fatalf("completed rollback run did not retain persisted fallback desired metadata in state: %#v", updatedState)
	}
}

func TestHandleRollbackRequestRecordsFailedRunWhenDesiredStateBuildFails(t *testing.T) {
	fixture := newRollbackRequestTestFixture(t, false)
	fixture.runtime.buildErr = errors.New("desired state unavailable")

	fixture.reactor.handleRollbackRequest(context.Background(), fixture.request)

	if fixture.runtime.deployCalled {
		t.Fatal("rollback should not deploy when desired-state build fails")
	}
	rollbackIntent := fixture.rollbackIntent(t)
	if rollbackIntent.Status != domain.IntentStatusFailed {
		t.Fatalf("rollback intent status = %q, want %q", rollbackIntent.Status, domain.IntentStatusFailed)
	}
	if got := len(fixture.runs.runs); got != 1 {
		t.Fatalf("rollback runs created = %d, want 1", got)
	}
	for _, run := range fixture.runs.runs {
		if run.Status != domain.RunStatusFailed {
			t.Fatalf("rollback preparation run status = %q, want %q", run.Status, domain.RunStatusFailed)
		}
		if run.ApplyMetadata["source_kind"] != string(domain.SourceKindRollback) {
			t.Fatalf("rollback preparation run source_kind = %#v", run.ApplyMetadata["source_kind"])
		}
		if run.Metadata["failure_step"] != "building_desired_state" {
			t.Fatalf("rollback preparation failure_step = %#v", run.Metadata["failure_step"])
		}
	}
	assertRollbackErrorStep(t, fixture.capture.events, "desired_state_error")
}

func TestHandleRollbackRequestRecordsFailedRunWhenDeployFails(t *testing.T) {
	fixture := newRollbackRequestTestFixture(t, true)
	fixture.runtime.deployErr = errors.New("runtime apply failed")

	fixture.reactor.handleRollbackRequest(context.Background(), fixture.request)

	if !fixture.runtime.deployCalled {
		t.Fatal("rollback should attempt deploy before recording deploy failure")
	}
	rollbackIntent := fixture.rollbackIntent(t)
	if rollbackIntent.Status != domain.IntentStatusFailed {
		t.Fatalf("rollback intent status = %q, want %q", rollbackIntent.Status, domain.IntentStatusFailed)
	}
	if got := len(fixture.runs.runs); got != 1 {
		t.Fatalf("rollback runs created = %d, want 1", got)
	}
	for _, run := range fixture.runs.runs {
		if run.Status != domain.RunStatusFailed {
			t.Fatalf("rollback deploy run status = %q, want %q", run.Status, domain.RunStatusFailed)
		}
		if run.ApplyMetadata["desired_hash"] != fixture.previousDesired.DesiredHash {
			t.Fatalf("rollback deploy run desired_hash = %#v, want %q", run.ApplyMetadata["desired_hash"], fixture.previousDesired.DesiredHash)
		}
	}
	assertRollbackErrorStep(t, fixture.capture.events, "rollback_failed")
}

type rollbackRequestTestFixture struct {
	serviceID       uuid.UUID
	environmentID   uuid.UUID
	previousDesired *domain.DesiredServiceSpec
	intents         *testDeploymentIntentRepo
	runs            *testDeploymentRunRepo
	state           *testEnvironmentServiceStateRepo
	runtime         *stubRuntimeLifecycleOperatorService
	capture         *captureNostrPublisher
	reactor         *Reactor
	request         *nostr.Event
}

func newRollbackRequestTestFixture(t *testing.T, includePreviousDesired bool) *rollbackRequestTestFixture {
	t.Helper()
	serviceID := uuid.New()
	environmentID := uuid.New()
	previousArtifactID := uuid.New()
	currentArtifactID := uuid.New()
	previousIntentID := uuid.New()
	currentIntentID := uuid.New()

	previousDesired := &domain.DesiredServiceSpec{
		SchemaVersion:    domain.DesiredStateSchemaVersion,
		ServiceID:        serviceID,
		EnvironmentID:    environmentID,
		ArtifactID:       previousArtifactID,
		StableServiceKey: "api",
		ImageRef:         "registry.example.com/api@sha256:previous",
	}
	previousDesired.ComputeDesiredHash()

	previousIntent := &domain.DeploymentIntent{
		ID:            previousIntentID,
		ServiceID:     serviceID,
		EnvironmentID: environmentID,
		ArtifactID:    previousArtifactID,
		RequestedBy:   "operator",
		SourceKind:    domain.SourceKindManual,
		Status:        domain.IntentStatusDeployed,
		CreatedAt:     time.Date(2026, 5, 26, 12, 0, 0, 0, time.UTC),
	}
	if includePreviousDesired {
		previousIntent.DesiredState = previousDesired
		previousIntent.DesiredHash = previousDesired.DesiredHash
	}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{
		previousIntentID: previousIntent,
		currentIntentID: {
			ID:            currentIntentID,
			ServiceID:     serviceID,
			EnvironmentID: environmentID,
			ArtifactID:    currentArtifactID,
			RequestedBy:   "operator",
			SourceKind:    domain.SourceKindManual,
			Status:        domain.IntentStatusDeployed,
			CreatedAt:     time.Date(2026, 5, 26, 12, 1, 0, 0, time.UTC),
		},
	}}
	runRepo := &testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{
		serviceID.String() + ":" + environmentID.String(): {
			ServiceID:         serviceID,
			EnvironmentID:     environmentID,
			DesiredArtifactID: &currentArtifactID,
			DesiredIntentID:   &currentIntentID,
		},
	}}
	registry := service.NewRegistryService(
		&testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}},
		&testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod", Protected: false}},
		&testBuildRepo{},
		&testArtifactRepo{artifacts: map[uuid.UUID]*domain.Artifact{
			previousArtifactID: {ID: previousArtifactID, ServiceID: serviceID, ImageRepo: "registry.example.com/api", ImageTag: "v1", ImageDigest: "sha256:previous"},
			currentArtifactID:  {ID: currentArtifactID, ServiceID: serviceID, ImageRepo: "registry.example.com/api", ImageTag: "v2", ImageDigest: "sha256:current"},
		}},
		intentRepo,
		runRepo,
		&testObservationRepo{},
		stateRepo,
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)
	runtimeStub := &stubRuntimeLifecycleOperatorService{desiredState: previousDesired, deployResp: &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID, HealthStatus: domain.HealthStatusHealthy, Source: "direct_runtime"}}
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{AuthorizedPubkeys: []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}}, capture, registry, nil, runtimeStub)
	request := &nostr.Event{
		ID:      testNostrID("rollback-request"),
		PubKey:  testNostrPubKeyFromPrivateKey(t, testRequesterKey),
		Kind:    KindRollbackRequest,
		Content: fmt.Sprintf(`{"service_id":"%s","environment_id":"%s"}`, serviceID, environmentID),
	}
	return &rollbackRequestTestFixture{serviceID: serviceID, environmentID: environmentID, previousDesired: previousDesired, intents: intentRepo, runs: runRepo, state: stateRepo, runtime: runtimeStub, capture: capture, reactor: reactor, request: request}
}

func (f *rollbackRequestTestFixture) rollbackIntent(t *testing.T) *domain.DeploymentIntent {
	t.Helper()
	for _, intent := range f.intents.intents {
		if intent.SourceKind == domain.SourceKindRollback {
			return intent
		}
	}
	t.Fatal("rollback intent was not persisted")
	return nil
}

func assertRollbackErrorStep(t *testing.T, events []nostr.Event, step string) {
	t.Helper()
	for _, ev := range events {
		for _, tag := range ev.Tags {
			if len(tag) >= 2 && tag[0] == "step" && tag[1] == step {
				return
			}
		}
	}
	t.Fatalf("missing rollback error step %q in events %#v", step, events)
}

func TestDirectRuntimeDeployResultCarriesDesiredHashFromState(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	environmentID := uuid.New()
	artifactID := uuid.New()
	obsID := uuid.New()
	desiredHash := "sha256:direct-action-desired"

	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{
		serviceID.String() + ":" + environmentID.String(): {
			ServiceID:         serviceID,
			EnvironmentID:     environmentID,
			DesiredArtifactID: &artifactID,
			DesiredHash:       desiredHash,
		},
	}}
	registry := service.NewRegistryService(
		&testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}},
		&testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod"}},
		&testBuildRepo{},
		&testArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID}},
		&testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}},
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}},
		&testObservationRepo{},
		stateRepo,
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)
	runtimeStub := &stubRuntimeLifecycleOperatorService{deployResp: &domain.RuntimeObservation{ID: obsID, ServiceID: serviceID, EnvironmentID: environmentID, HealthStatus: domain.HealthStatusHealthy, Source: "direct_runtime"}}
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}}, capture, registry, nil, runtimeStub)

	reactor.handleServiceAction(ctx, &nostr.Event{
		ID:      testNostrID("direct-deploy-request"),
		PubKey:  testNostrPubKeyFromPrivateKey(t, testRequesterKey),
		Kind:    KindServiceAction,
		Content: fmt.Sprintf(`{"action":"deploy","service_id":"%s","environment_id":"%s","artifact_id":"%s"}`, serviceID, environmentID, artifactID),
	})

	var result *nostr.Event
	for i := range capture.events {
		if capture.events[i].Kind == KindContextVMMessage {
			result = &capture.events[i]
		}
	}
	if result == nil {
		t.Fatal("direct runtime deploy did not publish terminal result")
	}
	assertReactorTag(t, result.Tags, "desired_hash", desiredHash)
	var payload dto.RuntimeActionResponse
	decodeContextVMResult(t, *result, &payload)
	if payload.DesiredHash != desiredHash {
		t.Fatalf("result desired_hash = %q, want %q", payload.DesiredHash, desiredHash)
	}
}

func TestDirectRuntimeRestartResultDoesNotCarryDesiredHashFromState(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	environmentID := uuid.New()
	desiredHash := "sha256:existing-desired"

	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{
		serviceID.String() + ":" + environmentID.String(): {
			ServiceID:     serviceID,
			EnvironmentID: environmentID,
			DesiredHash:   desiredHash,
		},
	}}
	registry := service.NewRegistryService(
		&testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}},
		&testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod"}},
		&testBuildRepo{},
		&testArtifactRepo{},
		&testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}},
		&testDeploymentRunRepo{runs: map[uuid.UUID]*domain.DeploymentRun{}},
		&testObservationRepo{},
		stateRepo,
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)
	runtimeStub := &stubRuntimeLifecycleOperatorService{restartResp: &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: environmentID, HealthStatus: domain.HealthStatusHealthy, Source: "direct_runtime"}}
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}}, capture, registry, nil, runtimeStub)

	reactor.handleServiceAction(ctx, &nostr.Event{
		ID:      testNostrID("direct-restart-request"),
		PubKey:  testNostrPubKeyFromPrivateKey(t, testRequesterKey),
		Kind:    KindServiceAction,
		Content: fmt.Sprintf(`{"action":"restart","service_id":"%s","environment_id":"%s"}`, serviceID, environmentID),
	})

	var result *nostr.Event
	for i := range capture.events {
		if capture.events[i].Kind == KindContextVMMessage {
			result = &capture.events[i]
		}
	}
	if result == nil {
		t.Fatal("direct runtime restart did not publish terminal result")
	}
	for _, tag := range result.Tags {
		if len(tag) >= 1 && tag[0] == "desired_hash" {
			t.Fatalf("restart result unexpectedly carried desired_hash tag: %v", result.Tags)
		}
	}
	var payload dto.RuntimeActionResponse
	decodeContextVMResult(t, *result, &payload)
	if payload.DesiredHash != "" {
		t.Fatalf("restart result desired_hash = %q, want empty", payload.DesiredHash)
	}
}

func TestHandleDeployRequestRejectsPolicyBlockedRequest(t *testing.T) {
	ctx := context.Background()
	serviceID := uuid.New()
	environmentID := uuid.New()
	artifactID := uuid.New()

	svcRepo := &testServiceRepo{service: &domain.Service{ID: serviceID, Name: "api"}}
	envRepo := &testEnvironmentRepo{environment: &domain.Environment{ID: environmentID, Name: "prod", Protected: true}}
	artifactRepo := &testArtifactRepo{artifact: &domain.Artifact{ID: artifactID, ServiceID: serviceID, ImageDigest: "sha256:blocked"}}
	intentRepo := &testDeploymentIntentRepo{intents: map[uuid.UUID]*domain.DeploymentIntent{}}
	stateRepo := &testEnvironmentServiceStateRepo{states: map[string]*domain.EnvironmentServiceState{}}

	registry := service.NewRegistryService(
		svcRepo,
		envRepo,
		&testBuildRepo{},
		artifactRepo,
		intentRepo,
		&testDeploymentRunRepo{},
		&testObservationRepo{},
		stateRepo,
		nil,
		&events.NoopPublisher{},
		zap.NewNop(),
	)

	policyRepo := &testPolicyRepo{
		globalPolicies: []domain.DeploymentPolicy{{
			ID:          uuid.New(),
			Name:        "require-sig",
			Enforcement: domain.PolicyEnforcementBlock,
			Enabled:     true,
			Rules:       []domain.PolicyRule{{Type: domain.RuleRequireSignature}},
		}},
	}
	policyService := service.NewPolicyService(policyRepo, &testSignatureRepo{hasVerifiedSignature: false}, &testSBOMRepo{}, zap.NewNop())
	capture := &captureNostrPublisher{published: 1}
	reactor := newDeployRequestTestReactor(t, Config{AuthorizedPubkeys: []string{testNostrPubKeyHexFromPrivateKey(t, testRequesterKey)}}, capture, registry, policyService)

	request := &nostr.Event{
		ID:      testNostrID("deploy-request"),
		PubKey:  testNostrPubKeyFromPrivateKey(t, testRequesterKey),
		Kind:    KindDeployRequest,
		Content: fmt.Sprintf(`{"service_id":"%s","environment_id":"%s","artifact_id":"%s"}`, serviceID, environmentID, artifactID),
	}

	reactor.handleDeployRequest(ctx, request)

	if got := len(intentRepo.intents); got != 0 {
		t.Fatalf("deployment intents created = %d, want 0", got)
	}
	if stateRepo.upserts != 0 {
		t.Fatalf("state upserts = %d, want 0", stateRepo.upserts)
	}
	if got := len(capture.events); got != 1 {
		t.Fatalf("published events = %d, want 1", got)
	}

	result := capture.events[0]
	if result.Kind != KindContextVMMessage {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindContextVMMessage)
	}
	assertNoLegacyStatusResultEvents(t, capture.events)
	assertReactorTag(t, result.Tags, "e", request.ID.Hex())
	assertReactorTag(t, result.Tags, "p", request.PubKey.Hex())
	assertReactorTag(t, result.Tags, "status", "error")
	assertReactorTag(t, result.Tags, "step", "policy_blocked")
	assertReactorTag(t, result.Tags, "service", serviceID.String())
	assertReactorTag(t, result.Tags, "environment", environmentID.String())
	assertReactorTag(t, result.Tags, "artifact", artifactID.String())
	assertSignedEvent(t, result)

	var response ContextVMJSONRPCResponse
	if err := json.Unmarshal([]byte(result.Content), &response); err != nil {
		t.Fatalf("decode policy-blocked ContextVM response: %v", err)
	}
	if response.Error == nil || response.Error.Message == "" {
		t.Fatalf("expected policy-blocked ContextVM error, got %#v", response)
	}
}

func newDeployRequestTestReactor(t *testing.T, cfg Config, capture *captureNostrPublisher, registry *service.RegistryService, policyService *service.PolicyService, runtimeLifecycle ...RuntimeLifecycleOperatorService) *Reactor {
	t.Helper()
	signer, err := NewPrivateKeySigner(nostr.Generate().Hex())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	opts := []ReactorOption{WithControlPlanePublisher(capture), WithPolicyService(policyService)}
	if len(runtimeLifecycle) > 0 {
		opts = append(opts, WithRuntimeLifecycleService(runtimeLifecycle[0]))
	}
	return NewReactor(cfg, registry, nil, signer, zap.NewNop(), opts...)
}

type testServiceRepo struct {
	service *domain.Service
}

func (r *testServiceRepo) Create(context.Context, *domain.Service) error { return nil }
func (r *testServiceRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Service, error) {
	if r.service != nil && r.service.ID == id {
		cp := *r.service
		return &cp, nil
	}
	return nil, nil
}
func (r *testServiceRepo) GetByName(context.Context, string) (*domain.Service, error) {
	return nil, nil
}
func (r *testServiceRepo) List(context.Context) ([]domain.Service, error) { return nil, nil }
func (r *testServiceRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Service, error) {
	return nil, nil
}
func (r *testServiceRepo) Update(context.Context, *domain.Service) error { return nil }
func (r *testServiceRepo) Delete(context.Context, uuid.UUID) error       { return nil }

type testEnvironmentRepo struct {
	environment *domain.Environment
}

func (r *testEnvironmentRepo) Create(context.Context, *domain.Environment) error { return nil }
func (r *testEnvironmentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Environment, error) {
	if r.environment != nil && r.environment.ID == id {
		cp := *r.environment
		return &cp, nil
	}
	return nil, nil
}
func (r *testEnvironmentRepo) GetByName(context.Context, string) (*domain.Environment, error) {
	return nil, nil
}
func (r *testEnvironmentRepo) List(context.Context) ([]domain.Environment, error) { return nil, nil }
func (r *testEnvironmentRepo) ListByOrg(context.Context, uuid.UUID) ([]domain.Environment, error) {
	return nil, nil
}
func (r *testEnvironmentRepo) Update(context.Context, *domain.Environment) error { return nil }
func (r *testEnvironmentRepo) Delete(context.Context, uuid.UUID) error           { return nil }

type testBuildRepo struct {
	build *domain.Build
}

func (r *testBuildRepo) Create(context.Context, *domain.Build) error { return nil }
func (r *testBuildRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Build, error) {
	if r.build != nil && r.build.ID == id {
		cp := *r.build
		return &cp, nil
	}
	return nil, nil
}
func (r *testBuildRepo) GetByCISystemRunID(context.Context, string, string) (*domain.Build, error) {
	return nil, nil
}
func (r *testBuildRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Build, error) {
	return nil, nil
}
func (r *testBuildRepo) UpdateStatus(context.Context, uuid.UUID, domain.BuildStatus) error {
	return nil
}

type testArtifactRepo struct {
	artifact  *domain.Artifact
	artifacts map[uuid.UUID]*domain.Artifact
}

func (r *testArtifactRepo) Create(_ context.Context, artifact *domain.Artifact) error {
	if r.artifacts == nil {
		r.artifacts = map[uuid.UUID]*domain.Artifact{}
	}
	cp := *artifact
	r.artifacts[artifact.ID] = &cp
	return nil
}
func (r *testArtifactRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.Artifact, error) {
	if r.artifacts != nil {
		if artifact, ok := r.artifacts[id]; ok {
			cp := *artifact
			return &cp, nil
		}
	}
	if r.artifact != nil && r.artifact.ID == id {
		cp := *r.artifact
		return &cp, nil
	}
	return nil, nil
}
func (r *testArtifactRepo) GetByDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *testArtifactRepo) GetByImageRepoDigest(context.Context, string, string) (*domain.Artifact, error) {
	return nil, nil
}
func (r *testArtifactRepo) ListByService(context.Context, uuid.UUID, int, int) ([]domain.Artifact, error) {
	return nil, nil
}
func (r *testArtifactRepo) ListByBuild(context.Context, uuid.UUID) ([]domain.Artifact, error) {
	return nil, nil
}

type testDeploymentIntentRepo struct {
	intents map[uuid.UUID]*domain.DeploymentIntent
}

func (r *testDeploymentIntentRepo) Create(_ context.Context, di *domain.DeploymentIntent) error {
	if di.ID == uuid.Nil {
		di.ID = uuid.New()
	}
	cp := *di
	r.intents[cp.ID] = &cp
	return nil
}
func (r *testDeploymentIntentRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentIntent, error) {
	intent, ok := r.intents[id]
	if !ok {
		return nil, nil
	}
	cp := *intent
	return &cp, nil
}
func (r *testDeploymentIntentRepo) GetByHiveResultEventID(context.Context, string) (*domain.DeploymentIntent, error) {
	return nil, nil
}
func (r *testDeploymentIntentRepo) ListByServiceEnv(_ context.Context, serviceID, envID uuid.UUID, _, _ int) ([]domain.DeploymentIntent, error) {
	out := make([]domain.DeploymentIntent, 0, len(r.intents))
	for _, intent := range r.intents {
		if intent.ServiceID == serviceID && intent.EnvironmentID == envID {
			out = append(out, *intent)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID.String() < out[j].ID.String()
		}
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}
func (r *testDeploymentIntentRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentIntentStatus) error {
	if intent, ok := r.intents[id]; ok {
		intent.Status = status
	}
	return nil
}
func (r *testDeploymentIntentRepo) UpdateApproval(_ context.Context, id uuid.UUID, status domain.ApprovalStatus) error {
	if intent, ok := r.intents[id]; ok {
		intent.ApprovalStatus = status
	}
	return nil
}
func (r *testDeploymentIntentRepo) UpdateDesiredState(_ context.Context, id uuid.UUID, desiredState *domain.DesiredServiceSpec, desiredHash string) error {
	if intent, ok := r.intents[id]; ok {
		intent.DesiredState = desiredState
		intent.DesiredHash = desiredHash
	}
	return nil
}

type testDeploymentRunRepo struct {
	runs map[uuid.UUID]*domain.DeploymentRun
}

func (r *testDeploymentRunRepo) Create(_ context.Context, run *domain.DeploymentRun) error {
	if r.runs == nil {
		r.runs = map[uuid.UUID]*domain.DeploymentRun{}
	}
	cp := *run
	r.runs[run.ID] = &cp
	return nil
}
func (r *testDeploymentRunRepo) GetByID(_ context.Context, id uuid.UUID) (*domain.DeploymentRun, error) {
	if run, ok := r.runs[id]; ok {
		cp := *run
		return &cp, nil
	}
	return nil, nil
}
func (r *testDeploymentRunRepo) ListByIntent(_ context.Context, intentID uuid.UUID) ([]domain.DeploymentRun, error) {
	out := make([]domain.DeploymentRun, 0, len(r.runs))
	for _, run := range r.runs {
		if run.DeploymentIntentID == intentID {
			out = append(out, *run)
		}
	}
	return out, nil
}
func (r *testDeploymentRunRepo) UpdateStatus(_ context.Context, id uuid.UUID, status domain.DeploymentRunStatus, exitCode *int) error {
	if run, ok := r.runs[id]; ok {
		run.Status = status
		run.ExitCode = exitCode
	}
	return nil
}

type testObservationRepo struct{}

func (r *testObservationRepo) Create(context.Context, *domain.RuntimeObservation) error { return nil }
func (r *testObservationRepo) GetLatest(context.Context, uuid.UUID, uuid.UUID) (*domain.RuntimeObservation, error) {
	return nil, nil
}
func (r *testObservationRepo) ListByServiceEnv(context.Context, uuid.UUID, uuid.UUID, int) ([]domain.RuntimeObservation, error) {
	return nil, nil
}

type testEnvironmentServiceStateRepo struct {
	states  map[string]*domain.EnvironmentServiceState
	upserts int
}

func (r *testEnvironmentServiceStateRepo) Upsert(_ context.Context, state *domain.EnvironmentServiceState) error {
	r.upserts++
	cp := *state
	r.states[state.ServiceID.String()+":"+state.EnvironmentID.String()] = &cp
	return nil
}
func (r *testEnvironmentServiceStateRepo) Get(_ context.Context, serviceID, envID uuid.UUID) (*domain.EnvironmentServiceState, error) {
	state, ok := r.states[serviceID.String()+":"+envID.String()]
	if !ok {
		return nil, nil
	}
	cp := *state
	return &cp, nil
}
func (r *testEnvironmentServiceStateRepo) ListByEnvironment(context.Context, uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *testEnvironmentServiceStateRepo) ListByService(context.Context, uuid.UUID) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *testEnvironmentServiceStateRepo) ListDrifted(context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *testEnvironmentServiceStateRepo) ListDueForObservation(context.Context, time.Time) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}
func (r *testEnvironmentServiceStateRepo) ListAll(context.Context) ([]domain.EnvironmentServiceState, error) {
	return nil, nil
}

type testPolicyRepo struct {
	globalPolicies []domain.DeploymentPolicy
	envPolicies    []domain.DeploymentPolicy
}

func (r *testPolicyRepo) Create(context.Context, *domain.DeploymentPolicy) error { return nil }
func (r *testPolicyRepo) GetByID(context.Context, uuid.UUID) (*domain.DeploymentPolicy, error) {
	return nil, nil
}
func (r *testPolicyRepo) GetByName(context.Context, string) (*domain.DeploymentPolicy, error) {
	return nil, nil
}
func (r *testPolicyRepo) List(context.Context, bool) ([]domain.DeploymentPolicy, error) {
	return nil, nil
}
func (r *testPolicyRepo) ListByEnvironment(context.Context, uuid.UUID) ([]domain.DeploymentPolicy, error) {
	return append([]domain.DeploymentPolicy(nil), r.envPolicies...), nil
}
func (r *testPolicyRepo) ListGlobal(context.Context) ([]domain.DeploymentPolicy, error) {
	return append([]domain.DeploymentPolicy(nil), r.globalPolicies...), nil
}
func (r *testPolicyRepo) Update(context.Context, *domain.DeploymentPolicy) error { return nil }
func (r *testPolicyRepo) Delete(context.Context, uuid.UUID) error                { return nil }

type testSignatureRepo struct {
	hasVerifiedSignature bool
}

func (r *testSignatureRepo) Create(context.Context, *domain.ArtifactSignature) error { return nil }
func (r *testSignatureRepo) GetByID(context.Context, uuid.UUID) (*domain.ArtifactSignature, error) {
	return nil, nil
}
func (r *testSignatureRepo) ListByArtifact(context.Context, uuid.UUID) ([]domain.ArtifactSignature, error) {
	return nil, nil
}
func (r *testSignatureRepo) ListVerifiedByArtifact(context.Context, uuid.UUID) ([]domain.ArtifactSignature, error) {
	return nil, nil
}
func (r *testSignatureRepo) HasVerifiedSignature(context.Context, uuid.UUID) (bool, error) {
	return r.hasVerifiedSignature, nil
}

type testSBOMRepo struct{}

func (r *testSBOMRepo) CreateSBOM(context.Context, *domain.ArtifactSBOM) error { return nil }
func (r *testSBOMRepo) GetSBOMByID(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (r *testSBOMRepo) GetSBOMByArtifact(context.Context, uuid.UUID) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (r *testSBOMRepo) GetSBOMByHash(context.Context, string) (*domain.ArtifactSBOM, error) {
	return nil, repository.ErrNotFound
}
func (r *testSBOMRepo) CreatePackages(context.Context, []domain.SBOMPackage) error { return nil }
func (r *testSBOMRepo) ListPackagesBySBOM(context.Context, uuid.UUID) ([]domain.SBOMPackage, error) {
	return nil, nil
}
func (r *testSBOMRepo) SearchPackagesByName(context.Context, string, int) ([]domain.SBOMPackage, error) {
	return nil, nil
}

func decodeJSONMap(t *testing.T, content string) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal([]byte(content), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return payload
}
