package qdrant

import (
	"errors"
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
	client := NewClient(Config{URL: " http://localhost:6333/ "}, nil)
	if !client.Configured() {
		t.Fatal("explicit qdrant URL should configure client")
	}
	if client.baseURL != "http://localhost:6333" {
		t.Fatalf("baseURL = %q, want trimmed URL", client.baseURL)
	}
}
