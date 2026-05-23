package controlplane

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"go.uber.org/zap"
)

type recordingDNSOperator struct {
	zones        map[string]bool
	reconcileAll int
	reconciled   []string
}

func (o *recordingDNSOperator) ReconcileAll(context.Context) error {
	o.reconcileAll++
	return nil
}

func (o *recordingDNSOperator) ReconcileZone(_ context.Context, zoneName string) error {
	o.reconciled = append(o.reconciled, zoneName)
	return nil
}

func (o *recordingDNSOperator) HasZone(zoneName string) bool {
	return o.zones[zoneName]
}

func TestDNSDriftRemediateHandlerTriggersReconcile(t *testing.T) {
	reactor, capture, pubkey, operator := newDNSHandlerTestReactor(t)
	event := &nostr.Event{ID: "dns-remediate", PubKey: pubkey, Kind: KindDNSDriftRemediateRequest, Content: `{"zone":"prod.example"}`}

	reactor.handleDNSDriftRemediate(context.Background(), event)

	if len(operator.reconciled) != 1 || operator.reconciled[0] != "prod.example" {
		t.Fatalf("expected zone reconcile for prod.example, got %#v", operator.reconciled)
	}
	assertDNSPublishedKind(t, capture.events, KindDNSOperationStatus)
	result := assertDNSPublishedKind(t, capture.events, KindDNSDriftRemediateResult)
	assertDNSResultStatus(t, result, "success")
}

func TestDNSZoneCreateExistingZoneReturnsSuccess(t *testing.T) {
	reactor, capture, pubkey, operator := newDNSHandlerTestReactor(t)
	event := &nostr.Event{ID: "dns-zone-create", PubKey: pubkey, Kind: KindDNSZoneCreateRequest, Content: `{"zone":"prod.example"}`}

	reactor.handleDNSZoneCreate(context.Background(), event)

	if len(operator.reconciled) != 1 || operator.reconciled[0] != "prod.example" {
		t.Fatalf("expected zone reconcile for existing zone, got %#v", operator.reconciled)
	}
	result := assertDNSPublishedKind(t, capture.events, KindDNSZoneCreateResult)
	assertDNSResultStatus(t, result, "success")
}

func TestDNSZoneCreateUnknownZoneReturnsUnsupported(t *testing.T) {
	reactor, capture, pubkey, operator := newDNSHandlerTestReactor(t)
	operator.zones = map[string]bool{}
	event := &nostr.Event{ID: "dns-zone-create-unknown", PubKey: pubkey, Kind: KindDNSZoneCreateRequest, Content: `{"zone":"unknown.example"}`}

	reactor.handleDNSZoneCreate(context.Background(), event)

	if len(operator.reconciled) != 0 {
		t.Fatalf("expected no reconcile for unknown zone, got %#v", operator.reconciled)
	}
	result := assertDNSPublishedKind(t, capture.events, KindDNSZoneCreateResult)
	assertDNSResultStatus(t, result, "failed")
	assertDNSResultStep(t, result, "unsupported")
}

func TestDNSUnsupportedHandlersPublishDeterministicResults(t *testing.T) {
	reactor, capture, pubkey, _ := newDNSHandlerTestReactor(t)
	cases := []struct {
		kind       int
		resultKind int
	}{
		{kind: KindDNSRecordOverrideRequest, resultKind: KindDNSRecordOverrideResult},
		{kind: KindDNSPolicyApplyRequest, resultKind: KindDNSPolicyApplyResult},
		{kind: KindDNSBackendRegisterRequest, resultKind: KindDNSBackendRegisterResult},
	}
	for _, tc := range cases {
		capture.events = nil
		reactor.handleDNSRequest(context.Background(), &nostr.Event{ID: "dns-unsupported", PubKey: pubkey, Kind: tc.kind, Content: `{}`})
		result := assertDNSPublishedKind(t, capture.events, tc.resultKind)
		assertDNSResultStatus(t, result, "failed")
		assertDNSResultStep(t, result, "unsupported")
	}
}

func newDNSHandlerTestReactor(t *testing.T) (*Reactor, *captureNostrPublisher, string, *recordingDNSOperator) {
	t.Helper()
	privateKey := nostr.GeneratePrivateKey()
	signer, err := NewPrivateKeySigner(privateKey)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	pubkey, err := nostr.GetPublicKey(privateKey)
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	capture := &captureNostrPublisher{published: 1}
	operator := &recordingDNSOperator{zones: map[string]bool{"prod.example": true}}
	reactor := NewReactor(Config{AuthorizedPubkeys: []string{pubkey}}, nil, nil, signer, zap.NewNop(), WithControlPlanePublisher(capture), WithDNSOperator(operator))
	return reactor, capture, pubkey, operator
}

func assertDNSPublishedKind(t *testing.T, events []nostr.Event, kind int) nostr.Event {
	t.Helper()
	for _, ev := range events {
		if ev.Kind == kind {
			if ok, err := ev.CheckSignature(); err != nil || !ok {
				t.Fatalf("kind %d signature invalid: ok=%v err=%v", kind, ok, err)
			}
			return ev
		}
	}
	t.Fatalf("kind %d not published; events=%#v", kind, events)
	return nostr.Event{}
}

func assertDNSResultStatus(t *testing.T, event nostr.Event, want string) {
	t.Helper()
	var content map[string]any
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		t.Fatalf("decode DNS result content: %v", err)
	}
	if got := content["status"]; got != want {
		t.Fatalf("status = %v, want %s; content=%s", got, want, event.Content)
	}
}

func assertDNSResultStep(t *testing.T, event nostr.Event, want string) {
	t.Helper()
	var content map[string]any
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		t.Fatalf("decode DNS result content: %v", err)
	}
	if got := content["step"]; got != want {
		t.Fatalf("step = %v, want %s; content=%s", got, want, event.Content)
	}
}
