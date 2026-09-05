package client

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"fiatjaf.com/nostr"
	"fiatjaf.com/nostr/keyer"
	cascontextvm "git.sharegap.net/cascadia/cascadia-go/contextvm"
	"github.com/openagentsinc/bahia/internal/controlplane"
)

func TestContextVMRequestClientPlainRoundTripSerializesMethodAndParams(t *testing.T) {
	senderSecret := nostr.Generate()
	recipientSecret := nostr.Generate()
	transport := newFakeOperatorTransport()
	zeroRetries := 0
	client, err := NewContextVMRequestClient(ContextVMRequestConfig{
		Transport:       transport,
		Signer:          keyer.NewPlainKeySigner(senderSecret),
		SenderPubkey:    senderSecret.Public().Hex(),
		RecipientPubkey: recipientSecret.Public().Hex(),
		ResultTimeout:   time.Second,
		ResultRetries:   &zeroRetries,
	})
	if err != nil {
		t.Fatalf("NewContextVMRequestClient() error = %v", err)
	}

	transport.publishFn = func(_ context.Context, event nostr.Event) (int, error) {
		assertSignedEvent(t, event)
		if event.Kind != nostr.Kind(controlplane.KindContextVMMessage) {
			t.Fatalf("request kind = %d, want %d", event.Kind, controlplane.KindContextVMMessage)
		}
		rpc := decodePublishedContextVMRequest(t, event)
		if rpc.JSONRPC != "2.0" || rpc.Method != "dns/resolve" {
			t.Fatalf("RPC envelope = %#v", rpc)
		}
		if rpc.Params["hostname"] != "api.example.com" || rpc.Params["ttl"] != float64(60) {
			t.Fatalf("RPC params = %#v", rpc.Params)
		}
		meta, ok := rpc.Params["_meta"].(map[string]any)
		if !ok || meta["progressToken"] != rpc.ID {
			t.Fatalf("RPC _meta = %#v, request ID = %q", rpc.Params["_meta"], rpc.ID)
		}
		assertTagValue(t, event.Tags, "d", rpc.ID)
		assertTagValue(t, event.Tags, "method", "dns/resolve")
		assertTagValue(t, event.Tags, "p", recipientSecret.Public().Hex())
		transport.events <- signedContextVMResult(t, recipientSecret.Hex(), event, map[string]any{"address": "192.0.2.1"})
		return 1, nil
	}

	result, err := client.Request(context.Background(), "dns/resolve", map[string]any{"hostname": "api.example.com", "ttl": 60}, nostr.Tags{{"scope", "prod"}}, nil)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if payload["address"] != "192.0.2.1" {
		t.Fatalf("result = %#v", payload)
	}
}

func TestContextVMRequestClientEncryptedLocalKeyRoundTrip(t *testing.T) {
	senderSecret := nostr.Generate()
	recipientSecret := nostr.Generate()
	senderKeyer := keyer.NewPlainKeySigner(senderSecret)
	recipientKeyer := keyer.NewPlainKeySigner(recipientSecret)
	transport := newFakeOperatorTransport()
	zeroRetries := 0
	client, err := NewContextVMRequestClient(ContextVMRequestConfig{
		Transport:       transport,
		Signer:          senderKeyer,
		SenderPubkey:    senderSecret.Public().Hex(),
		RecipientPubkey: recipientSecret.Public().Hex(),
		Encrypted:       true,
		ResultTimeout:   time.Second,
		ResultRetries:   &zeroRetries,
	})
	if err != nil {
		t.Fatalf("NewContextVMRequestClient() error = %v", err)
	}

	transport.publishFn = func(ctx context.Context, outer nostr.Event) (int, error) {
		if outer.Kind != nostr.Kind(controlplane.KindContextVMGiftWrap) {
			t.Fatalf("outer kind = %d, want %d", outer.Kind, controlplane.KindContextVMGiftWrap)
		}
		inner, err := cascontextvm.UnwrapNIP59(ctx, recipientKeyer, &outer)
		if err != nil {
			t.Fatalf("unwrap encrypted request: %v", err)
		}
		rpc := decodePublishedContextVMRequest(t, *inner)
		if rpc.Method != "dns/reload" || rpc.Params["zone"] != "example.com" {
			t.Fatalf("RPC envelope = %#v", rpc)
		}
		transport.events <- wrappedContextVMResult(t, senderSecret.Public(), outer, *inner, recipientSecret, false, map[string]any{"reloaded": true})
		return 1, nil
	}

	result, err := client.Request(context.Background(), "dns/reload", map[string]any{"zone": "example.com"}, nil, nil)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	if result.PubKey.Hex() != recipientSecret.Public().Hex() || result.Content != `{"reloaded":true}` {
		t.Fatalf("result author/content = %s %s", result.PubKey.Hex(), result.Content)
	}
	filter := transport.onlyFilter(t)
	if len(filter.Kinds) != 2 || len(filter.Authors) != 0 {
		t.Fatalf("encrypted filter = %#v", filter)
	}
}

func TestContextVMRequestClientCloseDoesNotCloseInjectedTransport(t *testing.T) {
	senderSecret := nostr.Generate()
	recipientSecret := nostr.Generate()
	transport := newFakeOperatorTransport()
	client, err := NewContextVMRequestClient(ContextVMRequestConfig{
		Transport:       transport,
		Signer:          keyer.NewPlainKeySigner(senderSecret),
		SenderPubkey:    senderSecret.Public().Hex(),
		RecipientPubkey: recipientSecret.Public().Hex(),
	})
	if err != nil {
		t.Fatalf("NewContextVMRequestClient() error = %v", err)
	}

	client.Close()
	transport.mu.Lock()
	closed := transport.closed
	transport.mu.Unlock()
	if closed {
		t.Fatal("Close() closed injected transport")
	}
}
