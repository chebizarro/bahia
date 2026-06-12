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
		tagRequestKind:   "5950",
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
	for key, want := range map[string]string{
		"schema":         "soulfactory-provisioning/v1",
		"method":         RuntimeMethodProvision,
		"agent_id":       "scout",
		"name":           "Scout",
		"tier":           string(domain.SoulTierHeavy),
		"template_ref":   "31950:template-author:scout",
		"draft_ref":      "31952:draft-author:scout",
		"draft_event_id": "draft-event-id",
		"spec_hash":      "sha256:spec",
		"brief":          "Map operator state",
	} {
		if got, _ := content[key].(string); got != want {
			t.Fatalf("content[%s] = %q, want %q in %s", key, got, want, event.Content)
		}
	}
	if _, ok := content["requested_at"].(float64); !ok {
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
