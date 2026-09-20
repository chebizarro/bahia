package controlplane

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"fiatjaf.com/nostr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type contentKeySet map[string]struct{}

func set(s ...string) contentKeySet {
	m := make(contentKeySet, len(s))
	for _, v := range s {
		m[v] = struct{}{}
	}
	return m
}

func hasKeys(actual contentKeySet, required contentKeySet) bool {
	for k := range required {
		if _, ok := actual[k]; !ok {
			return false
		}
	}
	return true
}

func keysOf(raw json.RawMessage) (contentKeySet, error) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	ks := make(contentKeySet, len(m))
	for k := range m {
		if k == "jsonrpc" || k == "id" || k == "error" || k == "result" {
			continue
		}
		ks[k] = struct{}{}
	}
	return ks, nil
}

func newTestReactor(t *testing.T, publisher NostrEventPublisher) (*Reactor, string) {
	t.Helper()
	privateKey, pubkey := testNostrKeypair()
	signer, err := NewPrivateKeySigner(privateKey)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{pubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(publisher))
	return reactor, pubkey
}

// TestDomainEnvelopeShape verifies that all four control-plane domains
// (dns, ml, worker, package) produce the same ContextVM envelope shape and
// correlation tags for an equivalent terminal result. This guards against
// each domain evolving its own reply skeleton independently.
func TestDomainEnvelopeShape(t *testing.T) {
	capture := &captureNostrPublisher{published: 1}
	reactor, pubkey := newTestReactor(t, capture)
	pub := testNostrPubKeyFromHex(t, pubkey)

	requestEvent := &nostr.Event{
		ID:        testNostrID("envelope-shape"),
		PubKey:    pub,
		Kind:      nostr.Kind(9999),
		CreatedAt: nostr.Now(),
	}

	type domainCase struct {
		name   string
		schema string
		fn     func()
	}
	succeededCases := []domainCase{
		{"dns", "bahia.result.dns.v1", func() {
			reactor.publishDomainResult(context.Background(), requestEvent, "dns", "bahia.result.dns.v1", "succeeded", "completed", "done", map[string]any{"action": "test"}, nostr.Tags{{"status", "succeeded"}})
		}},
		{"ml", "bahia.result.ml.v1", func() {
			reactor.publishDomainResult(context.Background(), requestEvent, "ml", "bahia.result.ml.v1", "succeeded", "completed", "done", map[string]any{"status": "succeeded"}, nostr.Tags{{"status", "succeeded"}})
		}},
		{"worker", "bahia.result.worker.v1", func() {
			reactor.publishDomainResult(context.Background(), requestEvent, "worker", "bahia.result.worker.v1", "succeeded", "completed", "done", map[string]any{"status": "succeeded"}, nostr.Tags{{"status", "succeeded"}})
		}},
		{"package", "bahia.result.package.v1", func() {
			reactor.publishDomainResult(context.Background(), requestEvent, "package", "bahia.result.package.v1", "succeeded", "completed", "done", map[string]any{"status": "succeeded"}, nostr.Tags{{"status", "succeeded"}})
		}},
	}

	errorCases := []domainCase{
		{"dns", "bahia.result.dns.v1", func() {
			reactor.publishDomainResult(context.Background(), requestEvent, "dns", "bahia.result.dns.v1", "failed", "parse_error", "invalid input", map[string]any{"action": "test"}, nostr.Tags{{"status", "failed"}})
		}},
		{"ml", "bahia.result.ml.v1", func() {
			reactor.publishDomainResult(context.Background(), requestEvent, "ml", "bahia.result.ml.v1", "failed", "parse_error", "invalid input", map[string]any{"status": "failed"}, nostr.Tags{{"status", "failed"}})
		}},
		{"worker", "bahia.result.worker.v1", func() {
			reactor.publishDomainResult(context.Background(), requestEvent, "worker", "bahia.result.worker.v1", "failed", "parse_error", "invalid input", map[string]any{"status": "failed"}, nostr.Tags{{"status", "failed"}})
		}},
		{"package", "bahia.result.package.v1", func() {
			reactor.publishDomainResult(context.Background(), requestEvent, "package", "bahia.result.package.v1", "failed", "parse_error", "invalid input", map[string]any{"status": "failed"}, nostr.Tags{{"status", "failed"}})
		}},
	}

	// Test succeeded envelope shape.
	for _, tc := range succeededCases {
		t.Run(tc.name+"-succeeded", func(t *testing.T) {
			capture.events = nil
			tc.fn()
			events := capture.events
			if len(events) == 0 {
				t.Fatalf("%s: expected a published ContextVM result event", tc.name)
			}
			event := events[len(events)-1]

			if got := tagValueNostr(event.Tags, "domain"); got != tc.name {
				t.Errorf("%s: domain tag = %q, want %s", tc.name, got, tc.name)
			}
			if got := tagValueNostr(event.Tags, "schema"); got != tc.schema {
				t.Errorf("%s: schema tag = %q, want %s", tc.name, got, tc.schema)
			}

			var rpc struct {
				JSONRPC string          `json:"jsonrpc"`
				Result  json.RawMessage `json:"result,omitempty"`
				Error   json.RawMessage `json:"error,omitempty"`
			}
			if err := json.Unmarshal([]byte(event.Content), &rpc); err != nil {
				t.Fatalf("%s: cannot unmarshal ContextVM envelope: %v", tc.name, err)
			}
			if rpc.JSONRPC != "2.0" {
				t.Errorf("%s: jsonrpc = %q, want \"2.0\"", tc.name, rpc.JSONRPC)
			}
			if rpc.Result == nil {
				t.Errorf("%s: result is nil in succeeded response", tc.name)
			}
			if rpc.Error != nil {
				t.Errorf("%s: error should be absent in succeeded response", tc.name)
			}

			if got := tagValueNostr(event.Tags, "e"); got != requestEvent.ID.Hex() {
				t.Errorf("%s: e tag = %q, want request event ID", tc.name, got)
			}
			if got := tagValueNostr(event.Tags, "p"); got != requestEvent.PubKey.Hex() {
				t.Errorf("%s: p tag = %q, want request pubkey", tc.name, got)
			}
			if got := tagValueNostr(event.Tags, ContextVMRoutingTag); got != ContextVMWireVersion {
				t.Errorf("%s: %s tag = %q, want %s", tc.name, ContextVMRoutingTag, got, ContextVMWireVersion)
			}
		})
	}

	// Test failed envelope shape.
	for _, tc := range errorCases {
		t.Run(tc.name, func(t *testing.T) {
			capture.events = nil
			tc.fn()
			events := capture.events
			if len(events) == 0 {
				t.Fatalf("%s: expected a published ContextVM result event", tc.name)
			}
			event := events[len(events)-1]

			var rpc struct {
				JSONRPC string          `json:"jsonrpc"`
				Result  json.RawMessage `json:"result,omitempty"`
				Error   *struct {
					Code    int    `json:"code"`
					Message string `json:"message"`
				} `json:"error,omitempty"`
			}
			if err := json.Unmarshal([]byte(event.Content), &rpc); err != nil {
				t.Fatalf("%s: cannot unmarshal ContextVM envelope: %v", tc.name, err)
			}
			if rpc.JSONRPC != "2.0" {
				t.Errorf("%s: jsonrpc = %q, want \"2.0\"", tc.name, rpc.JSONRPC)
			}
			if rpc.Result != nil {
				t.Errorf("%s: result should be nil in failed response, got %s", tc.name, string(rpc.Result))
			}
			if rpc.Error == nil {
				t.Errorf("%s: error should be present in failed response", tc.name)
			} else {
				if rpc.Error.Code != -32000 {
					t.Errorf("%s: error code = %d, want -32000", tc.name, rpc.Error.Code)
				}
				if rpc.Error.Message == "" {
					t.Errorf("%s: error message should not be empty", tc.name)
				}
			}

			if got := tagValueNostr(event.Tags, "e"); got != requestEvent.ID.Hex() {
				t.Errorf("%s: e tag = %q, want request event ID", tc.name, got)
			}
		})
	}
}

// TestPublishFailureIsLogged verifies that when the underlying publisher fails,
// the domain result publisher logs a warning instead of silently discarding
// the error. This proves the chosen contract (a): helpers log internally via
// the reactor's zap logger.
func TestPublishFailureIsLogged(t *testing.T) {
	obsCore, obsLogs := observer.New(zapcore.WarnLevel)
	zapLog := zap.New(obsCore)

	failingPublisher := &failingNostrPublisher{}
	privateKey, pubkey := testNostrKeypair()
	signer, err := NewPrivateKeySigner(privateKey)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{pubkey}}, nil, nil, signer, zapLog, WithControlPlanePublisher(failingPublisher))

	requestEvent := &nostr.Event{
		ID:        testNostrID("publish-fail-test"),
		PubKey:    testNostrPubKeyFromHex(t, pubkey),
		Kind:      nostr.Kind(9999),
		CreatedAt: nostr.Now(),
	}

	reactor.publishDomainResult(context.Background(), requestEvent, "test", "bahia.result.test.v1", "failed", "test_error", "test message", map[string]any{"status": "failed"}, nostr.Tags{{"status", "failed"}})

	warnEntries := obsLogs.FilterField(zap.String("domain", "test"))
	if warnEntries.Len() == 0 {
		t.Fatal("expected a warn log when publishing to a failing publisher, but none was logged")
	}

	entry := warnEntries.All()[0]
	if entry.Level != zapcore.WarnLevel {
		t.Errorf("log level = %s, want WarnLevel", entry.Level)
	}
	if got, ok := entry.ContextMap()["error"]; !ok {
		t.Error("expected error field in warn log entry")
	} else if got == "" {
		t.Error("error field should not be empty")
	}
}

// TestDomainPublishResultNoLongerReturnsError verifies that the domain publish
// functions no longer return error values — the chosen contract is (a): helpers
// log internally and return nothing.

// failingNostrPublisher always returns an error from Publish.
type failingNostrPublisher struct{}

func (f *failingNostrPublisher) Publish(_ context.Context, _ nostr.Event) (int, error) {
	return 0, &http.ProtocolError{ErrorString: "simulated publish failure"}
}

var _ NostrEventPublisher = (*failingNostrPublisher)(nil)
