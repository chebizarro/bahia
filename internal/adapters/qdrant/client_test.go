package qdrant

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestClientMissingURLIsNotConfigured(t *testing.T) {
	client := NewClient(Config{}, nil)
	if client.Configured() {
		t.Fatal("zero-value qdrant config should not be configured")
	}
	if err := client.Health(t.Context()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Health() error = %v, want ErrNotConfigured", err)
	}
	if err := client.CreateCollection(t.Context(), "agents", DefaultCollectionConfig()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("CreateCollection() error = %v, want ErrNotConfigured", err)
	}
	if _, err := client.Search(t.Context(), "agents", []float32{1}, 1); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Search() error = %v, want ErrNotConfigured", err)
	}
}

func TestClientConfiguredTrimsURL(t *testing.T) {
	client := NewClient(Config{URL: " http://localhost:6333/ ", APIKey: "secret"}, nil)
	if !client.Configured() {
		t.Fatal("explicit qdrant URL should configure client")
	}
}

func TestClientConfiguredURLWithoutAuthFailsClosed(t *testing.T) {
	client := NewClient(Config{URL: "http://localhost:6333"}, nil)
	if err := client.Health(t.Context()); err == nil || !strings.Contains(err.Error(), "api_key is required") {
		t.Fatalf("Health() error = %v, want api_key required", err)
	}
}

func TestClientAllowsExplicitUnauthenticatedLocalMode(t *testing.T) {
	client := NewClient(Config{URL: "http://localhost:6333", AllowUnauthenticatedLocal: true}, nil)
	if err := client.requireConfigured(); err != nil {
		t.Fatalf("requireConfigured() error = %v", err)
	}
}

func TestClientRejectsUnauthenticatedRemoteMode(t *testing.T) {
	client := NewClient(Config{URL: "https://qdrant.example.com", AllowUnauthenticatedLocal: true}, nil)
	if err := client.Health(t.Context()); err == nil || !strings.Contains(err.Error(), "api_key is required") {
		t.Fatalf("Health() error = %v, want api_key required", err)
	}
}

func TestClientAttachesAPIKeyHeaderToRequests(t *testing.T) {
	paths := make(map[string]string)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths[r.Method+" "+r.URL.Path] = r.Header.Get("api-key")
		switch r.URL.Path {
		case "/healthz":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`ok`))
		case "/collections/agents":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":true}`))
		case "/collections/agents/points":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":{"operation_id":1,"status":"completed"}}`))
		case "/collections/agents/points/search":
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":[]}`))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClient(Config{URL: server.URL, APIKey: "secret-key"}, nil)
	if err := client.Health(t.Context()); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
	if err := client.CreateCollection(t.Context(), "agents", DefaultCollectionConfig()); err != nil {
		t.Fatalf("CreateCollection() error = %v", err)
	}
	if exists, err := client.CollectionExists(t.Context(), "agents"); err != nil || !exists {
		t.Fatalf("CollectionExists() = (%t, %v), want true nil", exists, err)
	}
	if err := client.UpsertPoints(t.Context(), "agents", []Point{{ID: "p1", Vector: []float32{1}}}); err != nil {
		t.Fatalf("UpsertPoints() error = %v", err)
	}
	if _, err := client.Search(t.Context(), "agents", []float32{1}, 1); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if err := client.DeleteCollection(t.Context(), "agents"); err != nil {
		t.Fatalf("DeleteCollection() error = %v", err)
	}

	for req, got := range paths {
		if got != "secret-key" {
			t.Fatalf("%s api-key header = %q, want secret-key; all paths=%#v", req, got, paths)
		}
	}
}
