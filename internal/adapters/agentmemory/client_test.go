package agentmemory

import (
	"errors"
	"testing"
)

func TestClientMissingURLIsNotConfigured(t *testing.T) {
	client := NewClient(Config{}, nil)
	if client.Configured() {
		t.Fatal("zero-value agent-memory config should not be configured")
	}
	if err := client.Health(t.Context()); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Health() error = %v, want ErrNotConfigured", err)
	}
	if err := client.RegisterAgent(t.Context(), "agent", "npub", nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("RegisterAgent() error = %v, want ErrNotConfigured", err)
	}
}

func TestClientConfiguredTrimsURL(t *testing.T) {
	client := NewClient(Config{URL: " http://localhost:8282/ "}, nil)
	if !client.Configured() {
		t.Fatal("explicit agent-memory URL should configure client")
	}
	if client.baseURL != "http://localhost:8282" {
		t.Fatalf("baseURL = %q, want trimmed URL", client.baseURL)
	}
}
