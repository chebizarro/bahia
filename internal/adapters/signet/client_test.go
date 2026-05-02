package signet

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nbd-wtf/go-nostr"
)

func TestNewClient(t *testing.T) {
	client, err := NewClient(Config{}, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client == nil {
		t.Fatal("NewClient() returned nil")
	}
	if client.clientSecretKey == "" {
		t.Error("NewClient() should generate client secret key")
	}
}

func TestNewClient_WithSecretKey(t *testing.T) {
	sk := nostr.GeneratePrivateKey()
	client, err := NewClient(Config{ClientSecretKey: sk}, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if client.clientSecretKey != sk {
		t.Error("NewClient() should use provided secret key")
	}
}

func TestClient_ConnectRequiresBunkerUnlessMockExplicitlyEnabled(t *testing.T) {
	client, err := NewClient(Config{}, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	err = client.Connect(context.Background())
	if !errors.Is(err, ErrNoBunkerConfigured) {
		t.Fatalf("Connect() error = %v, want ErrNoBunkerConfigured", err)
	}
	if client.IsConnected() {
		t.Error("client should remain disconnected when bunker config is missing")
	}
	if client.IsMockMode() {
		t.Error("client should not enter mock mode unless explicitly enabled")
	}
}

func TestClient_OperationsFailWhenNotConnected(t *testing.T) {
	client, err := NewClient(Config{}, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	ctx := context.Background()
	event := newTestEvent()

	if _, _, _, err := client.ProvisionAgent(ctx, "test-agent", []int{1}); !errors.Is(err, ErrNotConnected) {
		t.Errorf("ProvisionAgent() error = %v, want ErrNotConnected", err)
	}
	if err := client.Sign(ctx, event); !errors.Is(err, ErrNotConnected) {
		t.Errorf("Sign() error = %v, want ErrNotConnected", err)
	}
	if err := client.SignAs(ctx, "test-agent", event); !errors.Is(err, ErrNotConnected) {
		t.Errorf("SignAs() error = %v, want ErrNotConnected", err)
	}
	if err := client.RevokeAgent(ctx, strings.Repeat("a", 64)); !errors.Is(err, ErrNotConnected) {
		t.Errorf("RevokeAgent() error = %v, want ErrNotConnected", err)
	}
	if err := client.SuspendAgent(ctx, strings.Repeat("a", 64)); !errors.Is(err, ErrNotConnected) {
		t.Errorf("SuspendAgent() error = %v, want ErrNotConnected", err)
	}
	if err := client.ResumeAgent(ctx, strings.Repeat("a", 64)); !errors.Is(err, ErrNotConnected) {
		t.Errorf("ResumeAgent() error = %v, want ErrNotConnected", err)
	}
	if _, err := client.GetAgentStatus(ctx, strings.Repeat("a", 64)); !errors.Is(err, ErrNotConnected) {
		t.Errorf("GetAgentStatus() error = %v, want ErrNotConnected", err)
	}
	if _, err := client.GetPublicKey(ctx); !errors.Is(err, ErrNotConnected) {
		t.Errorf("GetPublicKey() error = %v, want ErrNotConnected", err)
	}
	if _, err := client.SignNIP98(ctx, "https://example.com/upload", "PUT", "abc123"); !errors.Is(err, ErrNotConnected) {
		t.Errorf("SignNIP98() error = %v, want ErrNotConnected", err)
	}
}

func TestClient_ExplicitMockMode(t *testing.T) {
	client := newConnectedMockClient(t)

	if !client.IsMockMode() {
		t.Error("client should be in explicit mock mode")
	}
	if !client.IsConnected() {
		t.Error("client should be connected after Connect()")
	}
}

func TestClient_ProvisionAgent_ExplicitMockMode(t *testing.T) {
	client := newConnectedMockClient(t)
	ctx := context.Background()

	pubkey, npub, bunkerURI, err := client.ProvisionAgent(ctx, "test-agent", []int{1, 30023})
	if err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	if len(pubkey) != 64 {
		t.Errorf("pubkey length = %d, want 64", len(pubkey))
	}
	if npub == "" {
		t.Error("npub should not be empty")
	}
	if !strings.HasPrefix(bunkerURI, "mock-bunker://") {
		t.Errorf("bunkerURI = %q, want identifiable mock-bunker URI", bunkerURI)
	}

	agents, err := client.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if len(agents) != 1 {
		t.Fatalf("ListAgents() count = %d, want 1", len(agents))
	}
	if agents[0].AgentID != "test-agent" {
		t.Errorf("agent ID = %s, want test-agent", agents[0].AgentID)
	}
}

func TestClient_Sign_ExplicitMockMode(t *testing.T) {
	client := newConnectedMockClient(t)
	ctx := context.Background()
	wantPubkey, err := client.GetPublicKey(ctx)
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}

	event := newTestEvent()
	if err := client.Sign(ctx, event); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	assertValidSignature(t, event)
	if event.PubKey != wantPubkey {
		t.Errorf("event.PubKey = %s, want explicit mock client pubkey %s", event.PubKey, wantPubkey)
	}

	secondEvent := newTestEvent()
	if err := client.Sign(ctx, secondEvent); err != nil {
		t.Fatalf("second Sign() error = %v", err)
	}
	assertValidSignature(t, secondEvent)
	if secondEvent.PubKey != wantPubkey {
		t.Errorf("second event.PubKey = %s, want stable explicit mock client pubkey %s", secondEvent.PubKey, wantPubkey)
	}
}

func TestClient_SignAs_ExplicitMockMode(t *testing.T) {
	client := newConnectedMockClient(t)
	ctx := context.Background()

	pubkey, _, _, err := client.ProvisionAgent(ctx, "test-agent", nil)
	if err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	event := newTestEvent()
	if err := client.SignAs(ctx, "test-agent", event); err != nil {
		t.Fatalf("SignAs() error = %v", err)
	}

	assertValidSignature(t, event)
	if event.PubKey != pubkey {
		t.Errorf("event.PubKey = %s, want %s", event.PubKey, pubkey)
	}
}

func TestClient_SignAs_NotFound(t *testing.T) {
	client := newConnectedMockClient(t)
	err := client.SignAs(context.Background(), "nonexistent", newTestEvent())
	if !errors.Is(err, ErrAgentNotFound) {
		t.Fatalf("SignAs() error = %v, want ErrAgentNotFound", err)
	}
}

func TestClient_RevokeAgent_ExplicitMockMode(t *testing.T) {
	client := newConnectedMockClient(t)
	ctx := context.Background()

	pubkey, _, _, err := client.ProvisionAgent(ctx, "test-agent", nil)
	if err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	if err := client.RevokeAgent(ctx, pubkey); err != nil {
		t.Fatalf("RevokeAgent() error = %v", err)
	}

	agents, err := client.ListAgents(ctx)
	if err != nil {
		t.Fatalf("ListAgents() error = %v", err)
	}
	if len(agents) != 0 {
		t.Errorf("ListAgents() count = %d, want 0 after revocation", len(agents))
	}
	if err := client.SignAs(ctx, "test-agent", newTestEvent()); !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("SignAs() after revoke error = %v, want ErrAgentNotFound", err)
	}
}

func TestClient_SuspendResume_ExplicitMockMode(t *testing.T) {
	client := newConnectedMockClient(t)
	ctx := context.Background()

	pubkey, _, _, err := client.ProvisionAgent(ctx, "test-agent", nil)
	if err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	status, err := client.GetAgentStatus(ctx, pubkey)
	if err != nil {
		t.Fatalf("GetAgentStatus() error = %v", err)
	}
	if status != AgentStatusActive {
		t.Fatalf("initial status = %s, want %s", status, AgentStatusActive)
	}

	if err := client.SuspendAgent(ctx, pubkey); err != nil {
		t.Fatalf("SuspendAgent() error = %v", err)
	}
	status, err = client.GetAgentStatus(ctx, pubkey)
	if err != nil {
		t.Fatalf("GetAgentStatus() after suspend error = %v", err)
	}
	if status != AgentStatusSuspended {
		t.Fatalf("status after suspend = %s, want %s", status, AgentStatusSuspended)
	}
	if err := client.SignAs(ctx, "test-agent", newTestEvent()); !errors.Is(err, ErrAgentSuspended) {
		t.Fatalf("SignAs() while suspended error = %v, want ErrAgentSuspended", err)
	}

	if err := client.ResumeAgent(ctx, pubkey); err != nil {
		t.Fatalf("ResumeAgent() error = %v", err)
	}
	status, err = client.GetAgentStatus(ctx, pubkey)
	if err != nil {
		t.Fatalf("GetAgentStatus() after resume error = %v", err)
	}
	if status != AgentStatusActive {
		t.Fatalf("status after resume = %s, want %s", status, AgentStatusActive)
	}

	event := newTestEvent()
	if err := client.SignAs(ctx, "test-agent", event); err != nil {
		t.Fatalf("SignAs() after resume error = %v", err)
	}
	assertValidSignature(t, event)
}

func TestClient_GetPublicKey_ExplicitMockMode(t *testing.T) {
	client := newConnectedMockClient(t)
	pk, err := client.GetPublicKey(context.Background())
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}
	if len(pk) != 64 {
		t.Errorf("public key length = %d, want 64", len(pk))
	}
}

func TestClient_SignNIP98_ExplicitMockMode(t *testing.T) {
	client := newConnectedMockClient(t)

	header, err := client.SignNIP98(context.Background(), "https://example.com/upload", "PUT", "abc123")
	if err != nil {
		t.Fatalf("SignNIP98() error = %v", err)
	}

	if !strings.HasPrefix(header, "Nostr ") {
		t.Errorf("header = %q, want Nostr auth header", header)
	}
}

func TestClient_Close(t *testing.T) {
	client := newConnectedMockClient(t)

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if client.IsConnected() {
		t.Error("should not be connected after Close()")
	}
	if client.IsMockMode() {
		t.Error("should not be in mock mode after Close()")
	}
}

func TestParseBunkerURI(t *testing.T) {
	tests := []struct {
		name       string
		uri        string
		wantPubkey string
		wantRelays []string
		wantSecret string
		wantErr    bool
	}{
		{
			name:       "valid with relay and secret",
			uri:        "bunker://3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d?relay=wss://relay.example.com&secret=mysecret",
			wantPubkey: "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d",
			wantRelays: []string{"wss://relay.example.com"},
			wantSecret: "mysecret",
			wantErr:    false,
		},
		{
			name:       "valid with multiple relays",
			uri:        "bunker://3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d?relay=wss://r1.example.com&relay=wss://r2.example.com",
			wantPubkey: "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d",
			wantRelays: []string{"wss://r1.example.com", "wss://r2.example.com"},
			wantSecret: "",
			wantErr:    false,
		},
		{
			name:    "invalid - not bunker://",
			uri:     "https://example.com",
			wantErr: true,
		},
		{
			name:    "invalid - short pubkey",
			uri:     "bunker://abc123?relay=wss://relay.example.com",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pubkey, relays, secret, err := ParseBunkerURI(tt.uri)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseBunkerURI() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !tt.wantErr {
				if pubkey != tt.wantPubkey {
					t.Errorf("pubkey = %s, want %s", pubkey, tt.wantPubkey)
				}
				if len(relays) != len(tt.wantRelays) {
					t.Errorf("relays count = %d, want %d", len(relays), len(tt.wantRelays))
				}
				if secret != tt.wantSecret {
					t.Errorf("secret = %s, want %s", secret, tt.wantSecret)
				}
			}
		})
	}
}

func newConnectedMockClient(t *testing.T) *Client {
	t.Helper()

	client, err := NewClient(Config{AllowMock: true}, nil)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if err := client.Connect(context.Background()); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	return client
}

func newTestEvent() *nostr.Event {
	return &nostr.Event{
		Kind:      1,
		Content:   "test content",
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
	}
}

func assertValidSignature(t *testing.T, event *nostr.Event) {
	t.Helper()

	if event.PubKey == "" {
		t.Fatal("event.PubKey should be set after signing")
	}
	if event.Sig == "" {
		t.Fatal("event.Sig should be set after signing")
	}

	ok, err := event.CheckSignature()
	if err != nil {
		t.Fatalf("CheckSignature() error = %v", err)
	}
	if !ok {
		t.Fatal("signature should be valid")
	}
}
