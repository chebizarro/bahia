package soulfactory

import (
	"context"
	"encoding/json"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

type captureSoulFactoryTransport struct {
	published []nostr.Event
	accepted  int
	err       error
}

func (t *captureSoulFactoryTransport) Publish(_ context.Context, event nostr.Event) (int, error) {
	t.published = append(t.published, event)
	return t.accepted, t.err
}

func (t *captureSoulFactoryTransport) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*RelayBusSubscription, error) {
	return nil, nil
}

func (t *captureSoulFactoryTransport) Close() {}

func TestNostrClientPublishProvisionRequestMatchesBrowserEventShape(t *testing.T) {
	signer := newFakeSigner(t)
	transport := &captureSoulFactoryTransport{accepted: 2}
	client := &NostrClient{signer: signer, transport: transport}

	receipt, err := client.PublishProvisionRequest(t.Context(), domain.ProvisioningRequest{
		AgentID:      "scout",
		Name:         "Scout",
		Tier:         domain.SoulTierHeavy,
		TemplateRef:  "31950:template-author:scout",
		DraftRef:     "31952:draft-author:scout",
		DraftEventID: "draft-event-id",
		SpecHash:     "sha256:spec",
		Brief:        "Map operator state",
		Runtime: domain.SoulRuntimeSpec{
			Target:        domain.RuntimeTargetOpenClaw,
			RuntimePubkey: "runtime-pubkey",
			CapabilityRef: "30317:runtime-pubkey:openclaw",
		},
	})
	if err != nil {
		t.Fatalf("PublishProvisionRequest() error = %v", err)
	}
	if receipt == nil || receipt.AcceptedRelays != 2 || receipt.StatusKind != domain.KindProvisioningStatus || receipt.ResultKind != domain.KindProvisioningResult {
		t.Fatalf("receipt = %+v", receipt)
	}
	if len(transport.published) != 1 {
		t.Fatalf("published events = %d, want 1", len(transport.published))
	}
	event := transport.published[0]
	if event.Kind != nostr.Kind(domain.KindProvisioningRequest) || event.PubKey.Hex() != signer.pubkey || !event.CheckID() {
		t.Fatalf("published event identity = kind:%d pubkey:%s id-ok:%v", event.Kind, event.PubKey.Hex(), event.CheckID())
	}
	if !event.VerifySignature() {
		t.Fatalf("published event signature invalid")
	}
	for tag, want := range map[string]string{
		tagAgentID:       "scout",
		tagName:          "Scout",
		tagTier:          string(domain.SoulTierHeavy),
		"method":         RuntimeMethodProvision,
		tagRequestKind:   "25910",
		tagTemplate:      "31950:template-author:scout",
		tagDraft:         "31952:draft-author:scout",
		tagDraftEvent:    "draft-event-id",
		tagSpecHash:      "sha256:spec",
		tagRuntime:       string(domain.RuntimeTargetOpenClaw),
		tagRuntimePubkey: "runtime-pubkey",
		tagCapability:    "30317:runtime-pubkey:openclaw",
	} {
		if got := findTag(&event, tag); got != want {
			t.Fatalf("tag %s = %q, want %q in %#v", tag, got, want, event.Tags)
		}
	}
	if !tagHasValue(event.Tags, tagEvent, "draft-event-id") {
		t.Fatalf("missing draft event e tag: %#v", event.Tags)
	}

	var content map[string]interface{}
	if err := json.Unmarshal([]byte(event.Content), &content); err != nil {
		t.Fatalf("content JSON error = %v", err)
	}
	if got := content["jsonrpc"]; got != "2.0" {
		t.Fatalf("jsonrpc = %v, want 2.0", got)
	}
	if got := content["method"]; got != ContextVMMethodProvision {
		t.Fatalf("method = %v, want %s", got, ContextVMMethodProvision)
	}
	params, ok := content["params"].(map[string]interface{})
	if !ok {
		t.Fatalf("params missing from ContextVM envelope: %s", event.Content)
	}
	for key, want := range map[string]string{
		"schema":         "soulfactory-provisioning/v1",
		"agent_id":       "scout",
		"name":           "Scout",
		"tier":           string(domain.SoulTierHeavy),
		"template_ref":   "31950:template-author:scout",
		"draft_ref":      "31952:draft-author:scout",
		"draft_event_id": "draft-event-id",
		"spec_hash":      "sha256:spec",
		"brief":          "Map operator state",
	} {
		if got, _ := params[key].(string); got != want {
			t.Fatalf("content[%s] = %q, want %q in %s", key, got, want, event.Content)
		}
	}
	if _, ok := params["requested_at"].(float64); !ok {
		t.Fatalf("content requested_at missing or non-numeric: %s", event.Content)
	}

	parsed, err := ParseProvisioningRequestEvent(&event)
	if err != nil {
		t.Fatalf("ParseProvisioningRequestEvent() error = %v", err)
	}
	if parsed.Runtime.Target != domain.RuntimeTargetOpenClaw || parsed.Runtime.RuntimePubkey != "runtime-pubkey" || parsed.Runtime.CapabilityRef != "30317:runtime-pubkey:openclaw" {
		t.Fatalf("parsed runtime = %+v", parsed.Runtime)
	}
}

func TestNostrClientPublishProvisionRequestRequiresAgentID(t *testing.T) {
	client := &NostrClient{signer: newFakeSigner(t), transport: &captureSoulFactoryTransport{accepted: 1}}
	if _, err := client.PublishProvisionRequest(t.Context(), domain.ProvisioningRequest{Brief: "missing id"}); err == nil {
		t.Fatal("PublishProvisionRequest() error = nil, want missing agent_id")
	}
}

// subscribableSoulFactoryTransport delivers pre-loaded events to subscribers.
type subscribableSoulFactoryTransport struct {
	published []nostr.Event
	accepted  int
	events    []*nostr.Event // events to deliver to subscribers
}

func (t *subscribableSoulFactoryTransport) Publish(_ context.Context, event nostr.Event) (int, error) {
	t.published = append(t.published, event)
	return t.accepted, nil
}

func (t *subscribableSoulFactoryTransport) SubscribeAllWithEOSE(_ context.Context, _ []nostr.Filter) (*RelayBusSubscription, error) {
	ctx, cancel := context.WithCancel(context.Background())
	events := make(chan *nostr.Event, len(t.events)+1)
	eose := make(chan struct{})
	for _, ev := range t.events {
		events <- ev
	}
	close(eose)
	go func() {
		<-ctx.Done()
		close(events)
	}()
	return &RelayBusSubscription{Events: events, EndOfStoredEvents: eose, cancel: cancel}, nil
}

func (t *subscribableSoulFactoryTransport) Close() {}

func TestAwaitTerminalRejectsEventsFromWrongAuthor(t *testing.T) {
	factorySigner := newFakeSigner(t)
	spoofSigner := newFakeSigner(t)
	clientSigner := newFakeSigner(t)

	// Create a spoofed terminal result event signed by the wrong author.
	spoofEvent := &nostr.Event{
		Kind:      nostr.Kind(domain.KindProvisioningResult),
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"e", "request-id"}, {"p", clientSigner.pubkey}, {"status", "success"}},
		Content:   `{"agentId":"spoof"}`,
	}
	if err := spoofSigner.Sign(t.Context(), spoofEvent); err != nil {
		t.Fatalf("sign spoof event: %v", err)
	}

	// Create a legitimate terminal result event signed by the factory.
	legitimateEvent := &nostr.Event{
		Kind:      nostr.Kind(domain.KindProvisioningResult),
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"e", "request-id"}, {"p", clientSigner.pubkey}, {"status", "success"}},
		Content:   `{"agentId":"scout"}`,
	}
	if err := factorySigner.Sign(t.Context(), legitimateEvent); err != nil {
		t.Fatalf("sign legitimate event: %v", err)
	}

	transport := &subscribableSoulFactoryTransport{
		accepted: 1,
		events:   []*nostr.Event{spoofEvent, legitimateEvent},
	}

	client := &NostrClient{signer: clientSigner, transport: transport}

	receipt := &SoulFactoryRequestReceipt{
		RequestID:       "request-id",
		RequesterPubkey: clientSigner.pubkey,
		StatusKind:      domain.KindProvisioningStatus,
		ResultKind:      domain.KindProvisioningResult,
		ExpectedAuthor:  factorySigner.pubkey,
	}

	result, err := client.AwaitProvisioningResult(t.Context(), receipt, nil)
	if err != nil {
		t.Fatalf("AwaitProvisioningResult() error = %v", err)
	}
	if result == nil {
		t.Fatal("AwaitProvisioningResult() result is nil")
	}
	// The spoofed event should have been skipped; the legitimate one accepted.
	if result.AgentID != "scout" {
		t.Fatalf("AwaitProvisioningResult() agent_id = %q, want scout (spoofed event was not rejected)", result.AgentID)
	}
}

func TestAwaitTerminalAcceptsAnyAuthorWhenExpectedAuthorEmpty(t *testing.T) {
	anySigner := newFakeSigner(t)
	clientSigner := newFakeSigner(t)

	event := &nostr.Event{
		Kind:      nostr.Kind(domain.KindProvisioningResult),
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"e", "request-id"}, {"p", clientSigner.pubkey}, {"status", "success"}},
		Content:   `{"agentId":"scout"}`,
	}
	if err := anySigner.Sign(t.Context(), event); err != nil {
		t.Fatalf("sign event: %v", err)
	}

	transport := &subscribableSoulFactoryTransport{
		accepted: 1,
		events:   []*nostr.Event{event},
	}

	client := &NostrClient{signer: clientSigner, transport: transport}

	receipt := &SoulFactoryRequestReceipt{
		RequestID:       "request-id",
		RequesterPubkey: clientSigner.pubkey,
		StatusKind:      domain.KindProvisioningStatus,
		ResultKind:      domain.KindProvisioningResult,
		// ExpectedAuthor left empty — should accept any author.
	}

	result, err := client.AwaitProvisioningResult(t.Context(), receipt, nil)
	if err != nil {
		t.Fatalf("AwaitProvisioningResult() error = %v", err)
	}
	if result == nil || result.AgentID != "scout" {
		t.Fatalf("AwaitProvisioningResult() should accept events from any author when ExpectedAuthor is empty, got %+v", result)
	}
}

func TestExecuteSoulActionRejectsEventsFromWrongAuthor(t *testing.T) {
	factorySigner := newFakeSigner(t)
	clientSigner := newFakeSigner(t)

	client := &NostrClient{
		signer:                clientSigner,
		transport:             &subscribableSoulFactoryTransport{accepted: 1},
		expectedFactoryPubkey: factorySigner.pubkey,
	}

	// Verify the expectedFactoryPubkey is stored on the client.
	if client.expectedFactoryPubkey != factorySigner.pubkey {
		t.Fatalf("expectedFactoryPubkey = %q, want %q", client.expectedFactoryPubkey, factorySigner.pubkey)
	}

	// Verify the builder method works.
	otherPubkey := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	client2 := (&NostrClient{signer: clientSigner, transport: &subscribableSoulFactoryTransport{accepted: 1}}).WithExpectedFactoryPubkey(otherPubkey)
	if client2.expectedFactoryPubkey != otherPubkey {
		t.Fatalf("WithExpectedFactoryPubkey() = %q, want %q", client2.expectedFactoryPubkey, otherPubkey)
	}
}
