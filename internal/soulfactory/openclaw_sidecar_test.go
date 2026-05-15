package soulfactory

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nbd-wtf/go-nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

type fakeOpenClawSidecarTransport struct {
	published []nostr.Event
	sub       *RelayBusSubscription
}

func (f *fakeOpenClawSidecarTransport) Publish(_ context.Context, event nostr.Event) (int, error) {
	f.published = append(f.published, event)
	return 1, nil
}

func (f *fakeOpenClawSidecarTransport) SubscribeAllWithEOSE(_ context.Context, _ []nostr.Filter) (*RelayBusSubscription, error) {
	if f.sub == nil {
		return nil, errors.New("no subscription configured")
	}
	return f.sub, nil
}

func (f *fakeOpenClawSidecarTransport) Close() {}

type fakeOpenClawDriver struct {
	methods []string
	calls   []OpenClawControlInvocation
	outcome *OpenClawControlOutcome
	err     error
}

func (f *fakeOpenClawDriver) Methods() []string {
	if len(f.methods) > 0 {
		return f.methods
	}
	return append([]string{}, openClawSoulFactoryMethods...)
}

func (f *fakeOpenClawDriver) Execute(_ context.Context, invocation OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	f.calls = append(f.calls, invocation)
	if f.err != nil {
		return nil, f.err
	}
	if f.outcome != nil {
		return f.outcome, nil
	}
	return &OpenClawControlOutcome{Status: "success", Result: map[string]interface{}{"state": "running"}}, nil
}

func newTestOpenClawSidecar(t *testing.T, runtime, controller fakeSigner, transport *fakeOpenClawSidecarTransport, driver *fakeOpenClawDriver) *OpenClawSidecar {
	t.Helper()
	sidecar, err := NewOpenClawSidecar(OpenClawSidecarConfig{
		RuntimePubkey:            runtime.pubkey,
		Signer:                   runtime,
		TrustedControllerPubkeys: []string{controller.pubkey},
		Identifier:               "openclaw-test",
		Relays:                   []string{"wss://relay.example"},
		RelayHints: domain.SoulRelayPolicySpec{
			Read:    []string{"wss://read.example"},
			Write:   []string{"wss://write.example"},
			Control: []string{"wss://control.example"},
		},
		Transport: transport,
		Driver:    driver,
		Now:       time.Now,
	})
	if err != nil {
		t.Fatalf("NewOpenClawSidecar error = %v", err)
	}
	return sidecar
}

func TestOpenClawSidecarPublishesCompatibleCapability(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{methods: []string{RuntimeMethodProvision, RuntimeMethodSuspend}}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)

	if err := sidecar.PublishCapability(t.Context()); err != nil {
		t.Fatalf("PublishCapability error = %v", err)
	}
	if len(transport.published) != 1 {
		t.Fatalf("published count = %d, want 1", len(transport.published))
	}
	event := transport.published[0]
	capability, ok := ParseRuntimeCapabilityEvent(&event)
	if !ok {
		t.Fatal("published capability did not parse")
	}
	if capability.Pubkey != runtime.pubkey || capability.Runtime != domain.RuntimeTargetOpenClaw || !capability.Compatible {
		t.Fatalf("unexpected capability: %+v", capability)
	}
	if !capability.Supports(domain.RuntimeTargetOpenClaw, RuntimeMethodProvision, controller.pubkey) || !capability.Supports(domain.RuntimeTargetOpenClaw, RuntimeMethodSuspend, controller.pubkey) {
		t.Fatalf("capability does not advertise OpenClaw SoulFactory support: %+v", capability)
	}
	if got := capability.RelayHints.Control; !reflect.DeepEqual(got, []string{"wss://control.example"}) {
		t.Fatalf("control relay hints = %#v", got)
	}
}

func TestOpenClawSidecarValidatesTrustAddressingAndRequiredParams(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	untrusted := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)

	valid := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)
	if _, err := sidecar.ValidateControlEvent(valid); err != nil {
		t.Fatalf("valid request rejected: %+v", err)
	}

	badController := signedOpenClawControlRequest(t, untrusted, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)
	if _, err := sidecar.ValidateControlEvent(badController); err == nil || err.Code != "unauthorized_controller" {
		t.Fatalf("untrusted controller error = %+v, want unauthorized_controller", err)
	}

	misaddressed := signedOpenClawControlRequest(t, controller, stringsRepeat("f", 64), RuntimeMethodProvision, openClawProvisionParams(), nil)
	if _, err := sidecar.ValidateControlEvent(misaddressed); err == nil || err.Code != "misaddressed_request" {
		t.Fatalf("misaddressed error = %+v, want misaddressed_request", err)
	}

	missingReason := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodSuspend, map[string]interface{}{}, nil)
	if _, err := sidecar.ValidateControlEvent(missingReason); err == nil || err.Code != "missing_required_param" {
		t.Fatalf("missing reason error = %+v, want missing_required_param", err)
	}
}

func TestOpenClawSidecarExecutesProvisionAndPublishesCorrelatedResult(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{outcome: &OpenClawControlOutcome{Status: "success", Result: map[string]interface{}{"state": "running", "runtime_binding": "openclaw://agents/agent-alice"}}}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)
	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)

	result, err := sidecar.HandleControlEvent(t.Context(), request)
	if err != nil {
		t.Fatalf("HandleControlEvent error = %v", err)
	}
	if result.Status != "success" || result.Method != RuntimeMethodProvision || result.RequestEvent != request.ID || result.OperatorRequestEvent != "operator-request" {
		t.Fatalf("unexpected result envelope: %+v", result)
	}
	if len(driver.calls) != 1 || driver.calls[0].Method != RuntimeMethodProvision || driver.calls[0].AgentID != "agent-alice" {
		t.Fatalf("driver calls = %+v", driver.calls)
	}
	if len(transport.published) != 1 {
		t.Fatalf("published count = %d, want result", len(transport.published))
	}
	published := transport.published[0]
	if published.Kind != domain.KindRuntimeControlResult || published.PubKey != runtime.pubkey || !published.CheckID() {
		t.Fatalf("result event not signed by runtime: kind=%d pubkey=%s", published.Kind, published.PubKey)
	}
	if tagValue(published.Tags, tagEvent) != request.ID || tagValue(published.Tags, tagPubkey) != controller.pubkey || tagValue(published.Tags, "idempotency-key") != "idem-soulfactory.provision" {
		t.Fatalf("result tags are not correlated: %#v", published.Tags)
	}
	parsed, ok := parseRuntimeControlResultEvent(&published)
	if !ok || !runtimeResultCorrelates(parsed, request, RuntimeAdapterRequest{Method: RuntimeMethodProvision, IdempotencyKey: "idem-soulfactory.provision", Operator: RuntimeOperatorRef{RequestEvent: "operator-request"}, Target: RuntimeTargetRef{RuntimePubkey: runtime.pubkey, AgentID: "agent-alice"}, Soul: RuntimeSoulRef{ID: "soul-alice", SpecHash: "sha256:spec"}}, controller.pubkey) {
		t.Fatalf("published result does not parse/correlate: %+v", parsed)
	}
}

func TestOpenClawSidecarIdempotentReplayDoesNotRepeatSideEffects(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)
	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)

	if _, err := sidecar.HandleControlEvent(t.Context(), request); err != nil {
		t.Fatalf("first HandleControlEvent error = %v", err)
	}
	if _, err := sidecar.HandleControlEvent(t.Context(), request); err != nil {
		t.Fatalf("replay HandleControlEvent error = %v", err)
	}
	if len(driver.calls) != 1 {
		t.Fatalf("driver called %d times, want 1", len(driver.calls))
	}
	if len(transport.published) != 2 {
		t.Fatalf("published results = %d, want one per request event delivery", len(transport.published))
	}
}

func TestOpenClawSidecarPersistsIdempotencyAcrossRestart(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodProvision, openClawProvisionParams(), nil)
	storePath := t.TempDir() + "/idempotency.json"
	store1, err := NewFileOpenClawIdempotencyStore(storePath)
	if err != nil {
		t.Fatalf("NewFileOpenClawIdempotencyStore first error = %v", err)
	}
	transport1 := &fakeOpenClawSidecarTransport{}
	driver1 := &fakeOpenClawDriver{}
	sidecar1, err := NewOpenClawSidecar(OpenClawSidecarConfig{
		RuntimePubkey:            runtime.pubkey,
		Signer:                   runtime,
		TrustedControllerPubkeys: []string{controller.pubkey},
		Relays:                   []string{"wss://relay.example"},
		Transport:                transport1,
		Driver:                   driver1,
		IdempotencyStore:         store1,
		Now:                      time.Now,
	})
	if err != nil {
		t.Fatalf("NewOpenClawSidecar first error = %v", err)
	}
	if _, err := sidecar1.HandleControlEvent(t.Context(), request); err != nil {
		t.Fatalf("first HandleControlEvent error = %v", err)
	}
	if len(driver1.calls) != 1 {
		t.Fatalf("first driver calls = %d, want 1", len(driver1.calls))
	}

	store2, err := NewFileOpenClawIdempotencyStore(storePath)
	if err != nil {
		t.Fatalf("NewFileOpenClawIdempotencyStore second error = %v", err)
	}
	transport2 := &fakeOpenClawSidecarTransport{}
	driver2 := &fakeOpenClawDriver{}
	sidecar2, err := NewOpenClawSidecar(OpenClawSidecarConfig{
		RuntimePubkey:            runtime.pubkey,
		Signer:                   runtime,
		TrustedControllerPubkeys: []string{controller.pubkey},
		Relays:                   []string{"wss://relay.example"},
		Transport:                transport2,
		Driver:                   driver2,
		IdempotencyStore:         store2,
		Now:                      time.Now,
	})
	if err != nil {
		t.Fatalf("NewOpenClawSidecar second error = %v", err)
	}
	if _, err := sidecar2.HandleControlEvent(t.Context(), request); err != nil {
		t.Fatalf("replayed HandleControlEvent error = %v", err)
	}
	if len(driver2.calls) != 0 {
		t.Fatalf("restarted sidecar driver calls = %d, want 0", len(driver2.calls))
	}
	if len(transport2.published) != 1 || tagValue(transport2.published[0].Tags, tagEvent) != request.ID {
		t.Fatalf("restarted sidecar did not republish cached correlated result: %#v", transport2.published)
	}
}

func TestOpenClawSidecarExecutesLifecycleSuspend(t *testing.T) {
	runtime := newFakeSigner(t)
	controller := newFakeSigner(t)
	transport := &fakeOpenClawSidecarTransport{}
	driver := &fakeOpenClawDriver{outcome: &OpenClawControlOutcome{Status: "success", Result: map[string]interface{}{"state": "suspended"}}}
	sidecar := newTestOpenClawSidecar(t, runtime, controller, transport, driver)
	request := signedOpenClawControlRequest(t, controller, runtime.pubkey, RuntimeMethodSuspend, map[string]interface{}{"reason": "operator request"}, nil)

	result, err := sidecar.HandleControlEvent(t.Context(), request)
	if err != nil {
		t.Fatalf("HandleControlEvent error = %v", err)
	}
	if result.Status != "success" || result.Result["state"] != "suspended" {
		t.Fatalf("unexpected lifecycle result: %+v", result)
	}
	if len(driver.calls) != 1 || driver.calls[0].Method != RuntimeMethodSuspend {
		t.Fatalf("driver calls = %+v", driver.calls)
	}
}

func openClawProvisionParams() map[string]interface{} {
	return map[string]interface{}{
		"identity":     map[string]interface{}{"name": "Alice", "purpose": "help operators", "tier": "standard"},
		"runtime":      map[string]interface{}{"target": "openclaw", "capability_ref": "capability-event"},
		"permissions":  map[string]interface{}{"allowed_kinds": []int{1}, "tool_grants": []string{}, "approval_policy": "manual"},
		"relay_policy": map[string]interface{}{"read": []string{"wss://relay.example"}, "write": []string{"wss://relay.example"}, "control": []string{"wss://relay.example"}},
		"workspace":    map[string]interface{}{"repo": "/tmp/alice", "branch": "main"},
		"assets":       map[string]interface{}{"avatar_ref": "https://example.com/alice.png"},
	}
}

func signedOpenClawControlRequest(t *testing.T, signer fakeSigner, runtimePubkey, method string, params map[string]interface{}, mutate func(*nostr.Event, *RuntimeControlEnvelope)) *nostr.Event {
	t.Helper()
	specHash := "sha256:spec"
	if method == RuntimeMethodUpdate {
		specHash = "sha256:spec2"
	}
	envelope := RuntimeControlEnvelope{
		Schema:         domain.SoulFactoryRuntimeControlSchema,
		Method:         method,
		IdempotencyKey: "idem-" + method,
		RequestedAt:    int64(nostr.Now()),
		Operator:       RuntimeOperatorRef{Pubkey: stringsRepeat("a", 64), RequestEvent: "operator-request"},
		Controller:     RuntimeControllerRef{Pubkey: signer.pubkey},
		Target:         RuntimeTargetRef{Runtime: domain.RuntimeTargetOpenClaw, RuntimePubkey: runtimePubkey, AgentID: "agent-alice"},
		Soul:           RuntimeSoulRef{ID: "soul-alice", Draft: "draft-event", SpecHash: specHash},
		Params:         params,
	}
	event, err := BuildRuntimeControlRequestEvent(envelope)
	if err != nil {
		t.Fatalf("BuildRuntimeControlRequestEvent error = %v", err)
	}
	event.CreatedAt = nostr.Now()
	if mutate != nil {
		mutate(event, &envelope)
		content, err := json.Marshal(envelope)
		if err != nil {
			t.Fatalf("marshal mutated envelope: %v", err)
		}
		event.Content = string(content)
	}
	if err := signer.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign request: %v", err)
	}
	return event
}
