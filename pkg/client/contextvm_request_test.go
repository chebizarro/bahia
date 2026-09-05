package client

import (
	"context"
	"encoding/json"
	"strings"
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

func TestContextVMParamsPreservesNestedJSONNumbers(t *testing.T) {
	const serial int64 = 1757100000123456789
	params, err := contextVMParams(map[string]any{
		"serial": serial,
		"zone": map[string]any{
			"ttl":          int64(300),
			"serial_floor": serial - 1,
		},
		"records": []any{map[string]any{"ttl": int64(600), "serial": serial}},
	}, "precision-test")
	if err != nil {
		t.Fatalf("contextVMParams() error = %v", err)
	}
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal ContextVM params: %v", err)
	}
	if !strings.Contains(string(encoded), `"serial":1757100000123456789`) ||
		!strings.Contains(string(encoded), `"serial_floor":1757100000123456788`) {
		t.Fatalf("ContextVM params rounded large integers: %s", encoded)
	}
	if got, ok := params["serial"].(json.Number); !ok || got.String() != "1757100000123456789" {
		t.Fatalf("serial = %#v (%T), want exact json.Number", params["serial"], params["serial"])
	}
	zone, ok := params["zone"].(map[string]any)
	if !ok || zone["ttl"] != json.Number("300") || zone["serial_floor"] != json.Number("1757100000123456788") {
		t.Fatalf("nested zone numbers = %#v", params["zone"])
	}
	records, ok := params["records"].([]any)
	if !ok || len(records) != 1 {
		t.Fatalf("nested records = %#v", params["records"])
	}
	record, ok := records[0].(map[string]any)
	if !ok || record["ttl"] != json.Number("600") || record["serial"] != json.Number("1757100000123456789") {
		t.Fatalf("nested record numbers = %#v", records[0])
	}

	fallback, err := contextVMParams([]int64{serial}, "fallback-test")
	if err != nil {
		t.Fatalf("contextVMParams() non-object error = %v", err)
	}
	fallbackJSON, err := json.Marshal(fallback)
	if err != nil {
		t.Fatalf("marshal non-object fallback: %v", err)
	}
	if !strings.Contains(string(fallbackJSON), `"value":[1757100000123456789]`) ||
		!strings.Contains(string(fallbackJSON), `"_meta":{"progressToken":"fallback-test"}`) {
		t.Fatalf("non-object fallback or progress metadata changed or rounded: %s", fallbackJSON)
	}
}

func TestContextVMRequestClientPreservesLargeIntegersEndToEnd(t *testing.T) {
	const serial int64 = 1757100000123456789
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
		var rpc struct {
			ID     string          `json:"id"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal([]byte(event.Content), &rpc); err != nil {
			t.Fatalf("decode published ContextVM request: %v", err)
		}
		if !strings.Contains(string(rpc.Params), `"serial":1757100000123456789`) ||
			!strings.Contains(string(rpc.Params), `"values":[1757100000123456789]`) {
			t.Fatalf("published params rounded large integers: %s", rpc.Params)
		}
		transport.events <- signedContextVMResult(t, recipientSecret.Hex(), event, map[string]any{
			"serial": serial,
			"nested": map[string]any{"values": []int64{serial}},
		})
		return 1, nil
	}

	result, err := client.Request(context.Background(), "dns/sync", map[string]any{
		"serial": serial,
		"nested": map[string]any{"values": []int64{serial}},
	}, nil, nil)
	if err != nil {
		t.Fatalf("Request() error = %v", err)
	}
	if !strings.Contains(result.Content, `"serial":1757100000123456789`) ||
		!strings.Contains(result.Content, `"values":[1757100000123456789]`) {
		t.Fatalf("result rounded large integers: %s", result.Content)
	}
	var decoded struct {
		Serial int64 `json:"serial"`
		Nested struct {
			Values []int64 `json:"values"`
		} `json:"nested"`
	}
	if err := json.Unmarshal([]byte(result.Content), &decoded); err != nil {
		t.Fatalf("decode exact result: %v", err)
	}
	if decoded.Serial != serial || len(decoded.Nested.Values) != 1 || decoded.Nested.Values[0] != serial {
		t.Fatalf("decoded result = %#v, want exact serial %d", decoded, serial)
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
