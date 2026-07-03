package soulfactory

import (
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

// TestTagEnvelope_RuntimeControlRequest_MethodFilterRoundTrip proves the
// runtime-control request envelope built by BuildRuntimeControlRequestEvent
// (producer) is matched by the OpenClaw sidecar subscription filter, which
// selects on tagPubkey + tagSchema + tagMethod. Guards drift of the
// method/pubkey/schema filter keys now shared via constants (bahia-s7o9).
func TestTagEnvelope_RuntimeControlRequest_MethodFilterRoundTrip(t *testing.T) {
	const runtimePubkey = "runtime-pubkey-hex"
	env := RuntimeControlEnvelope{
		Method: RuntimeMethodProvision,
		Schema: domain.SoulFactoryRuntimeControlSchema,
		Target: RuntimeTargetRef{RuntimePubkey: runtimePubkey, AgentID: "scout"},
	}
	event, err := BuildRuntimeControlRequestEvent(env)
	if err != nil {
		t.Fatalf("BuildRuntimeControlRequestEvent: %v", err)
	}

	// Consumer filter: mirrors OpenClaw sidecar subscription (pubkey+schema+method).
	filter := nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(domain.KindRuntimeControlRequest)},
		Tags: nostr.TagMap{
			tagPubkey: []string{runtimePubkey},
			tagSchema: []string{domain.SoulFactoryRuntimeControlSchema},
			tagMethod: []string{RuntimeMethodProvision},
		},
	}
	if !filter.Matches(*event) {
		t.Fatalf("runtime-control request not matched by sidecar filter; tags=%v", event.Tags)
	}

	// Drift guard: a filter requiring a different method must NOT match.
	wrong := nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(domain.KindRuntimeControlRequest)},
		Tags:  nostr.TagMap{tagMethod: []string{"some/other-method"}},
	}
	if wrong.Matches(*event) {
		t.Fatal("filter with mismatched method tag unexpectedly matched")
	}
}

// Exact-envelope round-trip tests (bahia-vkeh).
//
// These guard against producer/consumer tag drift: an event is built by the
// production Build*Event codec and then matched against the exact subscription
// Filter the consumer uses. If a producer tag key/value or a consumer filter
// key drifts, filter.Matches returns false and the test fails — surfacing the
// break at test time instead of as a silent relay-filter miss in production.

// TestTagEnvelope_AgentSoul_ProducerFilterRoundTrip covers the AgentSoul
// read-model d-tag shared by BuildAgentSoulEvent (producer) and the
// GetSoul / reactor soul-lookup filters (consumers), both keyed on
// tagParameterizedD.
func TestTagEnvelope_AgentSoul_ProducerFilterRoundTrip(t *testing.T) {
	soul := &domain.AgentSoul{
		AgentID:     "scout",
		Name:        "Scout",
		Tier:        domain.SoulTierStandard,
		NostrPubkey: soulTestPubKey("agent").Hex(),
		NostrNpub:   "npub1scout",
	}
	event := BuildAgentSoulEvent(soul)
	event.PubKey = soulTestPubKey("factory")
	event.ID = soulTestID("agent-soul")

	// Consumer filter: mirrors NostrClient.GetSoul / reactor soul lookup.
	filter := nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(domain.KindAgentSoul)},
		Tags:  nostr.TagMap{tagParameterizedD: []string{soul.AgentID}},
	}
	if !filter.Matches(*event) {
		t.Fatalf("AgentSoul event not matched by GetSoul filter; tags=%v", event.Tags)
	}

	// Drift guard: a different d-tag value must NOT match, proving the match
	// above is genuinely tag-sensitive rather than kind-only.
	wrong := nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(domain.KindAgentSoul)},
		Tags:  nostr.TagMap{tagParameterizedD: []string{"different-agent"}},
	}
	if wrong.Matches(*event) {
		t.Fatal("filter with mismatched d-tag value unexpectedly matched")
	}
}

// TestTagEnvelope_Provisioning_ProducerFilterRoundTrip covers the provisioning
// status/result reply envelope (e + p tags) shared by
// BuildProvisioningStatusEvent / BuildProvisioningErrorResultEvent (producers)
// and the receipt-await filter (consumer), both keyed on tagEvent + tagPubkey.
func TestTagEnvelope_Provisioning_ProducerFilterRoundTrip(t *testing.T) {
	reqEvent := &nostr.Event{
		Kind:   domain.KindProvisioningRequest,
		PubKey: soulTestPubKey("requester"),
	}
	reqEvent.ID = soulTestID("provisioning-request")

	status := BuildProvisioningStatusEvent(reqEvent, domain.ProvisioningStep("provisioning"), 1, 3, "working")
	errResult := BuildProvisioningErrorResultEvent(reqEvent, "provisioning", "boom")

	// Consumer filter: mirrors NostrClient provisioning receipt subscription
	// (kinds Status+Result, e=request id, p=requester pubkey).
	filter := nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(domain.KindProvisioningStatus), nostr.Kind(domain.KindProvisioningResult)},
		Tags: nostr.TagMap{
			tagEvent:  []string{reqEvent.ID.Hex()},
			tagPubkey: []string{reqEvent.PubKey.Hex()},
		},
	}
	if !filter.Matches(*status) {
		t.Fatalf("provisioning status event not matched by receipt filter; tags=%v", status.Tags)
	}
	if !filter.Matches(*errResult) {
		t.Fatalf("provisioning error result event not matched by receipt filter; tags=%v", errResult.Tags)
	}

	// Drift guard: a different request-event id must NOT match.
	wrong := nostr.Filter{
		Kinds: []nostr.Kind{nostr.Kind(domain.KindProvisioningStatus), nostr.Kind(domain.KindProvisioningResult)},
		Tags:  nostr.TagMap{tagEvent: []string{soulTestID("other-request").Hex()}},
	}
	if wrong.Matches(*status) {
		t.Fatal("filter with mismatched e-tag value unexpectedly matched")
	}
}
