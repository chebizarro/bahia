package client

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestNew(t *testing.T) {
	c := New("http://localhost:8080")
	if c == nil {
		t.Fatal("New() returned nil")
	}
	if c.baseURL != "http://localhost:8080" {
		t.Errorf("baseURL = %s, want http://localhost:8080", c.baseURL)
	}
}

func TestSetAuthToken(t *testing.T) {
	c := New("http://localhost:8080")
	c.SetAuthToken("test-token")
	if c.authToken != "test-token" {
		t.Errorf("authToken = %s, want test-token", c.authToken)
	}
}

func TestListServices(t *testing.T) {
	services := []domain.Service{
		{ID: uuid.New(), Name: "svc1"},
		{ID: uuid.New(), Name: "svc2"},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/services" {
			t.Errorf("path = %s, want /api/v1/services", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": services})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.ListServices(context.Background())
	if err != nil {
		t.Fatalf("ListServices() error = %v", err)
	}
	if len(result) != 2 {
		t.Errorf("got %d services, want 2", len(result))
	}
}

func TestGetService(t *testing.T) {
	svc := domain.Service{ID: uuid.New(), Name: "test-service"}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/services/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": svc})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.GetService(context.Background(), svc.ID.String())
	if err != nil {
		t.Fatalf("GetService() error = %v", err)
	}
	if result.Name != "test-service" {
		t.Errorf("Name = %s, want test-service", result.Name)
	}
}

func TestScanAdoption(t *testing.T) {
	var gotBody AdoptionScanRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/adoption/scan" {
			t.Errorf("path = %s, want /api/v1/adoption/scan", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		preview := AdoptionPreview{
			Target: gotBody.Targets[0],
			Containers: []AdoptionPreviewContainer{{
				Discovered: DiscoveredContainer{
					HealthStatus:            "healthy",
					Compose:                 &ComposeMetadata{ProjectName: "legacy"},
					Environment:             map[string]string{"APP_ENV": "prod"},
					RedactedEnvironmentKeys: []string{"DB_PASSWORD"},
					RedactedLabelKeys:       []string{"secret-token"},
				},
				ProposedServiceName: "legacy-api",
				Adoptable:           true,
			}},
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []AdoptionPreview{preview}})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.ScanAdoption(context.Background(), AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "local", DockerHost: "unix:///docker.sock", EnvironmentName: "prod"}}})
	if err != nil {
		t.Fatalf("ScanAdoption() error = %v", err)
	}
	if len(gotBody.Targets) != 1 || gotBody.Targets[0].Name != "local" {
		t.Fatalf("unexpected request body: %#v", gotBody)
	}
	if len(result) != 1 || result[0].Containers[0].ProposedServiceName != "legacy-api" {
		t.Fatalf("unexpected result: %#v", result)
	}
	discovered := result[0].Containers[0].Discovered
	if discovered.HealthStatus != "healthy" || discovered.Compose.ProjectName != "legacy" {
		t.Fatalf("unexpected discovered response shape: %#v", discovered)
	}
	if discovered.Environment["APP_ENV"] != "prod" || len(discovered.RedactedEnvironmentKeys) != 1 || discovered.RedactedEnvironmentKeys[0] != "DB_PASSWORD" || len(discovered.RedactedLabelKeys) != 1 {
		t.Fatalf("redacted discovered fields not decoded: %#v", discovered)
	}
}

func TestImportAdoption(t *testing.T) {
	var gotBody AdoptionImportRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/adoption/import" {
			t.Errorf("path = %s, want /api/v1/adoption/import", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		result := AdoptionImportResult{
			TargetName:              "local",
			ContainerID:             "abc123",
			ServiceName:             "api",
			Status:                  "created",
			RedactedEnvironmentKeys: []string{"DB_PASSWORD"},
			RedactedLabelKeys:       []string{"secret-token"},
		}
		json.NewEncoder(w).Encode(map[string]any{"data": []AdoptionImportResult{result}})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.ImportAdoption(context.Background(), AdoptionImportRequest{
		Targets:    []AdoptionTarget{{Name: "local", DockerHost: "unix:///docker.sock"}},
		Selections: []AdoptionSelection{{TargetName: "local", ContainerID: "abc123", ServiceNameOverride: "api"}},
	})
	if err != nil {
		t.Fatalf("ImportAdoption() error = %v", err)
	}
	if len(gotBody.Selections) != 1 || gotBody.Selections[0].ServiceNameOverride != "api" {
		t.Fatalf("unexpected request body: %#v", gotBody)
	}
	if len(result) != 1 || result[0].Status != "created" {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(result[0].RedactedEnvironmentKeys) != 1 || result[0].RedactedEnvironmentKeys[0] != "DB_PASSWORD" || len(result[0].RedactedLabelKeys) != 1 {
		t.Fatalf("redacted import fields not decoded: %#v", result[0])
	}
}

func TestPrivilegedMethodsSendBearerToken(t *testing.T) {
	wantToken := "Bearer operator-token"
	seen := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Method+" "+r.URL.Path] = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/adoption/scan":
			json.NewEncoder(w).Encode(map[string]any{"data": []AdoptionPreview{}})
		case "/api/v1/adoption/import":
			json.NewEncoder(w).Encode(map[string]any{"data": []AdoptionImportResult{}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"data": RuntimeActionResult{Action: r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:], ServiceID: "svc", EnvironmentID: "env"}})
		}
	}))
	defer server.Close()

	c := New(server.URL)
	c.SetAuthToken("operator-token")
	ctx := context.Background()
	if _, err := c.ScanAdoption(ctx, AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker"}}}); err != nil {
		t.Fatalf("ScanAdoption() error = %v", err)
	}
	if _, err := c.ImportAdoption(ctx, AdoptionImportRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker"}}, ImportAll: true}); err != nil {
		t.Fatalf("ImportAdoption() error = %v", err)
	}
	if _, err := c.DeployServiceRuntime(ctx, "svc", "env", nil); err != nil {
		t.Fatalf("DeployServiceRuntime() error = %v", err)
	}
	if _, err := c.RestartServiceRuntime(ctx, "svc", "env"); err != nil {
		t.Fatalf("RestartServiceRuntime() error = %v", err)
	}
	if _, err := c.StopServiceRuntime(ctx, "svc", "env"); err != nil {
		t.Fatalf("StopServiceRuntime() error = %v", err)
	}

	for _, key := range []string{
		"POST /api/v1/adoption/scan",
		"POST /api/v1/adoption/import",
		"POST /api/v1/services/svc/environments/env/deploy",
		"POST /api/v1/services/svc/environments/env/restart",
		"POST /api/v1/services/svc/environments/env/stop",
	} {
		if seen[key] != wantToken {
			t.Fatalf("%s Authorization = %q, want %q (all seen: %#v)", key, seen[key], wantToken, seen)
		}
	}
}

func TestRuntimeActionMethods(t *testing.T) {
	artifactID := uuid.NewString()
	var paths []string
	var deployBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/deploy") {
			if err := json.NewDecoder(r.Body).Decode(&deployBody); err != nil {
				t.Fatalf("decoding deploy request: %v", err)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": RuntimeActionResult{
			Action:        strings.TrimPrefix(r.URL.Path[strings.LastIndex(r.URL.Path, "/"):], "/"),
			ServiceID:     "svc",
			EnvironmentID: "env",
			Observation:   &RuntimeObservation{ID: "obs", ServiceID: "svc", EnvironmentID: "env", HealthStatus: "healthy"},
		}})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.DeployServiceRuntime(context.Background(), "svc", "env", &artifactID)
	if err != nil {
		t.Fatalf("DeployServiceRuntime() error = %v", err)
	}
	if result.Observation == nil || result.Observation.HealthStatus != "healthy" {
		t.Fatalf("unexpected runtime action response shape: %#v", result)
	}
	if _, err := c.RestartServiceRuntime(context.Background(), "svc", "env"); err != nil {
		t.Fatalf("RestartServiceRuntime() error = %v", err)
	}
	if _, err := c.StopServiceRuntime(context.Background(), "svc", "env"); err != nil {
		t.Fatalf("StopServiceRuntime() error = %v", err)
	}
	wantPaths := []string{
		"/api/v1/services/svc/environments/env/deploy",
		"/api/v1/services/svc/environments/env/restart",
		"/api/v1/services/svc/environments/env/stop",
	}
	for i, want := range wantPaths {
		if paths[i] != want {
			t.Fatalf("path[%d] = %s, want %s", i, paths[i], want)
		}
	}
	if deployBody["artifact_id"] != artifactID {
		t.Fatalf("artifact_id = %q, want %q", deployBody["artifact_id"], artifactID)
	}
}

func TestCreateService(t *testing.T) {
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/services" {
			t.Errorf("path = %s, want /api/v1/services", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		svc := domain.Service{ID: uuid.New(), Name: gotBody["name"], ArtifactRepo: gotBody["artifact_repo"], RuntimeType: domain.RuntimeType(gotBody["runtime_type"])}
		json.NewEncoder(w).Encode(map[string]any{"data": svc})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.CreateService(context.Background(), "api", "registry.example/api", domain.RuntimeTypeCompose)
	if err != nil {
		t.Fatalf("CreateService() error = %v", err)
	}
	if gotBody["name"] != "api" {
		t.Errorf("name = %s, want api", gotBody["name"])
	}
	if gotBody["artifact_repo"] != "registry.example/api" {
		t.Errorf("artifact_repo = %s, want registry.example/api", gotBody["artifact_repo"])
	}
	if gotBody["runtime_type"] != string(domain.RuntimeTypeCompose) {
		t.Errorf("runtime_type = %s, want %s", gotBody["runtime_type"], domain.RuntimeTypeCompose)
	}
	if result.Name != "api" {
		t.Errorf("Name = %s, want api", result.Name)
	}
}

func TestGetEnvironment(t *testing.T) {
	env := domain.Environment{ID: uuid.New(), Name: "prod", DeployStrategy: domain.DeployStrategyCanary, Protected: true}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/environments/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": env})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.GetEnvironment(context.Background(), env.ID.String())
	if err != nil {
		t.Fatalf("GetEnvironment() error = %v", err)
	}
	if result.Name != "prod" {
		t.Errorf("Name = %s, want prod", result.Name)
	}
	if !result.Protected {
		t.Error("Protected = false, want true")
	}
}

func TestCreateEnvironment(t *testing.T) {
	var gotBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/v1/environments" {
			t.Errorf("path = %s, want /api/v1/environments", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decoding request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		env := domain.Environment{ID: uuid.New(), Name: gotBody["name"].(string), DeployStrategy: domain.DeployStrategy(gotBody["deploy_strategy"].(string)), Protected: gotBody["protected"].(bool)}
		json.NewEncoder(w).Encode(map[string]any{"data": env})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.CreateEnvironment(context.Background(), "staging", domain.DeployStrategyReplace, true)
	if err != nil {
		t.Fatalf("CreateEnvironment() error = %v", err)
	}
	if gotBody["name"] != "staging" {
		t.Errorf("name = %v, want staging", gotBody["name"])
	}
	if gotBody["deploy_strategy"] != string(domain.DeployStrategyReplace) {
		t.Errorf("deploy_strategy = %v, want %s", gotBody["deploy_strategy"], domain.DeployStrategyReplace)
	}
	if gotBody["protected"] != true {
		t.Errorf("protected = %v, want true", gotBody["protected"])
	}
	if result.Name != "staging" {
		t.Errorf("Name = %s, want staging", result.Name)
	}
}

func TestAPIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
	}))
	defer server.Close()

	c := New(server.URL)
	_, err := c.ListServices(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %v, want to contain 'not found'", err)
	}
}

func TestAuthTokenHeader(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []domain.Service{}})
	}))
	defer server.Close()

	c := New(server.URL)
	c.SetAuthToken("my-token")
	_, _ = c.ListServices(context.Background())

	if gotAuth != "Bearer my-token" {
		t.Errorf("Authorization = %s, want Bearer my-token", gotAuth)
	}
}

func TestListWorkers(t *testing.T) {
	workers := []Worker{
		{Pubkey: "abc123", Name: "worker1", PricePerSec: 10},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/workers" {
			t.Errorf("path = %s, want /api/v1/workers", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": workers})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.ListWorkers(context.Background())
	if err != nil {
		t.Fatalf("ListWorkers() error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d workers, want 1", len(result))
	}
	if result[0].Name != "worker1" {
		t.Errorf("Name = %s, want worker1", result[0].Name)
	}
}

func TestGetRunLogs(t *testing.T) {
	logs := RunLogs{
		RunID:  "run-123",
		Stdout: "output",
		Stderr: "errors",
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/api/v1/deployments/runs/") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.RawQuery, "tail=50") {
			t.Errorf("query = %s, want tail=50", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": logs})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.GetRunLogs(context.Background(), "run-123", 50, "")
	if err != nil {
		t.Fatalf("GetRunLogs() error = %v", err)
	}
	if result.Stdout != "output" {
		t.Errorf("Stdout = %s, want output", result.Stdout)
	}
}

func TestListSecrets(t *testing.T) {
	secrets := []SecretRef{
		{ID: "secret-1", Name: "API_KEY", Version: 1},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": secrets})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.ListSecrets(context.Background(), "svc-123")
	if err != nil {
		t.Fatalf("ListSecrets() error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d secrets, want 1", len(result))
	}
}

func TestSetSecret(t *testing.T) {
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": SecretRef{ID: "new-secret", Name: gotBody["name"], Version: 1}})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.SetSecret(context.Background(), "svc-123", "API_KEY", "secret-value", "env-123")
	if err != nil {
		t.Fatalf("SetSecret() error = %v", err)
	}
	if result.Name != "API_KEY" {
		t.Errorf("Name = %s, want API_KEY", result.Name)
	}
	if gotBody["environment_id"] != "env-123" {
		t.Errorf("environment_id = %s, want env-123", gotBody["environment_id"])
	}
}

func TestCreateOrg(t *testing.T) {
	var gotBody map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		org := domain.Organization{ID: uuid.New(), Name: gotBody["name"], DisplayName: gotBody["display_name"]}
		json.NewEncoder(w).Encode(map[string]any{"data": org})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.CreateOrg(context.Background(), "my-org", "My Organization")
	if err != nil {
		t.Fatalf("CreateOrg() error = %v", err)
	}
	if result.Name != "my-org" {
		t.Errorf("Name = %s, want my-org", result.Name)
	}
	if result.DisplayName != "My Organization" {
		t.Errorf("DisplayName = %s, want My Organization", result.DisplayName)
	}
}

func TestListOrgMembers(t *testing.T) {
	members := []domain.OrgMember{
		{Pubkey: "abc", Role: domain.RoleAdmin},
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": members})
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.ListOrgMembers(context.Background(), "org-123")
	if err != nil {
		t.Fatalf("ListOrgMembers() error = %v", err)
	}
	if len(result) != 1 {
		t.Errorf("got %d members, want 1", len(result))
	}
	if result[0].Role != domain.RoleAdmin {
		t.Errorf("Role = %s, want admin", result[0].Role)
	}
}

func TestStreamEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "text/event-stream" {
			t.Errorf("Accept = %s, want text/event-stream", r.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		// Send a test event
		w.Write([]byte("event: test.event\n"))
		w.Write([]byte(`data: {"type":"test.event","entity_id":"123","data":{"key":"value"}}` + "\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	c := New(server.URL)
	c.httpClient.Timeout = 2 * time.Second

	var received []Event
	var mu sync.Mutex

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_ = c.StreamEvents(ctx, []string{"test.event"}, func(ev Event) {
		mu.Lock()
		received = append(received, ev)
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Errorf("received %d events, want 1", len(received))
	}
	if len(received) > 0 && received[0].EntityID != "123" {
		t.Errorf("EntityID = %s, want 123", received[0].EntityID)
	}
}

func TestStreamLiveLogs(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/logs") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher := w.(http.Flusher)

		w.Write([]byte(`data: {"timestamp":"2024-01-01T00:00:00Z","stream":"stdout","message":"hello"}` + "\n\n"))
		flusher.Flush()
	}))
	defer server.Close()

	c := New(server.URL)
	c.httpClient.Timeout = 2 * time.Second

	var received []LogLine
	var mu sync.Mutex

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_ = c.StreamLiveLogs(ctx, "svc-123", "env-456", 100, func(line LogLine) {
		mu.Lock()
		received = append(received, line)
		mu.Unlock()
	})

	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Errorf("received %d lines, want 1", len(received))
	}
	if len(received) > 0 && received[0].Message != "hello" {
		t.Errorf("Message = %s, want hello", received[0].Message)
	}
}
