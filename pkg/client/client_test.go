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
