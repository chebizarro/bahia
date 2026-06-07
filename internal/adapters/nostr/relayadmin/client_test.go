package relayadmin

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"
)

func TestDisabledClientFailsClosed(t *testing.T) {
	client, err := NewClient(Config{})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	if _, err := client.Call(context.Background(), "relay", MethodSupportedMethods, nil); !errors.Is(err, ErrDisabled) {
		t.Fatalf("Call() error = %v, want ErrDisabled", err)
	}
}

func TestClientRequiresAuthorizedConfiguredTarget(t *testing.T) {
	privateKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}

	_, err = NewClient(Config{
		Enabled:       true,
		PrivateKeyHex: privateKey,
		Targets: []Target{{
			Ref:                  "owned-sidecar",
			RelayURL:             "wss://relay.example.com",
			AdministratorPubkeys: []string{strings.Repeat("a", 64)},
		}},
	})
	if !errors.Is(err, ErrUnauthorizedTarget) {
		t.Fatalf("NewClient() error = %v, want ErrUnauthorizedTarget", err)
	}

	client, err := NewClient(Config{
		Enabled:       true,
		PrivateKeyHex: privateKey,
		Targets: []Target{{
			Ref:                  "owned-sidecar",
			RelayURL:             "wss://relay.example.com",
			AdministratorPubkeys: []string{pubkey},
		}},
	})
	if err != nil {
		t.Fatalf("NewClient() with authorized pubkey error = %v", err)
	}
	if _, err := client.Call(context.Background(), "missing", MethodSupportedMethods, nil); !errors.Is(err, ErrUnauthorizedTarget) {
		t.Fatalf("Call() error = %v, want ErrUnauthorizedTarget", err)
	}
}

func TestCallSignsPayloadBoundNIP98Authorization(t *testing.T) {
	privateKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}

	var receivedBody []byte
	var receivedAuth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if got := r.Header.Get("Content-Type"); got != ContentType {
			t.Fatalf("Content-Type = %q, want %q", got, ContentType)
		}
		receivedAuth = r.Header.Get("Authorization")
		var readErr error
		receivedBody, readErr = io.ReadAll(r.Body)
		if readErr != nil {
			t.Fatalf("reading request body: %v", readErr)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"result":["supportedmethods"],"error":""}`))
	}))
	defer server.Close()

	client, err := NewClient(Config{
		Enabled:       true,
		PrivateKeyHex: privateKey,
		Now:           func() time.Time { return time.Unix(1_700_000_000, 0) },
		Targets: []Target{{
			Ref:                  "owned-sidecar",
			RelayURL:             "wss://relay.example.com/nostr/",
			HTTPURL:              server.URL,
			AdministratorPubkeys: []string{pubkey},
		}},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	methods, err := client.SupportedMethods(context.Background(), "owned-sidecar")
	if err != nil {
		t.Fatalf("SupportedMethods() error = %v", err)
	}
	if len(methods) != 1 || methods[0] != MethodSupportedMethods {
		t.Fatalf("methods = %#v", methods)
	}

	if string(receivedBody) != `{"method":"supportedmethods","params":[]}` {
		t.Fatalf("body = %s", string(receivedBody))
	}
	header := receivedAuth
	if !strings.HasPrefix(header, "Nostr ") {
		t.Fatalf("Authorization header = %q", header)
	}
	event := decodeAuthEvent(t, header)
	if event.Kind != 27235 {
		t.Fatalf("event.Kind = %d, want 27235", event.Kind)
	}
	if event.PubKey != pubkey {
		t.Fatalf("event.PubKey = %s, want %s", event.PubKey, pubkey)
	}
	if event.CreatedAt != nostr.Timestamp(1_700_000_000) {
		t.Fatalf("event.CreatedAt = %v", event.CreatedAt)
	}
	assertTag(t, event, "u", "wss://relay.example.com/nostr/")
	assertTag(t, event, "method", http.MethodPost)
	hash := sha256.Sum256(receivedBody)
	assertTag(t, event, "payload", hex.EncodeToString(hash[:]))
	ok, err := event.CheckSignature()
	if err != nil {
		t.Fatalf("CheckSignature() error = %v", err)
	}
	if !ok {
		t.Fatal("event signature is invalid")
	}
}

func TestClientRejectsExternalPlaintextAdministrationEndpoints(t *testing.T) {
	privateKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}
	tests := []struct {
		name   string
		target Target
		want   string
	}{
		{
			name:   "external ws relay",
			target: Target{Ref: "owned", RelayURL: "ws://relay.example.com", HTTPURL: "https://relay.example.com", AdministratorPubkeys: []string{pubkey}},
			want:   "use wss",
		},
		{
			name:   "external http endpoint",
			target: Target{Ref: "owned", RelayURL: "wss://relay.example.com", HTTPURL: "http://relay.example.com", AdministratorPubkeys: []string{pubkey}},
			want:   "use https",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewClient(Config{Enabled: true, PrivateKeyHex: privateKey, Targets: []Target{tt.target}})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("NewClient() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestCallRejectsContextVMMutationMethodsBeforeHTTP(t *testing.T) {
	privateKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	client, err := NewClient(Config{
		Enabled:       true,
		PrivateKeyHex: privateKey,
		Targets:       []Target{{Ref: "owned", RelayURL: "wss://relay.example.com", HTTPURL: server.URL, AdministratorPubkeys: []string{pubkey}}},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.Call(context.Background(), "owned", "service/deploy", []any{map[string]any{"service_id": "svc"}})
	if !errors.Is(err, ErrUnsupportedMethod) {
		t.Fatalf("Call() error = %v, want ErrUnsupportedMethod", err)
	}
	if called {
		t.Fatal("unsupported ContextVM method reached HTTP server")
	}
}

func TestCallReportsHTTPAndRelayErrors(t *testing.T) {
	privateKey := nostr.GeneratePrivateKey()
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatalf("GetPublicKey() error = %v", err)
	}
	statusServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer statusServer.Close()
	client, err := NewClient(Config{
		Enabled:       true,
		PrivateKeyHex: privateKey,
		Targets:       []Target{{Ref: "owned", RelayURL: "wss://relay.example.com", HTTPURL: statusServer.URL, AdministratorPubkeys: []string{pubkey}}},
	})
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	_, err = client.Call(context.Background(), "owned", MethodSupportedMethods, nil)
	var statusErr *HTTPStatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != http.StatusUnauthorized {
		t.Fatalf("Call() error = %#v, want HTTPStatusError 401", err)
	}

	relayErrorServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"result":null,"error":"method not allowed for administrator"}`))
	}))
	defer relayErrorServer.Close()
	client.targets["owned"] = Target{Ref: "owned", RelayURL: "wss://relay.example.com", HTTPURL: relayErrorServer.URL, AdministratorPubkeys: []string{pubkey}}
	_, err = client.Call(context.Background(), "owned", MethodSupportedMethods, nil)
	if !errors.Is(err, ErrRelayError) {
		t.Fatalf("Call() error = %v, want ErrRelayError", err)
	}
}

func decodeAuthEvent(t *testing.T, header string) nostr.Event {
	t.Helper()
	encoded := strings.TrimPrefix(header, "Nostr ")
	data, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatalf("DecodeString() error = %v", err)
	}
	var event nostr.Event
	if err := json.Unmarshal(data, &event); err != nil {
		t.Fatalf("Unmarshal event error = %v", err)
	}
	return event
}

func assertTag(t *testing.T, event nostr.Event, name, want string) {
	t.Helper()
	for _, tag := range event.Tags {
		if len(tag) >= 2 && tag[0] == name {
			if tag[1] != want {
				t.Fatalf("tag %s = %q, want %q", name, tag[1], want)
			}
			return
		}
	}
	t.Fatalf("missing tag %s", name)
}
