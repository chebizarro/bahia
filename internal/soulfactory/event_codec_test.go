package soulfactory

import (
	"encoding/json"
	"testing"

	"github.com/nbd-wtf/go-nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestEventCodecParsesProvisioningRequestsLegacyAndStructured(t *testing.T) {
	legacy := &nostr.Event{
		ID:        "legacy-5950",
		Kind:      domain.KindProvisioningRequest,
		CreatedAt: nostr.Now(),
		PubKey:    "operator",
		Tags:      nostr.Tags{{"agent-id", "scout"}, {"name", "Scout"}},
		Content:   `{"brief":"Monitor deployments"}`,
	}
	req, err := ParseProvisioningRequestEvent(legacy)
	if err != nil {
		t.Fatalf("ParseProvisioningRequestEvent legacy error = %v", err)
	}
	if req.AgentID != "scout" || req.Name != "Scout" || req.Brief != "Monitor deployments" {
		t.Fatalf("legacy request = %+v", req)
	}
	if req.Tier != domain.SoulTierStandard {
		t.Fatalf("legacy tier = %s, want default standard", req.Tier)
	}

	structured := &nostr.Event{
		ID:        "structured-5950",
		Kind:      domain.KindProvisioningRequest,
		CreatedAt: nostr.Now(),
		PubKey:    "operator",
		Tags: nostr.Tags{
			{"agent-id", "navigator"},
			{"draft", "31952:operator:navigator"},
			{"e", "draft-event-id", "", "draft"},
			{"spec-hash", "sha256:from-tag"},
		},
		Content: `{"name":"Navigator","tier":"heavy","brief":"","spec_hash":"sha256:from-content"}`,
	}
	req, err = ParseProvisioningRequestEvent(structured)
	if err != nil {
		t.Fatalf("ParseProvisioningRequestEvent structured error = %v", err)
	}
	if req.AgentID != "navigator" || req.Name != "Navigator" || req.Tier != domain.SoulTierHeavy {
		t.Fatalf("structured request identity = %+v", req)
	}
	if req.DraftRef != "31952:operator:navigator" || req.DraftEventID != "draft-event-id" {
		t.Fatalf("structured draft refs = %+v", req)
	}
	if req.SpecHash != "sha256:from-tag" {
		t.Fatalf("structured spec hash = %q, want tag to win", req.SpecHash)
	}
}

func TestEventCodecRejectsMalformedJSONContent(t *testing.T) {
	badRequest := &nostr.Event{
		ID:        "bad-5950",
		Kind:      domain.KindProvisioningRequest,
		CreatedAt: nostr.Now(),
		PubKey:    "operator",
		Tags:      nostr.Tags{{"agent-id", "scout"}},
		Content:   `{"brief":`,
	}
	if _, err := ParseProvisioningRequestEvent(badRequest); err == nil {
		t.Fatal("ParseProvisioningRequestEvent malformed JSON error = nil")
	}

	badAction := &nostr.Event{
		ID:        "bad-1950",
		Kind:      domain.KindSoulAction,
		CreatedAt: nostr.Now(),
		PubKey:    "operator",
		Tags:      nostr.Tags{{"soul", "31951:factory:scout"}, {"action", "update"}},
		Content:   `{"patch":`,
	}
	if _, err := ParseSoulActionEvent(badAction); err == nil {
		t.Fatal("ParseSoulActionEvent malformed JSON error = nil")
	}
}

func TestEventCodecParsesSoulActionsLegacyAndStructured(t *testing.T) {
	legacy := &nostr.Event{
		ID:        "legacy-1950",
		Kind:      domain.KindSoulAction,
		CreatedAt: nostr.Now(),
		PubKey:    "operator",
		Tags:      nostr.Tags{{"soul", "31951:factory:scout"}, {"action", "regenerate"}},
		Content:   `{"brief":"New mission brief"}`,
	}
	action, err := ParseSoulActionEvent(legacy)
	if err != nil {
		t.Fatalf("ParseSoulActionEvent legacy error = %v", err)
	}
	if action.SoulRef != "31951:factory:scout" || action.Action != domain.SoulActionRegenerate || action.NewBrief != "New mission brief" {
		t.Fatalf("legacy action = %+v", action)
	}

	structured := &nostr.Event{
		ID:        "structured-1950",
		Kind:      domain.KindSoulAction,
		CreatedAt: nostr.Now(),
		PubKey:    "operator",
		Tags: nostr.Tags{
			{"soul", "31951:factory:scout"},
			{"action", "update"},
			{"draft", "31952:operator:scout"},
			{"spec-hash", "sha256:new"},
			{"previous-spec-hash", "sha256:old"},
		},
		Content: `{"reason":"adjust permissions","patch":{"permissions":{"approval_policy":"manual"}}}`,
	}
	action, err = ParseSoulActionEvent(structured)
	if err != nil {
		t.Fatalf("ParseSoulActionEvent structured error = %v", err)
	}
	if action.Action != domain.SoulActionUpdate || action.DraftRef != "31952:operator:scout" || action.SpecHash != "sha256:new" || action.PreviousSpecHash != "sha256:old" {
		t.Fatalf("structured action refs = %+v", action)
	}
	if action.Reason != "adjust permissions" || action.Patch == nil {
		t.Fatalf("structured action content = %+v", action)
	}
}

func TestEventCodecBuildsAndParsesAgentSoulReadModelWithRuntimeFields(t *testing.T) {
	soul := &domain.AgentSoul{
		AgentID:          "scout",
		Name:             "Scout",
		Purpose:          "Watch deployments",
		Tier:             domain.SoulTierStandard,
		Status:           domain.SoulStatusActive,
		NostrPubkey:      "agent-pubkey",
		NostrNpub:        "npub1agent",
		SoulMD:           "# Scout",
		AllowedKinds:     []int{1, domain.KindSoulAction},
		ToolGrants:       []domain.ToolGrant{{MCPServer: "memory", Scopes: []string{"read"}}},
		DraftRef:         "31952:operator:scout",
		SpecHash:         "sha256:new",
		PreviousSpecHash: "sha256:old",
		Runtime: domain.SoulRuntimeSpec{
			Target:         domain.RuntimeTargetOpenClaw,
			RuntimePubkey:  "runtime-pubkey",
			RuntimeBinding: "openclaw://agents/scout",
			State:          "running",
		},
		CapabilityRef: "30317:runtime:openclaw",
		RelayPolicy: domain.SoulRelayPolicySpec{
			Read:    []string{"wss://read.example"},
			Write:   []string{"wss://write.example"},
			Control: []string{"wss://control.example"},
		},
		PermissionSpec: domain.SoulPermissionSpec{ApprovalPolicy: "manual"},
		Assets:         domain.SoulAssetRefs{AvatarRef: "blob:avatar", VoiceRef: "blob:voice"},
	}

	event := BuildAgentSoulEvent(soul)
	if event.Kind != domain.KindAgentSoul {
		t.Fatalf("agent soul kind = %d", event.Kind)
	}
	if got := findTag(event, "runtime"); got != string(domain.RuntimeTargetOpenClaw) {
		t.Fatalf("runtime tag = %q", got)
	}
	parsed := ParseAgentSoulEvent(event)
	if parsed.AgentID != soul.AgentID || parsed.Runtime.RuntimePubkey != soul.Runtime.RuntimePubkey || parsed.Runtime.RuntimeBinding != soul.Runtime.RuntimeBinding {
		t.Fatalf("parsed runtime soul = %+v", parsed)
	}
	if parsed.SpecHash != "sha256:new" || parsed.PreviousSpecHash != "sha256:old" || parsed.Assets.VoiceRef != "blob:voice" {
		t.Fatalf("parsed new soul fields = %+v", parsed)
	}
	if len(parsed.AllowedKinds) != 2 || len(parsed.ToolGrants) != 1 || parsed.ToolGrants[0].Scopes[0] != "read" {
		t.Fatalf("parsed legacy permission tags = %+v", parsed)
	}
}

func TestEventCodecBuildsLegacyAndCanonicalActionResults(t *testing.T) {
	action := &domain.SoulAction{EventID: "action-event", SoulRef: "31951:factory:scout", Action: domain.SoulActionSuspend, Initiator: "operator"}

	legacy, err := BuildActionResultEvent(action, "completed", map[string]interface{}{"ok": true}, ActionResultLegacy)
	if err != nil {
		t.Fatalf("BuildActionResultEvent legacy error = %v", err)
	}
	if legacy.Kind != domain.KindSoulActionLegacyResult {
		t.Fatalf("legacy action result kind = %d", legacy.Kind)
	}
	if got := findTag(legacy, "e"); got != "action-event" {
		t.Fatalf("legacy e tag = %q", got)
	}

	canonical, err := BuildActionResultEvent(action, "completed", map[string]interface{}{"ok": true}, ActionResultCanonical)
	if err != nil {
		t.Fatalf("BuildActionResultEvent canonical error = %v", err)
	}
	if canonical.Kind != domain.KindProvisioningResult {
		t.Fatalf("canonical action result kind = %d", canonical.Kind)
	}
	if got := findTag(canonical, "request-kind"); got != "1950" {
		t.Fatalf("canonical request-kind = %q", got)
	}
	var payload map[string]bool
	if err := json.Unmarshal([]byte(canonical.Content), &payload); err != nil || !payload["ok"] {
		t.Fatalf("canonical payload = %#v err=%v", payload, err)
	}
}

func TestEventCodecDraftAndRuntimeControlShapes(t *testing.T) {
	draft := &domain.SoulDraft{
		AgentID: "scout",
		Name:    "Scout",
		Tier:    domain.SoulTierStandard,
		Content: domain.SoulDraftContent{
			Brief:    "Monitor deployments",
			SpecHash: "sha256:draft",
			Identity: domain.SoulIdentitySpec{Name: "Scout", Purpose: "Watch deploys", Tier: domain.SoulTierStandard},
			Runtime:  domain.SoulRuntimeSpec{Target: domain.RuntimeTargetMetiq, RuntimePubkey: "runtime-pubkey"},
			RelayPolicy: domain.SoulRelayPolicySpec{
				Control: []string{"wss://control.example"},
			},
		},
	}
	draftEvent, err := BuildSoulDraftEvent(draft)
	if err != nil {
		t.Fatalf("BuildSoulDraftEvent error = %v", err)
	}
	draftEvent.ID = "draft-event-id"
	parsedDraft, err := ParseSoulDraftEvent(draftEvent)
	if err != nil {
		t.Fatalf("ParseSoulDraftEvent error = %v", err)
	}
	if parsedDraft.AgentID != "scout" || parsedDraft.Content.Runtime.Target != domain.RuntimeTargetMetiq || parsedDraft.Content.SpecHash != "sha256:draft" {
		t.Fatalf("parsed draft = %+v", parsedDraft)
	}

	envelope := RuntimeControlEnvelope{
		Method:         "soulfactory.provision",
		IdempotencyKey: "sha256:idempotency",
		RequestedAt:    1715700000,
		Operator:       RuntimeOperatorRef{Pubkey: "operator", RequestEvent: "5950-event"},
		Controller:     RuntimeControllerRef{Pubkey: "controller"},
		Target:         RuntimeTargetRef{Runtime: domain.RuntimeTargetOpenClaw, RuntimePubkey: "runtime-pubkey", AgentID: "scout"},
		Soul:           RuntimeSoulRef{ID: "scout", Draft: draftEvent.ID, SpecHash: "sha256:draft"},
		Params:         map[string]interface{}{"identity": map[string]interface{}{"name": "Scout"}},
	}
	runtimeEvent, err := BuildRuntimeControlRequestEvent(envelope)
	if err != nil {
		t.Fatalf("BuildRuntimeControlRequestEvent error = %v", err)
	}
	if runtimeEvent.Kind != domain.KindRuntimeControlRequest || findTag(runtimeEvent, "schema") != domain.SoulFactoryRuntimeControlSchema {
		t.Fatalf("runtime event kind/tags = %d %+v", runtimeEvent.Kind, runtimeEvent.Tags)
	}
	parsedEnvelope, err := ParseRuntimeControlRequestEvent(runtimeEvent)
	if err != nil {
		t.Fatalf("ParseRuntimeControlRequestEvent error = %v", err)
	}
	if parsedEnvelope.Method != "soulfactory.provision" || parsedEnvelope.Target.Runtime != domain.RuntimeTargetOpenClaw || parsedEnvelope.Soul.SpecHash != "sha256:draft" || parsedEnvelope.Soul.Draft != "draft-event-id" {
		t.Fatalf("parsed runtime envelope = %+v", parsedEnvelope)
	}

	tagOnlyRuntime := &nostr.Event{
		Kind: domain.KindRuntimeControlRequest,
		Tags: nostr.Tags{
			{"p", "runtime-pubkey"},
			{"method", "soulfactory.update"},
			{"e", "operator-event"},
			{"soul", "scout"},
			{"agent-id", "scout"},
			{"controller", "controller"},
			{"idempotency-key", "sha256:tag-only"},
			{"spec-hash", "sha256:tag-spec"},
			{"schema", domain.SoulFactoryRuntimeControlSchema},
			{"draft", "draft-event-id"},
			{"runtime", string(domain.RuntimeTargetMetiq)},
		},
		Content: `{}`,
	}
	parsedEnvelope, err = ParseRuntimeControlRequestEvent(tagOnlyRuntime)
	if err != nil {
		t.Fatalf("ParseRuntimeControlRequestEvent tag-only error = %v", err)
	}
	if parsedEnvelope.Method != "soulfactory.update" || parsedEnvelope.Target.RuntimePubkey != "runtime-pubkey" || parsedEnvelope.Target.AgentID != "scout" || parsedEnvelope.Target.Runtime != domain.RuntimeTargetMetiq || parsedEnvelope.Operator.RequestEvent != "operator-event" || parsedEnvelope.Controller.Pubkey != "controller" || parsedEnvelope.Soul.Draft != "draft-event-id" || parsedEnvelope.Soul.SpecHash != "sha256:tag-spec" {
		t.Fatalf("tag-only parsed runtime envelope = %+v", parsedEnvelope)
	}
}
