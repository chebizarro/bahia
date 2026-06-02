package client

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nbd-wtf/go-nostr"
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

func TestSetAuthorizationProvider(t *testing.T) {
	c := New("http://localhost:8080")
	provider := staticAuthorizationProvider("Nostr test")
	c.SetAuthorizationProvider(provider)
	if c.authorizationProvider == nil {
		t.Fatal("authorizationProvider was not set")
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

func TestAdoptionAndDirectRuntimeRestMutationsRemoved(t *testing.T) {
	tests := []struct {
		name     string
		call     func(*Client) (any, error)
		guidance string
	}{
		{
			name: "scan adoption",
			call: func(c *Client) (any, error) {
				return c.ScanAdoption(context.Background(), AdoptionScanRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker"}}})
			},
			guidance: "Nostr AdoptionScanRequest",
		},
		{
			name: "import adoption",
			call: func(c *Client) (any, error) {
				return c.ImportAdoption(context.Background(), AdoptionImportRequest{Targets: []AdoptionTarget{{Name: "prod", EndpointRef: "prod-docker"}}, ImportAll: true})
			},
			guidance: "Nostr AdoptionImportRequest",
		},
		{
			name: "deploy direct runtime",
			call: func(c *Client) (any, error) {
				artifactID := uuid.NewString()
				return c.DeployServiceRuntime(context.Background(), "svc", "env", &artifactID)
			},
			guidance: "Nostr DeployRequest",
		},
		{
			name: "restart direct runtime",
			call: func(c *Client) (any, error) {
				return c.RestartServiceRuntime(context.Background(), "svc", "env")
			},
			guidance: "Nostr ServiceAction",
		},
		{
			name: "stop direct runtime",
			call: func(c *Client) (any, error) {
				return c.StopServiceRuntime(context.Background(), "svc", "env")
			},
			guidance: "Nostr ServiceAction",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			called := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				t.Fatalf("%s must not call removed REST endpoint %s %s", tt.name, r.Method, r.URL.Path)
			}))
			defer server.Close()

			c := New(server.URL)
			result, err := tt.call(c)
			if err == nil {
				t.Fatalf("%s expected REST deprecation error", tt.name)
			}
			if !isNilClientResult(result) {
				t.Fatalf("%s result = %#v, want nil", tt.name, result)
			}
			if !strings.Contains(err.Error(), tt.guidance) {
				t.Fatalf("%s error = %q, want %s guidance", tt.name, err.Error(), tt.guidance)
			}
			if called {
				t.Fatalf("%s called removed REST endpoint", tt.name)
			}
		})
	}
}

func isNilClientResult(result any) bool {
	if result == nil {
		return true
	}
	value := reflect.ValueOf(result)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func TestCreateServiceRestMutationRemoved(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("CreateService must not call removed REST endpoint %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.CreateService(context.Background(), "api", "registry.example/api", domain.RuntimeTypeCompose)
	if err == nil {
		t.Fatal("CreateService() expected REST deprecation error")
	}
	if result != nil {
		t.Fatalf("CreateService() result = %#v, want nil", result)
	}
	if !strings.Contains(err.Error(), "Nostr ServiceCreate") {
		t.Fatalf("CreateService() error = %q, want Nostr ServiceCreate guidance", err.Error())
	}
	if called {
		t.Fatal("CreateService called removed REST endpoint")
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

func TestCreateEnvironmentRestMutationRemoved(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("CreateEnvironment must not call removed REST endpoint %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.CreateEnvironment(context.Background(), "staging", domain.DeployStrategyReplace, true)
	if err == nil {
		t.Fatal("CreateEnvironment() expected REST deprecation error")
	}
	if result != nil {
		t.Fatalf("CreateEnvironment() result = %#v, want nil", result)
	}
	if !strings.Contains(err.Error(), "Nostr EnvironmentCreate") {
		t.Fatalf("CreateEnvironment() error = %q, want Nostr EnvironmentCreate guidance", err.Error())
	}
	if called {
		t.Fatal("CreateEnvironment called removed REST endpoint")
	}
}

func TestCreateDeploymentIntentRestMutationRemoved(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("CreateDeploymentIntent must not call removed REST endpoint %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.CreateDeploymentIntent(context.Background(), "svc", "env", "artifact", "deployer")
	if err == nil {
		t.Fatal("CreateDeploymentIntent() expected REST deprecation error")
	}
	if result != nil {
		t.Fatalf("CreateDeploymentIntent() result = %#v, want nil", result)
	}
	if !strings.Contains(err.Error(), "Nostr DeployRequest") {
		t.Fatalf("CreateDeploymentIntent() error = %q, want Nostr DeployRequest guidance", err.Error())
	}
	if called {
		t.Fatal("CreateDeploymentIntent called removed REST endpoint")
	}
}

func TestRollbackRestMutationRemoved(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("Rollback must not call removed REST endpoint %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.Rollback(context.Background(), "svc", "env", "operator")
	if err == nil {
		t.Fatal("Rollback() expected REST deprecation error")
	}
	if result != nil {
		t.Fatalf("Rollback() result = %#v, want nil", result)
	}
	if !strings.Contains(err.Error(), "Nostr RollbackRequest") {
		t.Fatalf("Rollback() error = %q, want Nostr RollbackRequest guidance", err.Error())
	}
	if called {
		t.Fatal("Rollback called removed REST endpoint")
	}
}

func TestCreatePolicyRestMutationRemoved(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("CreatePolicy must not call removed REST endpoint %s %s", r.Method, r.URL.Path)
	}))
	defer server.Close()

	c := New(server.URL)
	result, err := c.CreatePolicy(context.Background(), "require-sbom", "", map[string]any{"type": "require_sbom"}, "block", true)
	if err == nil {
		t.Fatal("CreatePolicy() expected REST deprecation error")
	}
	if result != nil {
		t.Fatalf("CreatePolicy() result = %#v, want nil", result)
	}
	if !strings.Contains(err.Error(), "Nostr PolicyCreate") {
		t.Fatalf("CreatePolicy() error = %q, want Nostr PolicyCreate guidance", err.Error())
	}
	if called {
		t.Fatal("CreatePolicy called removed REST endpoint")
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

func TestNIP98AuthorizationHeader(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"data": []domain.Service{}})
	}))
	defer server.Close()

	provider, err := NewNIP98PrivateKeyProvider(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("NewNIP98PrivateKeyProvider() error = %v", err)
	}
	c := New(server.URL)
	c.SetAuthorizationProvider(provider)
	_, _ = c.ListServices(context.Background())

	event := decodeNIP98Header(t, gotAuth)
	assertNIP98Event(t, event, http.MethodGet, server.URL+"/api/v1/services")
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

func TestAuthorizationProviderErrorPreventsRequest(t *testing.T) {
	providerErr := errors.New("signer unavailable")
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	c := New(server.URL)
	c.SetAuthorizationProvider(errorAuthorizationProvider{err: providerErr})
	_, err := c.ListServices(context.Background())
	if !errors.Is(err, providerErr) {
		t.Fatalf("ListServices() error = %v, want %v", err, providerErr)
	}
	if called {
		t.Fatal("server was called after authorization provider failed")
	}
}

func TestNIP98ProviderCreatesFreshEventPerRequest(t *testing.T) {
	provider, err := NewNIP98PrivateKeyProvider(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("NewNIP98PrivateKeyProvider() error = %v", err)
	}
	provider.Clock = func() time.Time { return time.Unix(1700000000, 0) }

	first, err := provider.AuthorizationHeader(context.Background(), http.MethodGet, "https://example.com/api")
	if err != nil {
		t.Fatalf("first AuthorizationHeader() error = %v", err)
	}
	second, err := provider.AuthorizationHeader(context.Background(), http.MethodGet, "https://example.com/api")
	if err != nil {
		t.Fatalf("second AuthorizationHeader() error = %v", err)
	}
	if first == second {
		t.Fatal("NIP-98 provider reused an authorization header")
	}
	firstEvent := decodeNIP98Header(t, first)
	secondEvent := decodeNIP98Header(t, second)
	if firstEvent.ID == secondEvent.ID {
		t.Fatalf("NIP-98 provider reused event ID %s", firstEvent.ID)
	}
	assertNIP98Event(t, firstEvent, http.MethodGet, "https://example.com/api")
	assertNIP98Event(t, secondEvent, http.MethodGet, "https://example.com/api")
}

type staticAuthorizationProvider string

func (p staticAuthorizationProvider) AuthorizationHeader(ctx context.Context, method, absoluteURL string) (string, error) {
	return string(p), nil
}

type errorAuthorizationProvider struct {
	err error
}

func (p errorAuthorizationProvider) AuthorizationHeader(ctx context.Context, method, absoluteURL string) (string, error) {
	return "", p.err
}

func decodeNIP98Header(t *testing.T, header string) nostr.Event {
	t.Helper()
	if !strings.HasPrefix(header, "Nostr ") {
		t.Fatalf("Authorization header = %q, want Nostr scheme", header)
	}
	eventJSON, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(header, "Nostr "))
	if err != nil {
		t.Fatalf("decode NIP-98 event: %v", err)
	}
	var event nostr.Event
	if err := json.Unmarshal(eventJSON, &event); err != nil {
		t.Fatalf("unmarshal NIP-98 event: %v", err)
	}
	return event
}

func assertNIP98Event(t *testing.T, event nostr.Event, method, absoluteURL string) {
	t.Helper()
	if event.Kind != 27235 {
		t.Fatalf("event.Kind = %d, want 27235", event.Kind)
	}
	if !event.CheckID() {
		t.Fatal("event ID does not match serialized event")
	}
	ok, err := event.CheckSignature()
	if err != nil || !ok {
		t.Fatalf("event signature valid = %v, err = %v", ok, err)
	}
	if got := event.Tags.GetFirst([]string{"u"}); got == nil || got.Value() != absoluteURL {
		t.Fatalf("u tag = %v, want %s", got, absoluteURL)
	}
	if got := event.Tags.GetFirst([]string{"method"}); got == nil || got.Value() != method {
		t.Fatalf("method tag = %v, want %s", got, method)
	}
}

func TestStreamLiveLogs(t *testing.T) {
	var gotAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
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

	provider, err := NewNIP98PrivateKeyProvider(nostr.GeneratePrivateKey())
	if err != nil {
		t.Fatalf("NewNIP98PrivateKeyProvider() error = %v", err)
	}
	c := New(server.URL)
	c.SetAuthorizationProvider(provider)
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
	event := decodeNIP98Header(t, gotAuth)
	assertNIP98Event(t, event, http.MethodGet, server.URL+"/api/v1/services/svc-123/environments/env-456/logs?follow=true&tail=100")
}
