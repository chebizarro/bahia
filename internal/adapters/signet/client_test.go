package signet

import (
	"context"
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

func TestClient_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)

	ctx := context.Background()
	if err := client.Connect(ctx); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}

	if !client.IsMockMode() {
		t.Error("should be in mock mode without bunker URI")
	}

	if !client.IsConnected() {
		t.Error("should be connected after Connect()")
	}
}

func TestClient_ProvisionAgent_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	pubkey, npub, bunkerURI, err := client.ProvisionAgent(ctx, "test-agent", []int{1, 30023})
	if err != nil {
		t.Fatalf("ProvisionAgent() error = %v", err)
	}

	if pubkey == "" {
		t.Error("pubkey should not be empty")
	}
	if len(pubkey) != 64 {
		t.Errorf("pubkey length = %d, want 64", len(pubkey))
	}
	if npub == "" {
		t.Error("npub should not be empty")
	}
	if bunkerURI == "" {
		t.Error("bunkerURI should not be empty")
	}

	// Verify agent is stored
	agents, _ := client.ListAgents(ctx)
	if len(agents) != 1 {
		t.Errorf("ListAgents() count = %d, want 1", len(agents))
	}
	if agents[0].AgentID != "test-agent" {
		t.Errorf("agent ID = %s, want test-agent", agents[0].AgentID)
	}
}

func TestClient_Sign_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	event := &nostr.Event{
		Kind:      1,
		Content:   "test content",
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
	}

	if err := client.Sign(ctx, event); err != nil {
		t.Fatalf("Sign() error = %v", err)
	}

	if event.PubKey == "" {
		t.Error("event.PubKey should be set after signing")
	}
	if event.Sig == "" {
		t.Error("event.Sig should be set after signing")
	}

	// Verify signature
	ok, err := event.CheckSignature()
	if err != nil {
		t.Fatalf("CheckSignature() error = %v", err)
	}
	if !ok {
		t.Error("signature should be valid")
	}
}

func TestClient_SignAs_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	// Provision an agent first
	pubkey, _, _, _ := client.ProvisionAgent(ctx, "test-agent", nil)

	event := &nostr.Event{
		Kind:      1,
		Content:   "test content",
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{},
	}

	if err := client.SignAs(ctx, "test-agent", event); err != nil {
		t.Fatalf("SignAs() error = %v", err)
	}

	// In mock mode, pubkey is set but signature is not (can't sign without key)
	if event.PubKey != pubkey {
		t.Errorf("event.PubKey = %s, want %s", event.PubKey, pubkey)
	}
}

func TestClient_SignAs_NotFound(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	event := &nostr.Event{Kind: 1}
	err := client.SignAs(ctx, "nonexistent", event)
	if err == nil {
		t.Error("SignAs() should fail for nonexistent agent")
	}
}

func TestClient_RevokeAgent_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	pubkey, _, _, _ := client.ProvisionAgent(ctx, "test-agent", nil)

	if err := client.RevokeAgent(ctx, pubkey); err != nil {
		t.Fatalf("RevokeAgent() error = %v", err)
	}

	// Verify agent is removed
	agents, _ := client.ListAgents(ctx)
	if len(agents) != 0 {
		t.Errorf("ListAgents() count = %d, want 0 after revocation", len(agents))
	}
}

func TestClient_GetAgentStatus_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	status, err := client.GetAgentStatus(ctx, "anypubkey")
	if err != nil {
		t.Fatalf("GetAgentStatus() error = %v", err)
	}
	if status != "active" {
		t.Errorf("status = %s, want active", status)
	}
}

func TestClient_GetPublicKey_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	pk, err := client.GetPublicKey(ctx)
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}
	if pk == "" {
		t.Error("public key should not be empty")
	}
	if len(pk) != 64 {
		t.Errorf("public key length = %d, want 64", len(pk))
	}
}

func TestClient_SignNIP98_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	header, err := client.SignNIP98(ctx, "https://example.com/upload", "PUT", "abc123")
	if err != nil {
		t.Fatalf("SignNIP98() error = %v", err)
	}

	if header == "" {
		t.Error("header should not be empty")
	}
	if len(header) < 10 {
		t.Error("header seems too short")
	}
}

func TestClient_Close(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	if err := client.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if client.IsConnected() {
		t.Error("should not be connected after Close()")
	}
}

func TestClient_SuspendAgent_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	pubkey, _, _, _ := client.ProvisionAgent(ctx, "test-agent", nil)

	if err := client.SuspendAgent(ctx, pubkey); err != nil {
		t.Fatalf("SuspendAgent() error = %v", err)
	}
}

func TestClient_ResumeAgent_MockMode(t *testing.T) {
	client, _ := NewClient(Config{}, nil)
	ctx := context.Background()
	client.Connect(ctx)

	pubkey, _, _, _ := client.ProvisionAgent(ctx, "test-agent", nil)

	// Suspend first
	if err := client.SuspendAgent(ctx, pubkey); err != nil {
		t.Fatalf("SuspendAgent() error = %v", err)
	}

	// Then resume
	if err := client.ResumeAgent(ctx, pubkey); err != nil {
		t.Fatalf("ResumeAgent() error = %v", err)
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
