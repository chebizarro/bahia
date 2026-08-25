package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Mock Docker API server for apply tests
// ---------------------------------------------------------------------------

// applyMockState tracks the state of containers, networks, and volumes for
// the mock Docker server used in apply tests.
type applyMockState struct {
	mu sync.Mutex

	// containers keyed by ID.
	containers map[string]DockerContainer
	// nextContainerID is the next container ID to assign on create.
	nextContainerID int

	// API call tracking.
	pullCalls    []string
	stopCalls    []string
	removeCalls  []string
	createCalls  []createCall
	startCalls   []string
	connectCalls []connectCall

	// Failure injection.
	failPull   bool
	failStop   bool
	failRemove bool
	failCreate bool
	failStart  bool

	// Networks and volumes (reuse existing mock patterns).
	networks map[string]mockDockerResource
	volumes  map[string]mockDockerResource
}

type createCall struct {
	Name string
	Body map[string]any
}

type connectCall struct {
	NetworkName string
	ContainerID string
}

func newApplyMockState() *applyMockState {
	return &applyMockState{
		containers: make(map[string]DockerContainer),
		networks:   make(map[string]mockDockerResource),
		volumes:    make(map[string]mockDockerResource),
	}
}

func (m *applyMockState) addContainer(c DockerContainer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.containers[c.ID] = c
}

func (m *applyMockState) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")

		switch {
		// List containers: GET /v1.44/containers/json
		case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/json":
			m.handleListContainers(w, r)

		// Stop container: POST /v1.44/containers/{id}/stop
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/stop"):
			id := extractContainerID(r.URL.Path, "/stop")
			m.stopCalls = append(m.stopCalls, id)
			if m.failStop {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		// Remove container: DELETE /v1.44/containers/{id}
		case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/v1.44/containers/"):
			id := strings.TrimPrefix(r.URL.Path, "/v1.44/containers/")
			id = strings.Split(id, "?")[0]
			m.removeCalls = append(m.removeCalls, id)
			if m.failRemove {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			delete(m.containers, id)
			w.WriteHeader(http.StatusNoContent)

		// Create container: POST /v1.44/containers/create
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/containers/create":
			name := r.URL.Query().Get("name")
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			m.createCalls = append(m.createCalls, createCall{Name: name, Body: body})
			if m.failCreate {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			m.nextContainerID++
			newID := fmt.Sprintf("container-%d", m.nextContainerID)
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"Id": newID})

		// Start container: POST /v1.44/containers/{id}/start
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/start"):
			id := extractContainerID(r.URL.Path, "/start")
			m.startCalls = append(m.startCalls, id)
			if m.failStart {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusNoContent)

		// Pull image: POST /v1.44/images/create
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1.44/images/create"):
			image := r.URL.Query().Get("fromImage")
			m.pullCalls = append(m.pullCalls, image)
			if m.failPull {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"done"}`))

		// Network connect: POST /v1.44/networks/{name}/connect
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/connect"):
			parts := strings.Split(r.URL.Path, "/")
			// /v1.44/networks/{name}/connect → parts[3] is name
			networkName := ""
			if len(parts) >= 5 {
				networkName = parts[3]
			}
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			containerID, _ := body["Container"].(string)
			m.connectCalls = append(m.connectCalls, connectCall{NetworkName: networkName, ContainerID: containerID})
			w.WriteHeader(http.StatusOK)

		// Network inspect: GET /v1.44/networks/{name}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.44/networks/"):
			name := strings.TrimPrefix(r.URL.Path, "/v1.44/networks/")
			if net, ok := m.networks[name]; ok {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(net)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}

		// Network create: POST /v1.44/networks/create
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/networks/create":
			var body struct {
				Name   string `json:"Name"`
				Driver string `json:"Driver"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			driver := body.Driver
			if driver == "" {
				driver = "bridge"
			}
			m.networks[body.Name] = mockDockerResource{Name: body.Name, Driver: driver}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"Id": "net-" + body.Name})

		// Volume inspect: GET /v1.44/volumes/{name}
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1.44/volumes/"):
			name := strings.TrimPrefix(r.URL.Path, "/v1.44/volumes/")
			if vol, ok := m.volumes[name]; ok {
				w.WriteHeader(http.StatusOK)
				json.NewEncoder(w).Encode(vol)
			} else {
				w.WriteHeader(http.StatusNotFound)
			}

		// Volume create: POST /v1.44/volumes/create
		case r.Method == http.MethodPost && r.URL.Path == "/v1.44/volumes/create":
			var body struct {
				Name   string `json:"Name"`
				Driver string `json:"Driver"`
			}
			json.NewDecoder(r.Body).Decode(&body)
			driver := body.Driver
			if driver == "" {
				driver = "local"
			}
			m.volumes[body.Name] = mockDockerResource{Name: body.Name, Driver: driver}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(map[string]string{"Name": body.Name})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
}

func (m *applyMockState) handleListContainers(w http.ResponseWriter, r *http.Request) {
	filtersRaw := r.URL.Query().Get("filters")
	var containers []DockerContainer

	if filtersRaw != "" {
		// Parse filters for label-based lookup.
		var filters map[string][]string
		json.Unmarshal([]byte(filtersRaw), &filters)
		labelFilters := filters["label"]

		for _, c := range m.containers {
			if matchesLabelFilters(c, labelFilters) {
				containers = append(containers, c)
			}
		}
	} else {
		for _, c := range m.containers {
			containers = append(containers, c)
		}
	}

	json.NewEncoder(w).Encode(containers)
}

func matchesLabelFilters(c DockerContainer, filters []string) bool {
	for _, f := range filters {
		parts := strings.SplitN(f, "=", 2)
		if len(parts) != 2 {
			continue
		}
		if c.Labels[parts[0]] != parts[1] {
			return false
		}
	}
	return true
}

func extractContainerID(path, suffix string) string {
	// /v1.44/containers/{id}/{suffix}
	trimmed := strings.TrimPrefix(path, "/v1.44/containers/")
	return strings.TrimSuffix(trimmed, "/"+strings.TrimPrefix(suffix, "/"))
}

func setupApplyTest(mock *applyMockState) (*httptest.Server, *DockerObserver) {
	server := httptest.NewServer(mock.handler())
	observer := &DockerObserver{
		httpClient: server.Client(),
		host:       server.URL,
		logger:     zap.NewNop(),
	}
	return server, observer
}

func applyTestSpec() *domain.DesiredServiceSpec {
	return testDesiredSpec()
}

func applyTestRequest(spec *domain.DesiredServiceSpec) DesiredStateApplyRequest {
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: testEnvironmentID,
		Services:      []domain.DesiredServiceSpec{*spec},
	}
	plan.ComputeRevisionHash()

	return DesiredStateApplyRequest{
		EnvironmentPlan: plan,
		TargetService:   spec,
		Secrets:         map[string]string{"DB_PASSWORD": "s3cret", "API_KEY": "key-123"},
		PullPolicy:      "if-not-present",
	}
}

// ===========================================================================
// SupportsDesiredState
// ===========================================================================

func TestDockerObserver_SupportsDesiredState(t *testing.T) {
	t.Parallel()
	observer := &DockerObserver{logger: zap.NewNop()}
	if !observer.SupportsDesiredState() {
		t.Error("DockerObserver should support desired state")
	}
}

// ===========================================================================
// No-op: hash match
// ===========================================================================

func TestApplyDesiredState_NoOp_HashMatch(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	// Pre-populate a container with matching desired hash.
	mock.addContainer(DockerContainer{
		ID:    "existing-123",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should be a no-op.
	if result.Renderer != "docker" {
		t.Errorf("renderer = %q, want docker", result.Renderer)
	}
	if result.ExecutionMode != ExecutionModeEngineAPI {
		t.Errorf("execution mode = %q, want %q", result.ExecutionMode, ExecutionModeEngineAPI)
	}
	if result.DesiredHash != spec.DesiredHash {
		t.Errorf("desired_hash = %q, want %q", result.DesiredHash, spec.DesiredHash)
	}
	if len(result.ResourceIDs) != 1 || result.ResourceIDs[0] != "existing-123" {
		t.Errorf("resource_ids = %v, want [existing-123]", result.ResourceIDs)
	}
	if result.ObservationHints == nil || result.ObservationHints.ContainerID != "existing-123" {
		t.Error("observation hints should point to existing container")
	}

	// No mutations should have happened.
	if len(mock.pullCalls) > 0 {
		t.Error("should not pull on no-op")
	}
	if len(mock.stopCalls) > 0 {
		t.Error("should not stop on no-op")
	}
	if len(mock.removeCalls) > 0 {
		t.Error("should not remove on no-op")
	}
	if len(mock.createCalls) > 0 {
		t.Error("should not create on no-op")
	}
	if len(mock.startCalls) > 0 {
		t.Error("should not start on no-op")
	}
}

// ===========================================================================
// No-op overridden by pull policy "always"
// ===========================================================================

func TestApplyDesiredState_HashMatch_PullAlways_Recreates(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "existing-123",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   spec.DesiredHash,
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.PullPolicy = "always"

	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have performed full recreate despite hash match.
	if len(mock.pullCalls) != 1 {
		t.Errorf("expected 1 pull call, got %d", len(mock.pullCalls))
	}
	if len(mock.stopCalls) != 1 {
		t.Errorf("expected 1 stop call, got %d", len(mock.stopCalls))
	}
	if len(mock.removeCalls) != 1 {
		t.Errorf("expected 1 remove call, got %d", len(mock.removeCalls))
	}
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create call, got %d", len(mock.createCalls))
	}
	if len(mock.startCalls) != 1 {
		t.Errorf("expected 1 start call, got %d", len(mock.startCalls))
	}

	// Result should have the new container ID.
	if len(result.ResourceIDs) != 1 || result.ResourceIDs[0] != "container-1" {
		t.Errorf("resource_ids = %v, want [container-1]", result.ResourceIDs)
	}
}

// ===========================================================================
// Hash drift: recreate
// ===========================================================================

func TestApplyDesiredState_HashDrift_Recreates(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	// Container has a different hash — drift.
	mock.addContainer(DockerContainer{
		ID:    "old-container",
		Names: []string{"/bahia-22222222-my-api"},
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   "sha256:old-hash-that-differs",
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Full recreate flow.
	if len(mock.pullCalls) != 1 {
		t.Errorf("expected 1 pull, got %d", len(mock.pullCalls))
	}
	if mock.pullCalls[0] != spec.ImageRef {
		t.Errorf("pulled %q, want %q", mock.pullCalls[0], spec.ImageRef)
	}
	if len(mock.stopCalls) != 1 || mock.stopCalls[0] != "old-container" {
		t.Errorf("stop calls = %v, want [old-container]", mock.stopCalls)
	}
	if len(mock.removeCalls) != 1 || mock.removeCalls[0] != "old-container" {
		t.Errorf("remove calls = %v, want [old-container]", mock.removeCalls)
	}
	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create, got %d", len(mock.createCalls))
	}
	if mock.createCalls[0].Name != "bahia-22222222-my-api" {
		t.Errorf("created container name = %q, want bahia-22222222-my-api", mock.createCalls[0].Name)
	}
	if len(mock.startCalls) != 1 {
		t.Errorf("expected 1 start, got %d", len(mock.startCalls))
	}

	if result.DesiredHash != spec.DesiredHash {
		t.Errorf("result hash = %q, want %q", result.DesiredHash, spec.DesiredHash)
	}
}

// ===========================================================================
// Missing container: create fresh
// ===========================================================================

func TestApplyDesiredState_MissingContainer_CreatesFresh(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No stop/remove since no existing container.
	if len(mock.stopCalls) > 0 {
		t.Error("should not stop when no existing container")
	}
	if len(mock.removeCalls) > 0 {
		t.Error("should not remove when no existing container")
	}

	// Should pull and create.
	if len(mock.pullCalls) != 1 {
		t.Errorf("expected 1 pull, got %d", len(mock.pullCalls))
	}
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create, got %d", len(mock.createCalls))
	}
	if len(mock.startCalls) != 1 {
		t.Errorf("expected 1 start, got %d", len(mock.startCalls))
	}

	if result.Renderer != "docker" {
		t.Errorf("renderer = %q, want docker", result.Renderer)
	}
	if result.ExecutionMode != ExecutionModeEngineAPI {
		t.Errorf("execution mode = %q, want %q", result.ExecutionMode, ExecutionModeEngineAPI)
	}
	if len(result.ResourceIDs) != 1 {
		t.Errorf("expected 1 resource ID, got %d", len(result.ResourceIDs))
	}
	if len(result.ResourceNames) != 1 || result.ResourceNames[0] != "bahia-22222222-my-api" {
		t.Errorf("resource names = %v, want [bahia-22222222-my-api]", result.ResourceNames)
	}
}

// ===========================================================================
// Pull policy "never" skips pull
// ===========================================================================

func TestApplyDesiredState_PullNever_SkipsPull(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.PullPolicy = "never"

	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.pullCalls) > 0 {
		t.Error("should not pull when policy is never")
	}
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create, got %d", len(mock.createCalls))
	}
}

// ===========================================================================
// Failure: pull failure with "always" policy is fatal
// ===========================================================================

func TestApplyDesiredState_PullFailure_Always_Fatal(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	mock.failPull = true
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.PullPolicy = "always"

	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for pull failure with always policy")
	}
	if !strings.Contains(err.Error(), "pulling image") {
		t.Errorf("error should mention pulling image, got: %v", err)
	}

	// No create/start should have happened.
	if len(mock.createCalls) > 0 {
		t.Error("should not create after pull failure")
	}
}

// ===========================================================================
// Failure: pull failure with "if-not-present" is a warning
// ===========================================================================

func TestApplyDesiredState_PullFailure_IfNotPresent_Warning(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	mock.failPull = true
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.PullPolicy = "if-not-present"

	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("should not fail for if-not-present pull failure: %v", err)
	}

	// Should have a warning.
	if len(result.Warnings) == 0 {
		t.Error("expected warning about pull failure")
	}
	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "image pull failed") {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings should mention pull failure, got: %v", result.Warnings)
	}

	// Should still create and start.
	if len(mock.createCalls) != 1 {
		t.Errorf("expected 1 create despite pull failure, got %d", len(mock.createCalls))
	}
}

// ===========================================================================
// Failure: stop failure is fatal
// ===========================================================================

func TestApplyDesiredState_StopFailure_Fatal(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	mock.failStop = true
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "old-container",
		Names: []string{"/bahia-22222222-my-api"},
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   "sha256:different",
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for stop failure")
	}
	if !strings.Contains(err.Error(), "stopping container") {
		t.Errorf("error should mention stopping, got: %v", err)
	}
	if len(mock.createCalls) > 0 {
		t.Error("should not create after stop failure")
	}
}

// ===========================================================================
// Failure: remove failure is fatal
// ===========================================================================

func TestApplyDesiredState_RemoveFailure_Fatal(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	mock.failRemove = true
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "old-container",
		Names: []string{"/bahia-22222222-my-api"},
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   "sha256:different",
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for remove failure")
	}
	if !strings.Contains(err.Error(), "removing container") {
		t.Errorf("error should mention removing, got: %v", err)
	}
}

// ===========================================================================
// Failure: create failure is fatal
// ===========================================================================

func TestApplyDesiredState_CreateFailure_Fatal(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	mock.failCreate = true
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for create failure")
	}
	if !strings.Contains(err.Error(), "creating container") {
		t.Errorf("error should mention creating, got: %v", err)
	}
	if len(mock.startCalls) > 0 {
		t.Error("should not start after create failure")
	}
}

// ===========================================================================
// Failure: start failure is fatal
// ===========================================================================

func TestApplyDesiredState_StartFailure_Fatal(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	mock.failStart = true
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for start failure")
	}
	if !strings.Contains(err.Error(), "starting container") {
		t.Errorf("error should mention starting, got: %v", err)
	}
}

// ===========================================================================
// Nil spec returns error
// ===========================================================================

func TestApplyDesiredState_NilSpec_Error(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := DesiredStateApplyRequest{TargetService: nil}
	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err == nil {
		t.Fatal("expected error for nil spec")
	}
	if !strings.Contains(err.Error(), "nil") {
		t.Errorf("error should mention nil, got: %v", err)
	}
}

// ===========================================================================
// Dry run returns preview without mutations
// ===========================================================================

func TestApplyDesiredState_DryRun_NoMutations(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.DryRun = true

	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.DesiredHash != spec.DesiredHash {
		t.Errorf("result hash = %q, want %q", result.DesiredHash, spec.DesiredHash)
	}
	if len(result.Warnings) == 0 || !strings.Contains(result.Warnings[0], "dry-run") {
		t.Errorf("expected dry-run warning, got: %v", result.Warnings)
	}

	// No mutations.
	if len(mock.pullCalls) > 0 || len(mock.createCalls) > 0 || len(mock.startCalls) > 0 {
		t.Error("dry run should not mutate anything")
	}
}

func TestApplyDesiredState_DryRun_ExistingContainer_ShowsRecreate(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	mock.addContainer(DockerContainer{
		ID:    "old-container",
		Names: []string{"/bahia-22222222-my-api"},
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   "sha256:different",
		},
	})

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	req.DryRun = true

	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, w := range result.Warnings {
		if strings.Contains(w, "recreate") {
			found = true
		}
	}
	if !found {
		t.Errorf("dry-run with existing container should mention recreate, got: %v", result.Warnings)
	}
}

// ===========================================================================
// Environment revision is propagated
// ===========================================================================

func TestApplyDesiredState_EnvironmentRevision(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.EnvironmentRevision == "" {
		t.Error("expected non-empty environment revision")
	}
	if result.EnvironmentRevision != req.EnvironmentPlan.RevisionHash {
		t.Errorf("revision = %q, want %q", result.EnvironmentRevision, req.EnvironmentPlan.RevisionHash)
	}
}

func TestApplyDesiredState_NilPlan_EmptyRevision(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := DesiredStateApplyRequest{
		TargetService: spec,
		Secrets:       map[string]string{"DB_PASSWORD": "s3cret", "API_KEY": "key-123"},
	}
	result, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.EnvironmentRevision != "" {
		t.Errorf("expected empty revision for nil plan, got %q", result.EnvironmentRevision)
	}
}

// ===========================================================================
// Network ensure during apply
// ===========================================================================

func TestApplyDesiredState_EnsuresNetworkForCustomNetworkMode(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.NetworkMode = "custom-network"

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Network should have been created.
	if _, ok := mock.networks["custom-network"]; !ok {
		t.Error("expected custom-network to be created")
	}
}

func TestApplyDesiredState_SkipsNetworkEnsureForHostMode(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.NetworkMode = "host"

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.networks) > 0 {
		t.Error("should not create networks for host mode")
	}
}

// ===========================================================================
// Volume ensure during apply
// ===========================================================================

func TestApplyDesiredState_EnsuresNamedVolumes(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()
	spec.Volumes = []string{"app-data:/data", "/host/path:/mnt"}

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only named volume should be ensured, not host bind.
	if _, ok := mock.volumes["app-data"]; !ok {
		t.Error("expected app-data volume to be ensured")
	}
	if len(mock.volumes) != 1 {
		t.Errorf("expected 1 volume, got %d", len(mock.volumes))
	}
}

// ===========================================================================
// Container config is correctly passed to create
// ===========================================================================

func TestApplyDesiredState_ContainerConfigPassedCorrectly(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	spec := applyTestSpec()

	server, observer := setupApplyTest(mock)
	defer server.Close()

	req := applyTestRequest(spec)
	_, err := observer.ApplyDesiredState(context.Background(), req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.createCalls) != 1 {
		t.Fatalf("expected 1 create call, got %d", len(mock.createCalls))
	}

	call := mock.createCalls[0]
	if call.Name != "bahia-22222222-my-api" {
		t.Errorf("container name = %q, want bahia-22222222-my-api", call.Name)
	}

	// Check image is in the body.
	if image, ok := call.Body["Image"].(string); !ok || image != spec.ImageRef {
		t.Errorf("Image = %v, want %s", call.Body["Image"], spec.ImageRef)
	}
}

// ===========================================================================
// AsDesiredStateApplier capability probe
// ===========================================================================

func TestAsDesiredStateApplier_Docker(t *testing.T) {
	t.Parallel()
	observer := &DockerObserver{logger: zap.NewNop()}
	applier, ok := AsDesiredStateApplier(observer)
	if !ok {
		t.Fatal("DockerObserver should be recognized as DesiredStateApplier")
	}
	if applier == nil {
		t.Fatal("applier should not be nil")
	}
}

// ===========================================================================
// Internal helpers
// ===========================================================================

func TestNormalizePullPolicy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		reqPolicy  string
		specPolicy string
		want       string
	}{
		{"always", "never", "always"},     // request overrides spec
		{"", "always", "always"},          // spec used when request empty
		{"", "", "if-not-present"},        // default
		{"ALWAYS", "", "always"},          // case insensitive
		{"", "Never", "never"},            // case insensitive
		{"  always  ", "", "always"},      // trimmed
		{"garbage", "", "if-not-present"}, // unknown → default
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("req=%q_spec=%q", tt.reqPolicy, tt.specPolicy), func(t *testing.T) {
			t.Parallel()
			got := normalizePullPolicy(tt.reqPolicy, tt.specPolicy)
			if got != tt.want {
				t.Errorf("normalizePullPolicy(%q, %q) = %q, want %q",
					tt.reqPolicy, tt.specPolicy, got, tt.want)
			}
		})
	}
}

func TestShouldPull(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		policy       string
		existingHash string
		desiredHash  string
		isMissing    bool
		want         bool
	}{
		{"always", "always", "same", "same", false, true},
		{"never", "never", "", "", true, false},
		{"missing container", "if-not-present", "", "sha256:new", true, true},
		{"hash drift", "if-not-present", "sha256:old", "sha256:new", false, true},
		{"hash match", "if-not-present", "sha256:same", "sha256:same", false, false},
		{"empty existing hash", "if-not-present", "", "sha256:new", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldPull(tt.policy, tt.existingHash, tt.desiredHash, tt.isMissing)
			if got != tt.want {
				t.Errorf("shouldPull() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCollectNetworkSpecs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		networkMode string
		wantLen     int
	}{
		{"", 0},
		{"host", 0},
		{"none", 0},
		{"bridge", 0},
		{"my-custom-network", 1},
	}
	for _, tt := range tests {
		t.Run(tt.networkMode, func(t *testing.T) {
			t.Parallel()
			spec := &domain.DesiredServiceSpec{NetworkMode: tt.networkMode}
			got := collectNetworkSpecs(spec)
			if len(got) != tt.wantLen {
				t.Errorf("collectNetworkSpecs(%q) returned %d specs, want %d", tt.networkMode, len(got), tt.wantLen)
			}
			if tt.wantLen > 0 && got[0].Name != tt.networkMode {
				t.Errorf("network name = %q, want %q", got[0].Name, tt.networkMode)
			}
		})
	}
}

func TestCollectVolumeSpecs(t *testing.T) {
	t.Parallel()
	spec := &domain.DesiredServiceSpec{
		Volumes: []string{
			"named-vol:/data",
			"/host/path:/mnt",
			"./relative:/app",
			"~/home:/home",
			"another-vol:/var/data:ro",
			"",
			"no-colon-path",
		},
	}
	got := collectVolumeSpecs(spec)

	wantNames := map[string]bool{"named-vol": true, "another-vol": true}
	if len(got) != len(wantNames) {
		t.Fatalf("got %d volume specs, want %d: %+v", len(got), len(wantNames), got)
	}
	for _, v := range got {
		if !wantNames[v.Name] {
			t.Errorf("unexpected volume spec: %q", v.Name)
		}
	}
}

func TestEnvironmentRevision_NilPlan(t *testing.T) {
	t.Parallel()
	if got := environmentRevision(nil); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestEnvironmentRevision_WithPlan(t *testing.T) {
	t.Parallel()
	plan := &domain.DesiredEnvironmentPlan{
		EnvironmentID: uuid.New(),
		RevisionHash:  "sha256:abc123",
	}
	if got := environmentRevision(plan); got != "sha256:abc123" {
		t.Errorf("expected sha256:abc123, got %q", got)
	}
}

// ===========================================================================
// Control client delegation
// ===========================================================================

type recordingDockerControlClient struct {
	mode RuntimeExecutionMode

	findSpec       *domain.DesiredServiceSpec
	ensureNetworks []domain.NetworkSpec
	ensureVolumes  []domain.VolumeSpec
	pullImages     []string
	stopIDs        []string
	removeIDs      []string
	createNames    []string
	startIDs       []string
	connectCalls   []connectCall

	existingContainer *DockerContainer
	findResults       []*DockerContainer
	stopErr           error
	findCalls         int
}

func (c *recordingDockerControlClient) ExecutionMode() RuntimeExecutionMode {
	if c.mode != "" {
		return c.mode
	}
	return ExecutionModeEngineAPI
}

func (c *recordingDockerControlClient) FindManagedContainer(_ context.Context, spec *domain.DesiredServiceSpec) (*DockerContainer, error) {
	c.findSpec = spec
	if len(c.findResults) > 0 {
		idx := c.findCalls
		if idx >= len(c.findResults) {
			idx = len(c.findResults) - 1
		}
		c.findCalls++
		return c.findResults[idx], nil
	}
	c.findCalls++
	return c.existingContainer, nil
}

func (c *recordingDockerControlClient) EnsureNetworks(_ context.Context, specs []domain.NetworkSpec) error {
	c.ensureNetworks = append(c.ensureNetworks, specs...)
	return nil
}

func (c *recordingDockerControlClient) EnsureVolumes(_ context.Context, specs []domain.VolumeSpec) error {
	c.ensureVolumes = append(c.ensureVolumes, specs...)
	return nil
}

func (c *recordingDockerControlClient) PullImage(_ context.Context, image string) error {
	c.pullImages = append(c.pullImages, image)
	return nil
}

func (c *recordingDockerControlClient) StopContainer(_ context.Context, containerID string) error {
	c.stopIDs = append(c.stopIDs, containerID)
	return c.stopErr
}

func (c *recordingDockerControlClient) RemoveContainer(_ context.Context, containerID string) error {
	c.removeIDs = append(c.removeIDs, containerID)
	return nil
}

func (c *recordingDockerControlClient) CreateContainer(_ context.Context, name string, _ *DockerContainerConfigs) (string, error) {
	c.createNames = append(c.createNames, name)
	return "delegated-container", nil
}

func (c *recordingDockerControlClient) StartContainer(_ context.Context, containerID string) error {
	c.startIDs = append(c.startIDs, containerID)
	return nil
}

func (c *recordingDockerControlClient) ConnectNetwork(_ context.Context, containerID, networkName, _ string) error {
	c.connectCalls = append(c.connectCalls, connectCall{NetworkName: networkName, ContainerID: containerID})
	return nil
}

func TestApplyDesiredState_DelegatesMutationsToControlClient(t *testing.T) {
	t.Parallel()
	spec := applyTestSpec()
	spec.NetworkMode = "custom-network"
	spec.Volumes = []string{"app-data:/data"}
	control := &recordingDockerControlClient{mode: ExecutionModeEngineAPI}
	observer := &DockerObserver{logger: zap.NewNop(), controlClient: control}

	result, err := observer.ApplyDesiredState(context.Background(), applyTestRequest(spec))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if control.findSpec != spec {
		t.Fatal("expected managed container lookup through control client")
	}
	if len(control.ensureNetworks) != 1 || control.ensureNetworks[0].Name != "custom-network" {
		t.Fatalf("ensure networks = %+v, want custom-network", control.ensureNetworks)
	}
	if len(control.ensureVolumes) != 1 || control.ensureVolumes[0].Name != "app-data" {
		t.Fatalf("ensure volumes = %+v, want app-data", control.ensureVolumes)
	}
	if len(control.pullImages) != 1 || control.pullImages[0] != spec.ImageRef {
		t.Fatalf("pull images = %v, want [%s]", control.pullImages, spec.ImageRef)
	}
	if len(control.createNames) != 1 || control.createNames[0] != BahiaContainerName(spec) {
		t.Fatalf("create names = %v, want [%s]", control.createNames, BahiaContainerName(spec))
	}
	if len(control.startIDs) != 1 || control.startIDs[0] != "delegated-container" {
		t.Fatalf("start IDs = %v, want [delegated-container]", control.startIDs)
	}
	if result.ExecutionMode != ExecutionModeEngineAPI {
		t.Fatalf("execution mode = %q, want %q", result.ExecutionMode, ExecutionModeEngineAPI)
	}
	if len(result.ResourceIDs) != 1 || result.ResourceIDs[0] != "delegated-container" {
		t.Fatalf("resource IDs = %v, want [delegated-container]", result.ResourceIDs)
	}
}

func TestApplyDesiredState_ContinuesWhenStopErrorLeavesContainerExited(t *testing.T) {
	t.Parallel()
	spec := applyTestSpec()
	running := &DockerContainer{
		ID:    "old-container",
		State: "running",
		Labels: map[string]string{
			"bahia.service_id":     testServiceID.String(),
			"bahia.environment_id": testEnvironmentID.String(),
			"bahia.desired_hash":   "sha256:old-hash",
		},
	}
	exited := &DockerContainer{
		ID:     "old-container",
		State:  "exited",
		Labels: running.Labels,
	}
	control := &recordingDockerControlClient{
		findResults: []*DockerContainer{running, exited},
		stopErr:     errors.New("stopping container: context deadline exceeded"),
	}
	observer := &DockerObserver{logger: zap.NewNop(), controlClient: control}

	result, err := observer.ApplyDesiredState(context.Background(), applyTestRequest(spec))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if control.findCalls != 2 {
		t.Fatalf("managed container lookups = %d, want 2", control.findCalls)
	}
	if len(control.stopIDs) != 1 || control.stopIDs[0] != "old-container" {
		t.Fatalf("stop IDs = %v, want [old-container]", control.stopIDs)
	}
	if len(control.removeIDs) != 1 || control.removeIDs[0] != "old-container" {
		t.Fatalf("remove IDs = %v, want [old-container]", control.removeIDs)
	}
	if len(control.createNames) != 1 || len(control.startIDs) != 1 {
		t.Fatalf("create/start calls = %v/%v, want one each", control.createNames, control.startIDs)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "continuing remove/create") {
		t.Fatalf("warnings = %v, want stop recovery warning", result.Warnings)
	}
}

func TestDockerDeploy_DelegatesMutationsToControlClient(t *testing.T) {
	t.Parallel()
	mock := newApplyMockState()
	server, observer := setupApplyTest(mock)
	defer server.Close()
	control := &recordingDockerControlClient{}
	observer.controlClient = control

	err := observer.Deploy(context.Background(), "legacy-service", "example.com/app:1", DeployOptions{
		PullAlways: true,
		Ports:      []string{"8080:80"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(control.pullImages) != 1 || control.pullImages[0] != "example.com/app:1" {
		t.Fatalf("pull images = %v, want [example.com/app:1]", control.pullImages)
	}
	if len(control.createNames) != 1 || control.createNames[0] != "legacy-service" {
		t.Fatalf("create names = %v, want [legacy-service]", control.createNames)
	}
	if len(control.startIDs) != 1 || control.startIDs[0] != "delegated-container" {
		t.Fatalf("start IDs = %v, want [delegated-container]", control.startIDs)
	}
}
