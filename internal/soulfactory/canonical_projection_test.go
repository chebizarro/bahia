package soulfactory

import (
	"encoding/json"
	"log/slog"
	"testing"

	"fiatjaf.com/nostr"
	cascadia "git.sharegap.net/cascadia/cascadia-go"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestCanonicalProvisioningProjectionForContextVMRequest(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, fakeGenerator{}, signer, slog.Default())
	capture := attachPublishCapture(reactor)
	request := contextVMTestRequest(t, ContextVMMethodProvision, `{"agent_id":"ravel","brief":"review fleets"}`)
	interop, err := contextVMProvisioningEvent(request)
	if err != nil {
		t.Fatalf("contextVMProvisioningEvent() error = %v", err)
	}
	result := BuildProvisioningErrorResultEvent(interop, "deploy", "runtime unavailable")
	if err := signer.Sign(t.Context(), result); err != nil {
		t.Fatalf("sign result: %v", err)
	}
	if err := reactor.publishCanonicalProvisioningObservable(t.Context(), interop, result); err != nil {
		t.Fatalf("publishCanonicalProvisioningObservable() error = %v", err)
	}
	if len(capture.events) != 2 {
		t.Fatalf("published events = %d, want state + audit", len(capture.events))
	}
	state, audit := capture.events[0], capture.events[1]
	if state.Kind != nostr.Kind(cascadia.CAS_CP_STATE) || audit.Kind != nostr.Kind(cascadia.CAS_AUDIT) {
		t.Fatalf("published kinds = %d, %d", state.Kind, audit.Kind)
	}
	wantD := canonicalProvisioningCoordinatePrefix + request.Event.ID.Hex()
	if findTag(state, "d") != wantD || findTag(state, "status") != "error" || findTag(audit, "state") != wantD {
		t.Fatalf("projection tags: state=%v audit=%v", state.Tags, audit.Tags)
	}
	if !state.VerifySignature() || !audit.VerifySignature() {
		t.Fatal("canonical projections were not signed")
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(state.Content), &body); err != nil {
		t.Fatalf("decode state content: %v", err)
	}
	if body["request_event_id"] != request.Event.ID.Hex() || body["status"] != "error" || body["error"] != "runtime unavailable" {
		t.Fatalf("state content = %#v", body)
	}
}

func TestCanonicalProvisioningProjectionIgnoresDirectInteropRequest(t *testing.T) {
	signer := newFakeSigner(t)
	reactor := NewReactor(Config{Relays: []string{"wss://relay.example"}}, fakeGenerator{}, signer, slog.Default())
	capture := attachPublishCapture(reactor)
	request := &nostr.Event{ID: soulTestID("direct-5950"), Kind: nostr.Kind(domain.KindProvisioningRequest)}
	result := BuildProvisioningErrorResultEvent(request, "deploy", "runtime unavailable")
	if err := reactor.publishCanonicalProvisioningObservable(t.Context(), request, result); err != nil {
		t.Fatalf("publishCanonicalProvisioningObservable() error = %v", err)
	}
	if len(capture.events) != 0 {
		t.Fatalf("published events = %d, want zero", len(capture.events))
	}
}
