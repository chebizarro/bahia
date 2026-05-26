package controlplane

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
	"go.uber.org/zap"
)

type stubRuntimeLifecycleOperatorService struct {
	deployServiceID  uuid.UUID
	deployEnvID      uuid.UUID
	deployArtifact   *uuid.UUID
	restartServiceID uuid.UUID
	restartEnvID     uuid.UUID
	stopServiceID    uuid.UUID
	stopEnvID        uuid.UUID
	deployResp       *domain.RuntimeObservation
	restartResp      *domain.RuntimeObservation
	stopResp         *domain.RuntimeObservation
	deployErr        error
	restartErr       error
	stopErr          error
	deployCalled     bool
	restartCalled    bool
	stopCalled       bool
	emitSteps        bool
}

func (s *stubRuntimeLifecycleOperatorService) Deploy(_ context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID) (*domain.RuntimeObservation, error) {
	s.deployCalled = true
	s.deployServiceID = serviceID
	s.deployEnvID = envID
	s.deployArtifact = artifactID
	return s.deployResp, s.deployErr
}

func (s *stubRuntimeLifecycleOperatorService) DeployWithStatus(ctx context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID, statusFn service.DeployStatusCallback) (*domain.RuntimeObservation, error) {
	s.deployCalled = true
	s.deployServiceID = serviceID
	s.deployEnvID = envID
	s.deployArtifact = artifactID
	if statusFn != nil && s.emitSteps {
		for _, step := range []service.DeployStep{
			service.DeployStepBuildingDesiredState,
			service.DeployStepLockingEnvironment,
			service.DeployStepRendering,
			service.DeployStepApplying,
			service.DeployStepObserving,
			service.DeployStepProjecting,
		} {
			statusFn(ctx, step, "step: "+string(step))
		}
	}
	return s.deployResp, s.deployErr
}

func (s *stubRuntimeLifecycleOperatorService) Restart(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	s.restartCalled = true
	s.restartServiceID = serviceID
	s.restartEnvID = envID
	return s.restartResp, s.restartErr
}

func (s *stubRuntimeLifecycleOperatorService) Stop(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	s.stopCalled = true
	s.stopServiceID = serviceID
	s.stopEnvID = envID
	return s.stopResp, s.stopErr
}

type stubAdoptionOperatorService struct {
	scanReq      service.AdoptionScanRequest
	scanResp     []service.AdoptionPreview
	scanErr      error
	scanCalled   bool
	importReq    service.AdoptionImportRequest
	importResp   []service.AdoptionImportResult
	importErr    error
	importCalled bool
}

func (s *stubAdoptionOperatorService) Scan(_ context.Context, req service.AdoptionScanRequest) ([]service.AdoptionPreview, error) {
	s.scanCalled = true
	s.scanReq = req
	return s.scanResp, s.scanErr
}

func (s *stubAdoptionOperatorService) Import(_ context.Context, req service.AdoptionImportRequest) ([]service.AdoptionImportResult, error) {
	s.importCalled = true
	s.importReq = req
	return s.importResp, s.importErr
}

func TestHandleServiceActionRoutesDirectRuntimeDeploy(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	obsID := uuid.New()
	runtimeStub := &stubRuntimeLifecycleOperatorService{deployResp: &domain.RuntimeObservation{
		ID:                  obsID,
		ServiceID:           serviceID,
		EnvironmentID:       envID,
		ObservedImageDigest: "sha256:abc",
		ObservedContainerID: "container-1",
		HealthStatus:        domain.HealthStatusHealthy,
		Source:              "direct_runtime",
	}}
	reactor := newOperatorActionTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{"operator"}}, capture, nil, runtimeStub)

	reactor.handleServiceAction(context.Background(), &nostr.Event{ID: "deploy-request", PubKey: "operator", Kind: KindServiceAction, Content: `{"action":"deploy","service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `","artifact_id":"` + artifactID.String() + `"}`})

	if !runtimeStub.deployCalled || runtimeStub.deployServiceID != serviceID || runtimeStub.deployEnvID != envID || runtimeStub.deployArtifact == nil || *runtimeStub.deployArtifact != artifactID {
		t.Fatalf("deploy was not dispatched correctly: %#v", runtimeStub)
	}
	if len(capture.events) != 2 {
		t.Fatalf("published events = %d, want status and result", len(capture.events))
	}
	statusEvent, resultEvent := capture.events[0], capture.events[1]
	if statusEvent.Kind != KindActionStatus || resultEvent.Kind != KindActionResult {
		t.Fatalf("unexpected event kinds: status=%d result=%d", statusEvent.Kind, resultEvent.Kind)
	}
	assertReactorTag(t, statusEvent.Tags, "status", "processing")
	assertReactorTag(t, statusEvent.Tags, "step", "executing")
	assertReactorTag(t, statusEvent.Tags, "action", "deploy")
	assertReactorTag(t, statusEvent.Tags, "service", serviceID.String())
	assertReactorTag(t, statusEvent.Tags, "environment", envID.String())
	assertReactorTag(t, statusEvent.Tags, "artifact", artifactID.String())
	assertReactorTag(t, resultEvent.Tags, "status", "success")
	assertReactorTag(t, resultEvent.Tags, "action", "deploy")
	assertSignedEvent(t, statusEvent)
	assertSignedEvent(t, resultEvent)

	var payload dto.RuntimeActionResponse
	if err := json.Unmarshal([]byte(resultEvent.Content), &payload); err != nil {
		t.Fatalf("decode runtime action result: %v", err)
	}
	if payload.Action != "deploy" || payload.ServiceID != serviceID || payload.EnvironmentID != envID || payload.Observation == nil || payload.Observation.ID != obsID {
		t.Fatalf("unexpected runtime action payload: %#v", payload)
	}
}

func TestHandleServiceActionDirectRuntimeScopedAuth(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	runtimeStub := &stubRuntimeLifecycleOperatorService{}
	reactor := newOperatorActionTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{"allowed"}}, capture, nil, runtimeStub)

	reactor.handleServiceAction(context.Background(), &nostr.Event{ID: "restart-request", PubKey: "denied", Kind: KindServiceAction, Content: `{"action":"restart","service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `"}`})

	if runtimeStub.restartCalled {
		t.Fatal("runtime lifecycle service should not be called for unauthorized requester")
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want failure result", len(capture.events))
	}
	result := capture.events[0]
	if result.Kind != KindActionResult {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindActionResult)
	}
	assertReactorTag(t, result.Tags, "status", "failed")
	assertReactorTag(t, result.Tags, "action", "restart")
	assertReactorTag(t, result.Tags, "service", serviceID.String())
	assertReactorTag(t, result.Tags, "environment", envID.String())
}

func TestHandleServiceActionDirectRuntimeValidation(t *testing.T) {
	for _, tc := range []struct {
		name   string
		action string
		assert func(*testing.T, *stubRuntimeLifecycleOperatorService)
	}{
		{
			name:   "stop",
			action: "stop",
			assert: func(t *testing.T, stub *stubRuntimeLifecycleOperatorService) {
				t.Helper()
				if stub.stopCalled {
					t.Fatal("runtime lifecycle service should not be called for invalid stop artifact_id")
				}
			},
		},
		{
			name:   "restart",
			action: "restart",
			assert: func(t *testing.T, stub *stubRuntimeLifecycleOperatorService) {
				t.Helper()
				if stub.restartCalled {
					t.Fatal("runtime lifecycle service should not be called for invalid restart artifact_id")
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &captureNostrPublisher{published: 1}
			serviceID := uuid.New()
			envID := uuid.New()
			runtimeStub := &stubRuntimeLifecycleOperatorService{}
			reactor := newOperatorActionTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{"operator"}}, capture, nil, runtimeStub)

			reactor.handleServiceAction(context.Background(), &nostr.Event{ID: tc.name + "-request", PubKey: "operator", Kind: KindServiceAction, Content: `{"action":"` + tc.action + `","service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `","artifact_id":"` + uuid.NewString() + `"}`})

			tc.assert(t, runtimeStub)
			if len(capture.events) != 1 {
				t.Fatalf("published events = %d, want validation failure result", len(capture.events))
			}
			result := capture.events[0]
			assertReactorTag(t, result.Tags, "status", "failed")
			assertReactorTag(t, result.Tags, "action", tc.action)
			if !strings.Contains(result.Content, "artifact_id") {
				t.Fatalf("validation result does not mention artifact_id: %s", result.Content)
			}
		})
	}
}

func TestHandleServiceActionDirectRuntimeFailure(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	runtimeStub := &stubRuntimeLifecycleOperatorService{restartErr: errors.New("runtime docker does not support restart")}
	reactor := newOperatorActionTestReactor(t, Config{AuthorizedPubkeys: []string{"global-operator"}}, capture, nil, runtimeStub)

	reactor.handleServiceAction(context.Background(), &nostr.Event{ID: "restart-request", PubKey: "global-operator", Kind: KindServiceAction, Content: `{"action":"restart","service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `"}`})

	if !runtimeStub.restartCalled {
		t.Fatal("restart was not dispatched")
	}
	if len(capture.events) != 2 {
		t.Fatalf("published events = %d, want status and failure result", len(capture.events))
	}
	result := capture.events[1]
	assertReactorTag(t, result.Tags, "status", "failed")
	assertReactorTag(t, result.Tags, "action", "restart")
	if !strings.Contains(result.Content, "does not support restart") {
		t.Fatalf("failure result = %s", result.Content)
	}
}

func TestHandleServiceActionRoutesSuccessfulRestartAndStop(t *testing.T) {
	for _, tc := range []struct {
		name         string
		action       string
		content      string
		status       domain.HealthStatus
		assertCalled func(*testing.T, *stubRuntimeLifecycleOperatorService, uuid.UUID, uuid.UUID)
	}{
		{
			name:    "restart",
			action:  "restart",
			content: `{"action":"restart","service_id":"%s","environment_id":"%s"}`,
			status:  domain.HealthStatusHealthy,
			assertCalled: func(t *testing.T, stub *stubRuntimeLifecycleOperatorService, serviceID, envID uuid.UUID) {
				t.Helper()
				if !stub.restartCalled || stub.restartServiceID != serviceID || stub.restartEnvID != envID {
					t.Fatalf("restart was not dispatched correctly: %#v", stub)
				}
			},
		},
		{
			name:    "stop",
			action:  "stop",
			content: `{"action":"stop","service_id":"%s","environment_id":"%s"}`,
			status:  domain.HealthStatusStopped,
			assertCalled: func(t *testing.T, stub *stubRuntimeLifecycleOperatorService, serviceID, envID uuid.UUID) {
				t.Helper()
				if !stub.stopCalled || stub.stopServiceID != serviceID || stub.stopEnvID != envID {
					t.Fatalf("stop was not dispatched correctly: %#v", stub)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			capture := &captureNostrPublisher{published: 1}
			serviceID := uuid.New()
			envID := uuid.New()
			obsID := uuid.New()
			runtimeStub := &stubRuntimeLifecycleOperatorService{
				restartResp: &domain.RuntimeObservation{ID: obsID, ServiceID: serviceID, EnvironmentID: envID, HealthStatus: tc.status, Source: "direct_runtime"},
				stopResp:    &domain.RuntimeObservation{ID: obsID, ServiceID: serviceID, EnvironmentID: envID, HealthStatus: tc.status, Source: "direct_runtime"},
			}
			reactor := newOperatorActionTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{"operator"}}, capture, nil, runtimeStub)

			reactor.handleServiceAction(context.Background(), &nostr.Event{ID: tc.name + "-request", PubKey: "operator", Kind: KindServiceAction, Content: fmt.Sprintf(tc.content, serviceID.String(), envID.String())})

			tc.assertCalled(t, runtimeStub, serviceID, envID)
			if len(capture.events) != 2 {
				t.Fatalf("published events = %d, want status and result", len(capture.events))
			}
			statusEvent, resultEvent := capture.events[0], capture.events[1]
			if statusEvent.Kind != KindActionStatus || resultEvent.Kind != KindActionResult {
				t.Fatalf("unexpected event kinds: status=%d result=%d", statusEvent.Kind, resultEvent.Kind)
			}
			assertReactorTag(t, statusEvent.Tags, "status", "processing")
			assertReactorTag(t, statusEvent.Tags, "action", tc.action)
			assertReactorTag(t, resultEvent.Tags, "status", "success")
			assertReactorTag(t, resultEvent.Tags, "action", tc.action)
			assertSignedEvent(t, statusEvent)
			assertSignedEvent(t, resultEvent)

			var payload dto.RuntimeActionResponse
			if err := json.Unmarshal([]byte(resultEvent.Content), &payload); err != nil {
				t.Fatalf("decode runtime action result: %v", err)
			}
			if payload.Action != tc.action || payload.ServiceID != serviceID || payload.EnvironmentID != envID || payload.Observation == nil || payload.Observation.HealthStatus != string(tc.status) {
				t.Fatalf("unexpected runtime action payload: %#v", payload)
			}
		})
	}
}

func TestHandleServiceActionPreservesLegacyAcknowledgement(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	reactor := newOperatorActionTestReactor(t, Config{AuthorizedPubkeys: []string{"operator"}}, capture, nil, nil)

	reactor.handleServiceAction(context.Background(), &nostr.Event{ID: "legacy-request", PubKey: "operator", Kind: KindServiceAction, Tags: nostr.Tags{{"service", "svc-1"}, {"action", "scale"}, {"reason", "capacity"}}})

	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want legacy acknowledgement", len(capture.events))
	}
	result := capture.events[0]
	assertReactorTag(t, result.Tags, "status", "acknowledged")
	assertReactorTag(t, result.Tags, "action", "scale")
}

func TestHandleAdoptionScanRequestRejectsUnauthorized(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	stub := &stubAdoptionOperatorService{}
	reactor := newAdoptionTestReactor(t, Config{AdoptionAuthorizedPubkeys: []string{"allowed"}}, capture, stub)

	reactor.handleAdoptionScanRequest(context.Background(), &nostr.Event{ID: "scan-request", PubKey: "denied", Kind: KindAdoptionScanRequest, Content: `{"targets":[{"name":"prod","endpoint_ref":"prod-docker"}]}`})

	if stub.scanCalled {
		t.Fatal("adoption service should not be called for unauthorized request")
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	result := capture.events[0]
	if result.Kind != KindAdoptionScanResult {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindAdoptionScanResult)
	}
	assertReactorTag(t, result.Tags, "status", "failed")
	assertReactorTag(t, result.Tags, "step", "unauthorized")
	assertSignedEvent(t, result)
}

func TestHandleAdoptionScanRequestRejectsDockerHost(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	stub := &stubAdoptionOperatorService{}
	reactor := newAdoptionTestReactor(t, Config{AdoptionAuthorizedPubkeys: []string{"operator"}}, capture, stub)

	reactor.handleAdoptionScanRequest(context.Background(), &nostr.Event{ID: "scan-request", PubKey: "operator", Kind: KindAdoptionScanRequest, Content: `{"targets":[{"name":"prod","endpoint_ref":"prod-docker","docker_host":"tcp://docker.internal:2376"}]}`})

	if stub.scanCalled {
		t.Fatal("adoption service should not be called when docker_host is present")
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	result := capture.events[0]
	assertReactorTag(t, result.Tags, "status", "failed")
	assertReactorTag(t, result.Tags, "step", "validation_error")
	if !strings.Contains(result.Content, "docker_host") {
		t.Fatalf("validation result does not mention docker_host: %s", result.Content)
	}
	assertSignedEvent(t, result)
}

func TestHandleAdoptionImportRequestRejectsUnauthorized(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	stub := &stubAdoptionOperatorService{}
	reactor := newAdoptionTestReactor(t, Config{AdoptionAuthorizedPubkeys: []string{"allowed"}}, capture, stub)

	reactor.handleAdoptionImportRequest(context.Background(), &nostr.Event{ID: "import-request", PubKey: "denied", Kind: KindAdoptionImportRequest, Content: `{"targets":[{"name":"prod","endpoint_ref":"prod-docker"}],"import_all":true}`})

	if stub.importCalled {
		t.Fatal("adoption service should not be called for unauthorized import request")
	}
	if len(capture.events) != 1 {
		t.Fatalf("published events = %d, want 1", len(capture.events))
	}
	result := capture.events[0]
	if result.Kind != KindAdoptionImportResult {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindAdoptionImportResult)
	}
	assertReactorTag(t, result.Tags, "status", "failed")
	assertReactorTag(t, result.Tags, "step", "unauthorized")
	assertSignedEvent(t, result)
}

func TestHandleAdoptionScanRequestPublishesSanitizedResult(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	stub := &stubAdoptionOperatorService{scanResp: []service.AdoptionPreview{{
		Target: service.AdoptionTarget{Name: "prod", EndpointRef: "prod-docker", DockerHost: "tcp://secret-docker.internal:2376", EnvironmentName: "prod"},
		Containers: []service.AdoptionPreviewContainer{{
			Discovered: runtime.DiscoveredContainer{
				TargetName:      "prod",
				EnvironmentName: "prod",
				ContainerID:     "abc123",
				ContainerName:   "legacy-api",
				ImageRef:        "ghcr.io/org/api:v1",
				Environment:     map[string]string{"APP_ENV": "prod", "DB_PASSWORD": "secret"},
				Labels:          map[string]string{"public": "label", "secret-token": "secret"},
				Compose:         &domain.ComposeMetadata{ProjectName: "legacy", ConfigFiles: []string{"compose.yml"}},
				HealthStatus:    domain.HealthStatusHealthy,
				Adoptable:       true,
			},
			SafeEnvironment:         map[string]string{"APP_ENV": "prod"},
			SafeLabels:              map[string]string{"public": "label"},
			RedactedEnvironmentKeys: []string{"DB_PASSWORD"},
			RedactedLabelKeys:       []string{"secret-token"},
			ProposedServiceName:     "legacy-api",
			ExistingServiceID:       &serviceID,
			WillUpdate:              true,
			Adoptable:               true,
		}},
	}}}
	reactor := newAdoptionTestReactor(t, Config{AuthorizedPubkeys: []string{"global-operator"}}, capture, stub)

	reactor.handleAdoptionScanRequest(context.Background(), &nostr.Event{ID: "scan-request", PubKey: "global-operator", Kind: KindAdoptionScanRequest, Content: `{"targets":[{"name":" Prod ","endpoint_ref":"prod-docker","environment_name":" Prod "}]}`})

	if !stub.scanCalled {
		t.Fatal("adoption scan service was not called")
	}
	if len(stub.scanReq.Targets) != 1 || stub.scanReq.Targets[0].Name != "prod" || stub.scanReq.Targets[0].EndpointRef != "prod-docker" || stub.scanReq.Targets[0].DockerHost != "" {
		t.Fatalf("scan request not mapped safely: %#v", stub.scanReq.Targets)
	}
	if len(capture.events) != 2 {
		t.Fatalf("published events = %d, want status and result", len(capture.events))
	}
	statusEvent, resultEvent := capture.events[0], capture.events[1]
	if statusEvent.Kind != KindAdoptionStatus || resultEvent.Kind != KindAdoptionScanResult {
		t.Fatalf("unexpected event kinds: status=%d result=%d", statusEvent.Kind, resultEvent.Kind)
	}
	assertReactorTag(t, statusEvent.Tags, "status", "processing")
	assertReactorTag(t, statusEvent.Tags, "operation", "scan")
	assertReactorTag(t, statusEvent.Tags, "target", "prod")
	assertReactorTag(t, statusEvent.Tags, "endpoint_ref", "prod-docker")
	assertReactorTag(t, resultEvent.Tags, "status", "success")
	assertReactorTag(t, resultEvent.Tags, "operation", "scan")
	assertReactorTag(t, resultEvent.Tags, "endpoint_ref", "prod-docker")
	assertSignedEvent(t, statusEvent)
	assertSignedEvent(t, resultEvent)

	var payload []dto.AdoptionPreviewResponse
	if err := json.Unmarshal([]byte(resultEvent.Content), &payload); err != nil {
		t.Fatalf("decode scan result: %v", err)
	}
	if len(payload) != 1 || len(payload[0].Containers) != 1 {
		t.Fatalf("unexpected scan payload: %#v", payload)
	}
	if payload[0].Target.DockerHost != "" {
		t.Fatalf("sanitized scan payload leaked docker_host: %#v", payload[0].Target)
	}
	container := payload[0].Containers[0]
	if _, ok := container.Discovered.Environment["DB_PASSWORD"]; ok {
		t.Fatalf("sanitized scan payload leaked raw env: %#v", container.Discovered.Environment)
	}
	if _, ok := container.Discovered.Labels["secret-token"]; ok {
		t.Fatalf("sanitized scan payload leaked raw labels: %#v", container.Discovered.Labels)
	}
	if container.Discovered.Environment["APP_ENV"] != "prod" || container.Discovered.Labels["public"] != "label" {
		t.Fatalf("safe payload not preserved: env=%#v labels=%#v", container.Discovered.Environment, container.Discovered.Labels)
	}
}

func TestHandleAdoptionImportRequestPublishesPartialFailureResult(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	stub := &stubAdoptionOperatorService{importResp: []service.AdoptionImportResult{
		{TargetName: "prod", ContainerID: "abc123", ContainerName: "api", ServiceName: "api", ServiceID: &serviceID, Status: "created", RedactedEnvironmentKeys: []string{"DB_PASSWORD"}},
		{TargetName: "prod", ContainerID: "def456", ContainerName: "worker", Status: "failed", Error: "image unsupported"},
	}}
	reactor := newAdoptionTestReactor(t, Config{AdoptionAuthorizedPubkeys: []string{"operator"}}, capture, stub)

	reactor.handleAdoptionImportRequest(context.Background(), &nostr.Event{ID: "import-request", PubKey: "operator", Kind: KindAdoptionImportRequest, Content: `{"targets":[{"name":"prod","endpoint_ref":"prod-docker"}],"import_all":true}`})

	if !stub.importCalled {
		t.Fatal("adoption import service was not called")
	}
	if len(stub.importReq.Targets) != 1 || stub.importReq.Targets[0].EndpointRef != "prod-docker" || !stub.importReq.ImportAll {
		t.Fatalf("import request not mapped: %#v", stub.importReq)
	}
	if len(capture.events) != 2 {
		t.Fatalf("published events = %d, want status and result", len(capture.events))
	}
	result := capture.events[1]
	if result.Kind != KindAdoptionImportResult {
		t.Fatalf("result kind = %d, want %d", result.Kind, KindAdoptionImportResult)
	}
	assertReactorTag(t, result.Tags, "status", "partial_failure")
	assertReactorTag(t, result.Tags, "operation", "import")
	assertSignedEvent(t, result)
	var payload []dto.AdoptionImportResultResponse
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("decode import result: %v", err)
	}
	if len(payload) != 2 || payload[0].RedactedEnvironmentKeys[0] != "DB_PASSWORD" || payload[1].Error != "image unsupported" {
		t.Fatalf("unexpected import payload: %#v", payload)
	}
}

func TestHandleAdoptionScanRequestPublishesOperationFailure(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	stub := &stubAdoptionOperatorService{scanErr: errors.New("runtime unavailable")}
	reactor := newAdoptionTestReactor(t, Config{AdoptionAuthorizedPubkeys: []string{"operator"}}, capture, stub)

	reactor.handleAdoptionScanRequest(context.Background(), &nostr.Event{ID: "scan-request", PubKey: "operator", Kind: KindAdoptionScanRequest, Content: `{"targets":[{"name":"prod","endpoint_ref":"prod-docker"}]}`})

	if len(capture.events) != 2 {
		t.Fatalf("published events = %d, want status and failure result", len(capture.events))
	}
	result := capture.events[1]
	assertReactorTag(t, result.Tags, "status", "failed")
	assertReactorTag(t, result.Tags, "step", "operation_failed")
	if !strings.Contains(result.Content, "runtime unavailable") {
		t.Fatalf("failure content = %s", result.Content)
	}
}

func newAdoptionTestReactor(t *testing.T, cfg Config, capture *captureNostrPublisher, adoption AdoptionOperatorService) *Reactor {
	t.Helper()
	return newOperatorActionTestReactor(t, cfg, capture, adoption, nil)
}

func newOperatorActionTestReactor(t *testing.T, cfg Config, capture *captureNostrPublisher, adoption AdoptionOperatorService, runtimeLifecycle RuntimeLifecycleOperatorService) *Reactor {
	t.Helper()
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	return NewReactor(cfg, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithAdoptionService(adoption), WithRuntimeLifecycleService(runtimeLifecycle))
}

func assertSignedEvent(t *testing.T, ev nostr.Event) {
	t.Helper()
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("published event signature invalid: ok=%v err=%v", ok, err)
	}
}

func TestDeployStepProgressionEmitsStatusEvents(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	obsID := uuid.New()
	runtimeStub := &stubRuntimeLifecycleOperatorService{
		emitSteps: true,
		deployResp: &domain.RuntimeObservation{
			ID:            obsID,
			ServiceID:     serviceID,
			EnvironmentID: envID,
			HealthStatus:  domain.HealthStatusHealthy,
			Source:        "direct_runtime",
		},
	}
	reactor := newOperatorActionTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{"operator"}}, capture, nil, runtimeStub)

	reactor.handleServiceAction(context.Background(), &nostr.Event{
		ID:      "deploy-steps-request",
		PubKey:  "operator",
		Kind:    KindServiceAction,
		Content: `{"action":"deploy","service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `","artifact_id":"` + artifactID.String() + `"}`,
	})

	if !runtimeStub.deployCalled {
		t.Fatal("deploy was not called")
	}

	// Expected events: 1 initial "executing" status + 6 step statuses + 1 final result = 8
	expectedSteps := []string{
		"executing",
		"building_desired_state",
		"locking_environment",
		"rendering",
		"applying",
		"observing",
		"projecting",
	}
	expectedTotal := len(expectedSteps) + 1 // steps + final result
	if len(capture.events) != expectedTotal {
		t.Fatalf("published events = %d, want %d (7 status + 1 result)", len(capture.events), expectedTotal)
	}

	// Verify step status events are emitted in order with correct tags
	for i, expectedStep := range expectedSteps {
		ev := capture.events[i]
		if ev.Kind != KindActionStatus {
			t.Fatalf("event[%d] kind = %d, want %d (KindActionStatus)", i, ev.Kind, KindActionStatus)
		}
		assertReactorTag(t, ev.Tags, "status", "processing")
		assertReactorTag(t, ev.Tags, "action", "deploy")
		assertReactorTag(t, ev.Tags, "step", expectedStep)
		// Resource correlation tags from appendRequestResourceTags
		assertReactorTag(t, ev.Tags, "service", serviceID.String())
		assertReactorTag(t, ev.Tags, "environment", envID.String())
		assertReactorTag(t, ev.Tags, "artifact", artifactID.String())
		assertSignedEvent(t, ev)
	}

	// Verify final result event
	resultEvent := capture.events[len(capture.events)-1]
	if resultEvent.Kind != KindActionResult {
		t.Fatalf("final event kind = %d, want %d (KindActionResult)", resultEvent.Kind, KindActionResult)
	}
	assertReactorTag(t, resultEvent.Tags, "status", "success")
	assertReactorTag(t, resultEvent.Tags, "action", "deploy")
	assertReactorTag(t, resultEvent.Tags, "service", serviceID.String())
	assertReactorTag(t, resultEvent.Tags, "environment", envID.String())
	assertSignedEvent(t, resultEvent)
}

func TestDeployStepProgressionBackwardCompatible(t *testing.T) {
	// Verify that status events with step tags can be decoded by consumers
	// that only look for known tags — unknown tags are silently ignored.
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	runtimeStub := &stubRuntimeLifecycleOperatorService{
		emitSteps: true,
		deployResp: &domain.RuntimeObservation{
			ID:            uuid.New(),
			ServiceID:     serviceID,
			EnvironmentID: envID,
			HealthStatus:  domain.HealthStatusHealthy,
			Source:        "direct_runtime",
		},
	}
	reactor := newOperatorActionTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{"operator"}}, capture, nil, runtimeStub)

	reactor.handleServiceAction(context.Background(), &nostr.Event{
		ID:      "compat-request",
		PubKey:  "operator",
		Kind:    KindServiceAction,
		Content: `{"action":"deploy","service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `"}`,
	})

	// All status events must have valid nostr event structure
	for i, ev := range capture.events {
		if ev.Kind == KindActionStatus {
			// A consumer that only reads "status" and "action" tags should work fine
			var foundStatus, foundAction bool
			for _, tag := range ev.Tags {
				if len(tag) >= 2 {
					switch tag[0] {
					case "status":
						foundStatus = true
					case "action":
						foundAction = true
					}
				}
			}
			if !foundStatus || !foundAction {
				t.Fatalf("event[%d] missing required base tags: status=%v action=%v", i, foundStatus, foundAction)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Desired-state metadata enrichment tests (Item 8 — bahia-zu2p.7.2)
// ---------------------------------------------------------------------------

func TestRuntimeActionResultCarriesObservationID(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	obsID := uuid.New()
	runtimeStub := &stubRuntimeLifecycleOperatorService{deployResp: &domain.RuntimeObservation{
		ID:             obsID,
		ServiceID:      serviceID,
		EnvironmentID:  envID,
		HealthStatus:   domain.HealthStatusHealthy,
		Source:         "direct_runtime",
		NormalizedHash: "sha256:obs-hash",
	}}
	reactor := newOperatorActionTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{"operator"}}, capture, nil, runtimeStub)

	reactor.handleServiceAction(context.Background(), &nostr.Event{
		ID:      "enriched-deploy-request",
		PubKey:  "operator",
		Kind:    KindServiceAction,
		Content: `{"action":"deploy","service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `"}`,
	})

	if len(capture.events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(capture.events))
	}
	resultEvent := capture.events[len(capture.events)-1]
	if resultEvent.Kind != KindActionResult {
		t.Fatalf("last event kind = %d, want %d", resultEvent.Kind, KindActionResult)
	}
	// Verify observation_id and observed_hash tags are present
	assertReactorTag(t, resultEvent.Tags, "observation_id", obsID.String())
	assertReactorTag(t, resultEvent.Tags, "observed_hash", "sha256:obs-hash")
	assertSignedEvent(t, resultEvent)
}

func TestRuntimeActionResultOmitsObservationTagsWhenNil(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	serviceID := uuid.New()
	envID := uuid.New()
	// Restart returns nil observation
	runtimeStub := &stubRuntimeLifecycleOperatorService{restartResp: nil}
	reactor := newOperatorActionTestReactor(t, Config{DirectRuntimeAuthorizedPubkeys: []string{"operator"}}, capture, nil, runtimeStub)

	reactor.handleServiceAction(context.Background(), &nostr.Event{
		ID:      "restart-request",
		PubKey:  "operator",
		Kind:    KindServiceAction,
		Content: `{"action":"restart","service_id":"` + serviceID.String() + `","environment_id":"` + envID.String() + `"}`,
	})

	if len(capture.events) < 2 {
		t.Fatalf("expected at least 2 events, got %d", len(capture.events))
	}
	resultEvent := capture.events[len(capture.events)-1]
	if resultEvent.Kind != KindActionResult {
		t.Fatalf("last event kind = %d, want %d", resultEvent.Kind, KindActionResult)
	}
	// observation_id should NOT be present when obs is nil
	for _, tag := range resultEvent.Tags {
		if len(tag) >= 2 && tag[0] == "observation_id" {
			t.Fatal("observation_id tag should not be present when observation is nil")
		}
	}
	assertSignedEvent(t, resultEvent)
}
