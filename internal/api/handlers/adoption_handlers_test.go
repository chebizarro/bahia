package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/adapters/runtime"
	"github.com/openagentsinc/bahia/internal/api/dto"
	"github.com/openagentsinc/bahia/internal/domain"
	"github.com/openagentsinc/bahia/internal/service"
)

type stubAdoptionService struct {
	scanReq    service.AdoptionScanRequest
	scanResp   []service.AdoptionPreview
	importReq  service.AdoptionImportRequest
	importResp []service.AdoptionImportResult
}

func (s *stubAdoptionService) Scan(_ context.Context, req service.AdoptionScanRequest) ([]service.AdoptionPreview, error) {
	s.scanReq = req
	return s.scanResp, nil
}

func (s *stubAdoptionService) Import(_ context.Context, req service.AdoptionImportRequest) ([]service.AdoptionImportResult, error) {
	s.importReq = req
	return s.importResp, nil
}

func TestAdoptionHandlerScanMapsRequestAndResponse(t *testing.T) {
	serviceID := uuid.New()
	stub := &stubAdoptionService{scanResp: []service.AdoptionPreview{{
		Target: service.AdoptionTarget{Name: "local", DockerHost: "unix:///var/run/docker.sock", EnvironmentName: "prod"},
		Containers: []service.AdoptionPreviewContainer{{
			Discovered: runtime.DiscoveredContainer{
				ContainerID:   "abc123",
				ContainerName: "legacy-api",
				ImageRef:      "ghcr.io/org/api:v1",
				Compose:       &domain.ComposeMetadata{ProjectName: "legacy", ServiceName: "api", ConfigFiles: []string{"compose.yml"}},
				HealthStatus:  domain.HealthStatusHealthy,
				Adoptable:     true,
			},
			ProposedServiceName: "legacy-api",
			ExistingServiceID:   &serviceID,
			WillUpdate:          true,
			Adoptable:           true,
		}},
	}}}
	h := NewAdoptionHandler(stub)

	w := postJSON(t, h.Scan, dto.ScanAdoptionRequest{Targets: []dto.AdoptionTargetRequest{{Name: " local ", DockerHost: " unix:///var/run/docker.sock ", EnvironmentName: " prod "}}})
	assertStatus(t, w, http.StatusOK)
	if len(stub.scanReq.Targets) != 1 || stub.scanReq.Targets[0].Name != "local" || stub.scanReq.Targets[0].EnvironmentName != "prod" {
		t.Fatalf("request was not mapped/trimmed: %#v", stub.scanReq)
	}

	var resp struct {
		Data []dto.AdoptionPreviewResponse `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Data) != 1 || len(resp.Data[0].Containers) != 1 {
		t.Fatalf("unexpected response: %#v", resp.Data)
	}
	container := resp.Data[0].Containers[0]
	if container.ProposedServiceName != "legacy-api" || !container.WillUpdate || container.ExistingServiceID == nil || *container.ExistingServiceID != serviceID {
		t.Fatalf("unexpected preview container: %#v", container)
	}
	if container.Discovered.HealthStatus != string(domain.HealthStatusHealthy) {
		t.Fatalf("health_status = %q, want %q", container.Discovered.HealthStatus, domain.HealthStatusHealthy)
	}
	if container.Discovered.Compose == nil || container.Discovered.Compose.ProjectName != "legacy" || len(container.Discovered.Compose.ConfigFiles) != 1 {
		t.Fatalf("compose metadata not mapped to DTO: %#v", container.Discovered.Compose)
	}
}

func TestAdoptionHandlerRejectsDuplicateTargetsAfterNormalization(t *testing.T) {
	h := NewAdoptionHandler(&stubAdoptionService{})
	w := postJSON(t, h.Scan, dto.ScanAdoptionRequest{Targets: []dto.AdoptionTargetRequest{
		{Name: "Local", DockerHost: "unix:///docker.sock"},
		{Name: "local", DockerHost: "tcp://docker.example:2376"},
	}})
	assertStatus(t, w, http.StatusBadRequest)
	assertErrorContains(t, w, "normalization")
}

func TestAdoptionHandlerImportRequiresAllOrSelection(t *testing.T) {
	h := NewAdoptionHandler(&stubAdoptionService{})
	w := postJSON(t, h.Import, dto.ImportAdoptionRequest{Targets: []dto.AdoptionTargetRequest{{Name: "local", DockerHost: "unix:///docker.sock"}}})
	assertStatus(t, w, http.StatusBadRequest)
	assertErrorContains(t, w, "import_all")
}

func TestAdoptionHandlerImportMapsSelections(t *testing.T) {
	serviceID := uuid.New()
	stub := &stubAdoptionService{importResp: []service.AdoptionImportResult{{TargetName: "local", ContainerID: "abc123", ServiceName: "api", ServiceID: &serviceID, Status: "created"}}}
	h := NewAdoptionHandler(stub)

	w := postJSON(t, h.Import, dto.ImportAdoptionRequest{
		Targets:    []dto.AdoptionTargetRequest{{Name: "local", DockerHost: "unix:///docker.sock"}},
		Selections: []dto.AdoptionSelectionRequest{{TargetName: "local", ContainerID: "abc123", ServiceNameOverride: "api"}},
	})
	assertStatus(t, w, http.StatusOK)
	if len(stub.importReq.Selections) != 1 || stub.importReq.Selections[0].ServiceNameOverride != "api" {
		t.Fatalf("selection not mapped: %#v", stub.importReq)
	}
}

type stubRuntimeLifecycleService struct {
	deployServiceID uuid.UUID
	deployEnvID     uuid.UUID
	deployArtifact  *uuid.UUID
	restartCalled   bool
	stopCalled      bool
}

func (s *stubRuntimeLifecycleService) Deploy(_ context.Context, serviceID, envID uuid.UUID, artifactID *uuid.UUID) (*domain.RuntimeObservation, error) {
	s.deployServiceID = serviceID
	s.deployEnvID = envID
	s.deployArtifact = artifactID
	return &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: envID, HealthStatus: domain.HealthStatusHealthy}, nil
}

func (s *stubRuntimeLifecycleService) Restart(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	s.restartCalled = true
	return &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: envID, HealthStatus: domain.HealthStatusHealthy}, nil
}

func (s *stubRuntimeLifecycleService) Stop(_ context.Context, serviceID, envID uuid.UUID) (*domain.RuntimeObservation, error) {
	s.stopCalled = true
	return &domain.RuntimeObservation{ID: uuid.New(), ServiceID: serviceID, EnvironmentID: envID, HealthStatus: domain.HealthStatusStopped}, nil
}

func TestServiceActionHandlerDeployParsesIDsAndArtifact(t *testing.T) {
	serviceID := uuid.New()
	envID := uuid.New()
	artifactID := uuid.New()
	stub := &stubRuntimeLifecycleService{}
	h := NewServiceActionHandler(stub)

	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Body = ioNopCloserString(`{"artifact_id":"` + artifactID.String() + `"}`)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("serviceId", serviceID.String())
	rctx.URLParams.Add("envId", envID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.Deploy(w, req)
	assertStatus(t, w, http.StatusOK)

	if stub.deployServiceID != serviceID || stub.deployEnvID != envID || stub.deployArtifact == nil || *stub.deployArtifact != artifactID {
		t.Fatalf("deploy args not mapped: %#v", stub)
	}
	var resp struct {
		Data dto.RuntimeActionResponse `json:"data"`
	}
	if err := json.NewDecoder(w.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Data.Observation == nil || resp.Data.Observation.HealthStatus != string(domain.HealthStatusHealthy) {
		t.Fatalf("runtime observation not mapped to DTO: %#v", resp.Data.Observation)
	}
}

func TestRuntimeLifecycleErrorStatusMapping(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
	}{
		{name: "not found", err: errors.New("service 123 not found"), status: http.StatusNotFound},
		{name: "bad request", err: errors.New("no desired artifact for service"), status: http.StatusBadRequest},
		{name: "conflict", err: errors.New("runtime docker does not support restart"), status: http.StatusConflict},
		{name: "internal", err: errors.New("docker daemon unavailable"), status: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			writeRuntimeLifecycleError(w, tt.err)
			assertStatus(t, w, tt.status)
		})
	}
}

func TestServiceActionHandlerInvalidServiceID(t *testing.T) {
	h := NewServiceActionHandler(&stubRuntimeLifecycleService{})
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("serviceId", "not-a-uuid")
	rctx.URLParams.Add("envId", uuid.NewString())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	w := httptest.NewRecorder()
	h.Restart(w, req)
	assertStatus(t, w, http.StatusBadRequest)
}

type stringReadCloser struct{ *strings.Reader }

func (s stringReadCloser) Close() error { return nil }

func ioNopCloserString(s string) stringReadCloser { return stringReadCloser{strings.NewReader(s)} }
