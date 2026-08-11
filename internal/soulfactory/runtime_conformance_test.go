package soulfactory

import (
	"strings"
	"testing"
	"time"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// runtimeConformanceCases enumerates the runtime targets covered by the shared
// conformance tables: the two packaged targets plus a synthetic third target
// that exercises the generic adapter without a dedicated type.
var runtimeConformanceCases = []struct {
	name   string
	target domain.RuntimeTarget
}{
	{name: "openclaw", target: domain.RuntimeTargetOpenClaw},
	{name: "metiq", target: domain.RuntimeTargetMetiq},
	{name: "synthetic third runtime", target: domain.RuntimeTarget("synthetic-3")},
}

func conformanceCapability(t *testing.T, controller, runtime fakeSigner, target domain.RuntimeTarget, methods []string) *nostr.Event {
	t.Helper()
	return signedRuntimeCapabilityEvent(t, runtime, map[string]interface{}{
		"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
		"runtime":            string(target),
		"methods":            methods,
		"control_schema":     domain.SoulFactoryRuntimeControlSchema,
		"controller_pubkeys": []string{controller.pubkey},
	}, nostr.Tags{{tagParameterizedD, string(target) + "-main"}, {tagRuntime, string(target)}})
}

func newConformanceAdapter(t *testing.T, controller fakeSigner, target domain.RuntimeTarget, transport *fakeRuntimeAdapterTransport) RuntimeAdapter {
	t.Helper()
	adapter, err := NewRuntimeAdapter(RuntimeAdapterConfig{
		Target:           target,
		ControllerPubkey: controller.pubkey,
		Signer:           controller,
		Relays:           []string{"wss://fallback.example"},
		Transport:        transport,
		Now:              func() time.Time { return time.Unix(1715700000, 0) },
	})
	if err != nil {
		t.Fatalf("NewRuntimeAdapter(%s) error = %v", target, err)
	}
	return adapter
}

// TestRuntimeAdapterGenericConstructorCoversAnyTarget proves a protocol-
// conforming runtime registers through the generic adapter without a new type.
func TestRuntimeAdapterGenericConstructorCoversAnyTarget(t *testing.T) {
	controller := newFakeSigner(t)
	for _, tc := range runtimeConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			adapter := newConformanceAdapter(t, controller, tc.target, &fakeRuntimeAdapterTransport{})
			if adapter.Runtime() != tc.target {
				t.Fatalf("Runtime() = %q, want %q", adapter.Runtime(), tc.target)
			}
		})
	}
	if _, err := NewRuntimeAdapter(RuntimeAdapterConfig{ControllerPubkey: controller.pubkey, Signer: controller}); err == nil {
		t.Fatal("NewRuntimeAdapter without target error = nil, want error")
	}
}

// TestRuntimeAdapterRoutesToCorrectTargetAndPubkey proves the generic adapter
// binds target, runtime pubkey, capability ref, method, spec hash, and
// idempotency key identically across runtimes.
func TestRuntimeAdapterRoutesToCorrectTargetAndPubkey(t *testing.T) {
	for _, tc := range runtimeConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			controller := newFakeSigner(t)
			runtime := newFakeSigner(t)
			runtimeAdapterTestRuntimeSecret = runtime.secret
			runtimeAdapterTestRuntimePubkey = runtime.pubkey
			capability := conformanceCapability(t, controller, runtime, tc.target, []string{RuntimeMethodProvision})
			transport := &fakeRuntimeAdapterTransport{capabilities: []*nostr.Event{capability}}
			adapter := newConformanceAdapter(t, controller, tc.target, transport)

			result, err := adapter.Execute(t.Context(), RuntimeAdapterRequest{
				Method:   RuntimeMethodProvision,
				Operator: RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: stringsRepeat("b", 64)},
				Soul:     RuntimeSoulRef{ID: "scout", Draft: "draft-event", SpecHash: "sha256:spec"},
				Target:   RuntimeTargetRef{Runtime: tc.target, AgentID: "scout"},
				Params:   map[string]interface{}{"identity": map[string]interface{}{"name": "Scout"}},
			})
			if err != nil {
				t.Fatalf("Execute error = %v", err)
			}
			if result.Status != "success" || result.Result["runtime"] != string(tc.target) {
				t.Fatalf("unexpected result: %+v", result)
			}
			if len(transport.published) != 1 {
				t.Fatalf("published count = %d, want 1", len(transport.published))
			}
			request := transport.published[0]
			if got := tagValue(request.Tags, tagPubkey); got != runtime.pubkey {
				t.Fatalf("request p tag = %q, want runtime pubkey %s", got, runtime.pubkey)
			}
			if got := tagValue(request.Tags, tagCapability); got != capability.ID.Hex() {
				t.Fatalf("request capability tag = %q, want %s", got, capability.ID.Hex())
			}
			envelope, err := ParseRuntimeControlRequestEvent(&request)
			if err != nil {
				t.Fatalf("ParseRuntimeControlRequestEvent error = %v", err)
			}
			if envelope.Target.Runtime != tc.target || envelope.Target.RuntimePubkey != runtime.pubkey {
				t.Fatalf("envelope target = %+v, want %s/%s", envelope.Target, tc.target, runtime.pubkey)
			}
			if envelope.IdempotencyKey == "" || tagValue(request.Tags, "idempotency-key") != envelope.IdempotencyKey {
				t.Fatalf("idempotency key binding mismatch: %+v", envelope)
			}
			if envelope.Soul.SpecHash != "sha256:spec" || tagValue(request.Tags, tagSpecHash) != "sha256:spec" {
				t.Fatalf("spec hash binding mismatch: %+v", envelope)
			}
			if !hasFilter(transport.filters, domain.KindRuntimeCapability, nostr.TagMap{tagRuntime: []string{string(tc.target)}}) {
				t.Fatalf("missing %s-scoped capability filter: %#v", tc.target, transport.filters)
			}
		})
	}
}

// TestRuntimeAdapterRejectsUnavailableOrIncompatibleTargets proves dispatch
// fails closed before any 38384 is published.
func TestRuntimeAdapterRejectsUnavailableOrIncompatibleTargets(t *testing.T) {
	for _, tc := range runtimeConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			controller := newFakeSigner(t)
			runtime := newFakeSigner(t)
			runtimeAdapterTestRuntimeSecret = runtime.secret
			runtimeAdapterTestRuntimePubkey = runtime.pubkey

			t.Run("unsupported method", func(t *testing.T) {
				capability := conformanceCapability(t, controller, runtime, tc.target, []string{RuntimeMethodProvision})
				transport := &fakeRuntimeAdapterTransport{capabilities: []*nostr.Event{capability}}
				adapter := newConformanceAdapter(t, controller, tc.target, transport)
				_, err := adapter.Execute(t.Context(), RuntimeAdapterRequest{
					Method:   RuntimeMethodRevoke,
					Operator: RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: stringsRepeat("b", 64)},
					Soul:     RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
					Target:   RuntimeTargetRef{Runtime: tc.target, AgentID: "scout"},
					Params:   map[string]interface{}{"reason": "test"},
				})
				if err == nil || !strings.Contains(err.Error(), "no compatible") {
					t.Fatalf("Execute error = %v, want no compatible capability", err)
				}
				if len(transport.published) != 0 {
					t.Fatalf("published %d requests, want no side effects", len(transport.published))
				}
			})

			t.Run("unauthorized controller", func(t *testing.T) {
				otherController := newFakeSigner(t)
				capability := conformanceCapability(t, otherController, runtime, tc.target, []string{RuntimeMethodProvision})
				transport := &fakeRuntimeAdapterTransport{capabilities: []*nostr.Event{capability}}
				adapter := newConformanceAdapter(t, controller, tc.target, transport)
				_, err := adapter.Execute(t.Context(), RuntimeAdapterRequest{
					Method:   RuntimeMethodProvision,
					Operator: RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: stringsRepeat("b", 64)},
					Soul:     RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
					Target:   RuntimeTargetRef{Runtime: tc.target, AgentID: "scout"},
				})
				if err == nil || !strings.Contains(err.Error(), "no compatible") {
					t.Fatalf("Execute error = %v, want no compatible capability", err)
				}
				if len(transport.published) != 0 {
					t.Fatalf("published %d requests, want no side effects", len(transport.published))
				}
			})

			t.Run("missing capability", func(t *testing.T) {
				transport := &fakeRuntimeAdapterTransport{}
				adapter := newConformanceAdapter(t, controller, tc.target, transport)
				_, err := adapter.Execute(t.Context(), RuntimeAdapterRequest{
					Method:   RuntimeMethodProvision,
					Operator: RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: stringsRepeat("b", 64)},
					Soul:     RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
					Target:   RuntimeTargetRef{Runtime: tc.target, AgentID: "scout"},
				})
				if err == nil {
					t.Fatal("Execute error = nil, want missing capability failure")
				}
				if len(transport.published) != 0 {
					t.Fatalf("published %d requests, want no side effects", len(transport.published))
				}
			})

			t.Run("duplicate conflict surfaces without retry", func(t *testing.T) {
				capability := conformanceCapability(t, controller, runtime, tc.target, []string{RuntimeMethodProvision})
				transport := &fakeRuntimeAdapterTransport{
					capabilities: []*nostr.Event{capability},
					resultStatus: "rejected",
					resultError:  &RuntimeControlError{Code: "duplicate_conflict", Message: "idempotency key reused with conflicting input", Retryable: false},
				}
				adapter := newConformanceAdapter(t, controller, tc.target, transport)
				result, err := adapter.Execute(t.Context(), RuntimeAdapterRequest{
					Method:   RuntimeMethodProvision,
					Operator: RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: stringsRepeat("b", 64)},
					Soul:     RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
					Target:   RuntimeTargetRef{Runtime: tc.target, AgentID: "scout"},
				})
				if err == nil || !strings.Contains(err.Error(), "duplicate_conflict") {
					t.Fatalf("Execute error = %v, want duplicate_conflict", err)
				}
				if result == nil || result.Status != "rejected" || result.Error.Code != "duplicate_conflict" {
					t.Fatalf("result = %+v, want rejected duplicate_conflict", result)
				}
				if len(transport.published) != 1 {
					t.Fatalf("published count = %d, want exactly one request", len(transport.published))
				}
			})
		})
	}
}

// TestRuntimeAdapterRejectsStaleCapabilities proves stale 30317 announcements
// never gate dispatch, whether discovered or supplied by the caller.
func TestRuntimeAdapterRejectsStaleCapabilities(t *testing.T) {
	for _, tc := range runtimeConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			controller := newFakeSigner(t)
			runtime := newFakeSigner(t)
			runtimeAdapterTestRuntimeSecret = runtime.secret
			runtimeAdapterTestRuntimePubkey = runtime.pubkey
			now := time.Unix(1715700000, 0)
			staleAt := now.Add(-DefaultMaxCapabilityAge - time.Minute)
			staleCapability := signedRuntimeCapabilityEventAt(t, runtime, staleAt.Unix(), map[string]interface{}{
				"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
				"runtime":            string(tc.target),
				"methods":            []string{RuntimeMethodProvision},
				"control_schema":     domain.SoulFactoryRuntimeControlSchema,
				"controller_pubkeys": []string{controller.pubkey},
			}, nostr.Tags{{tagParameterizedD, string(tc.target) + "-main"}, {tagRuntime, string(tc.target)}})

			t.Run("stale discovery result is ineligible", func(t *testing.T) {
				transport := &fakeRuntimeAdapterTransport{capabilities: []*nostr.Event{staleCapability}}
				adapter := newConformanceAdapter(t, controller, tc.target, transport)
				_, err := adapter.Execute(t.Context(), RuntimeAdapterRequest{
					Method:   RuntimeMethodProvision,
					Operator: RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: stringsRepeat("b", 64)},
					Soul:     RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
					Target:   RuntimeTargetRef{Runtime: tc.target, AgentID: "scout"},
				})
				if err == nil || !strings.Contains(err.Error(), "no compatible") {
					t.Fatalf("Execute error = %v, want no compatible capability", err)
				}
				if len(transport.published) != 0 {
					t.Fatalf("published %d requests against stale capability", len(transport.published))
				}
			})

			t.Run("stale caller-supplied capability is rejected", func(t *testing.T) {
				transport := &fakeRuntimeAdapterTransport{}
				adapter := newConformanceAdapter(t, controller, tc.target, transport)
				stale := RuntimeCapability{
					ID:                staleCapability.ID.Hex(),
					Pubkey:            runtime.pubkey,
					Runtime:           tc.target,
					Schema:            domain.SoulFactoryRuntimeCapabilitySchema,
					ControlSchema:     domain.SoulFactoryRuntimeControlSchema,
					Methods:           []string{RuntimeMethodProvision},
					ControllerPubkeys: []string{controller.pubkey},
					CreatedAt:         staleAt,
					Compatible:        true,
				}
				_, err := adapter.Execute(t.Context(), RuntimeAdapterRequest{
					Method:     RuntimeMethodProvision,
					Operator:   RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: stringsRepeat("b", 64)},
					Soul:       RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
					Target:     RuntimeTargetRef{Runtime: tc.target, AgentID: "scout", RuntimePubkey: runtime.pubkey},
					Capability: &stale,
				})
				if err == nil || !strings.Contains(err.Error(), "stale") {
					t.Fatalf("Execute error = %v, want stale capability rejection", err)
				}
				if len(transport.published) != 0 {
					t.Fatalf("published %d requests against stale capability", len(transport.published))
				}
			})
		})
	}
}

// TestRuntimeCapabilityLatestWinsAcrossRuntimes proves replaceable latest-wins
// dedupe behaves identically for every runtime target.
func TestRuntimeCapabilityLatestWinsAcrossRuntimes(t *testing.T) {
	for _, tc := range runtimeConformanceCases {
		t.Run(tc.name, func(t *testing.T) {
			controller := newFakeSigner(t)
			runtime := newFakeSigner(t)
			now := int64(nostr.Now())
			oldCapability := signedRuntimeCapabilityEventAt(t, runtime, now-10, map[string]interface{}{
				"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
				"runtime":            string(tc.target),
				"methods":            []string{RuntimeMethodProvision},
				"control_schema":     domain.SoulFactoryRuntimeControlSchema,
				"controller_pubkeys": []string{controller.pubkey},
			}, nostr.Tags{{tagParameterizedD, "main"}, {tagRuntime, string(tc.target)}})
			newCapability := signedRuntimeCapabilityEventAt(t, runtime, now, map[string]interface{}{
				"schema":             domain.SoulFactoryRuntimeCapabilitySchema,
				"runtime":            string(tc.target),
				"methods":            []string{RuntimeMethodProvision, RuntimeMethodSuspend},
				"control_schema":     domain.SoulFactoryRuntimeControlSchema,
				"controller_pubkeys": []string{controller.pubkey},
			}, nostr.Tags{{tagParameterizedD, "main"}, {tagRuntime, string(tc.target)}})
			transport := &fakeRuntimeAdapterTransport{capabilities: []*nostr.Event{oldCapability, newCapability}}
			adapter := newConformanceAdapter(t, controller, tc.target, transport)

			capabilities, err := adapter.DiscoverCapabilities(t.Context(), domain.SoulRelayPolicySpec{})
			if err != nil {
				t.Fatalf("DiscoverCapabilities error = %v", err)
			}
			if len(capabilities) != 1 || capabilities[0].ID != newCapability.ID.Hex() {
				t.Fatalf("latest-wins dedupe = %+v, want only newest", capabilities)
			}
			if !capabilities[0].Supports(tc.target, RuntimeMethodSuspend, controller.pubkey) {
				t.Fatalf("newest capability lost methods: %+v", capabilities[0])
			}
		})
	}
}
