package controlplane

import (
	"context"
	"encoding/json"
	"errors"
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
	signer, err := NewPrivateKeySigner(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	return NewReactor(cfg, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithAdoptionService(adoption))
}

func assertSignedEvent(t *testing.T, ev nostr.Event) {
	t.Helper()
	if ok, err := ev.CheckSignature(); err != nil || !ok {
		t.Fatalf("published event signature invalid: ok=%v err=%v", ok, err)
	}
}
