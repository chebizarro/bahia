package soulfactory

import (
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/openagentsinc/bahia/internal/domain"
)

func TestEventCodecParsesProvisioningRequestsLegacyAndStructured(t *testing.T) {
	legacy := &nostr.Event{
		ID:        soulTestID("legacy-5950"),
		Kind:      nostr.Kind(domain.KindProvisioningRequest),
		CreatedAt: nostr.Now(),
		PubKey:    soulTestPubKey("operator"),
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
		ID:        soulTestID("structured-5950"),
		Kind:      nostr.Kind(domain.KindProvisioningRequest),
		CreatedAt: nostr.Now(),
		PubKey:    soulTestPubKey("operator"),
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
		ID:        soulTestID("bad-5950"),
		Kind:      nostr.Kind(domain.KindProvisioningRequest),
		CreatedAt: nostr.Now(),
		PubKey:    soulTestPubKey("operator"),
		Tags:      nostr.Tags{{"agent-id", "scout"}},
		Content:   `{"brief":`,
	}
	if _, err := ParseProvisioningRequestEvent(badRequest); err == nil {
		t.Fatal("ParseProvisioningRequestEvent malformed JSON error = nil")
	}

	badAction := &nostr.Event{
		ID:        soulTestID("bad-1950"),
		Kind:      nostr.Kind(domain.KindSoulAction),
		CreatedAt: nostr.Now(),
		PubKey:    soulTestPubKey("operator"),
		Tags:      nostr.Tags{{"soul", "31951:factory:scout"}, {"action", "update"}},
		Content:   `{"patch":`,
	}
	if _, err := ParseSoulActionEvent(badAction); err == nil {
		t.Fatal("ParseSoulActionEvent malformed JSON error = nil")
	}
}

func TestEventCodecParsesSoulActionsLegacyAndStructured(t *testing.T) {
	legacy := &nostr.Event{
		ID:        soulTestID("legacy-1950"),
		Kind:      nostr.Kind(domain.KindSoulAction),
		CreatedAt: nostr.Now(),
		PubKey:    soulTestPubKey("operator"),
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
		ID:        soulTestID("structured-1950"),
		Kind:      nostr.Kind(domain.KindSoulAction),
		CreatedAt: nostr.Now(),
		PubKey:    soulTestPubKey("operator"),
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

	draftEventOnly := &nostr.Event{
		ID:        soulTestID("draft-event-only-1950"),
		Kind:      nostr.Kind(domain.KindSoulAction),
		CreatedAt: nostr.Now(),
		PubKey:    soulTestPubKey("operator"),
		Tags: nostr.Tags{
			{"soul", "31951:factory:scout"},
			{"action", string(domain.SoulActionHotReload)},
			{"draft-event", "exact-draft-event"},
		},
		Content: `{"spec_hash":"sha256:hot"}`,
	}
	action, err = ParseSoulActionEvent(draftEventOnly)
	if err != nil {
		t.Fatalf("ParseSoulActionEvent draft-event-only error = %v", err)
	}
	if action.Action != domain.SoulActionHotReload || action.DraftEventID != "exact-draft-event" || action.DraftRef != "" || action.SpecHash != "sha256:hot" {
		t.Fatalf("draft-event-only action = %+v", action)
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
		DraftEventID:     "draft-event-id",
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
	if parsed.DraftEventID != "draft-event-id" || parsed.SpecHash != "sha256:new" || parsed.PreviousSpecHash != "sha256:old" || parsed.Assets.VoiceRef != "blob:voice" {
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

func TestEventCodecDraftV2CustomizationSpecsRoundTrip(t *testing.T) {
	draft := &domain.SoulDraft{
		AgentID: "scout",
		Name:    "Scout",
		Tier:    domain.SoulTierStandard,
		Content: domain.SoulDraftContent{
			Brief: "Monitor deployments",
			Identity: domain.SoulIdentitySpec{
				Name:    "Scout",
				Purpose: "Watch deploys",
				Tier:    domain.SoulTierStandard,
				Theme:   "warm",
				Emoji:   "🔍",
			},
			Persona: domain.SoulPersonaSpec{
				Traits:      []string{"curious", "thorough"},
				Style:       "conversational",
				Tone:        "friendly professional",
				Constraints: []string{"Always cite sources"},
				SystemPromptSections: map[string]string{
					"role":       "You are Scout.",
					"guidelines": "Be precise.",
				},
			},
			Avatar: domain.SoulAvatarSpec{
				Generation:   &domain.SoulAvatarGenerationSpec{Prompt: "Pixel art owl", StylePreset: "pixel-art", Seed: "scout-v1", Width: 512, Height: 512, Provider: "flux-comfyui"},
				GeneratedRef: "blossom:generated",
				Current:      "generated",
			},
			Voice: domain.SoulVoiceSpec{
				Provider:   "elevenlabs",
				PersonaID:  "scout-voice",
				Persona:    &domain.SoulVoicePersonaSpec{Label: "Scout Voice", Profile: "Researcher", Style: "articulate", Accent: "neutral american", Pacing: "measured"},
				AutoMode:   "tagged",
				SampleText: "Hello, I'm Scout.",
				Providers: map[string]map[string]any{
					"elevenlabs": {"voice_id": "pNInz6obpgDQGcFmaJgB", "model": "eleven_turbo_v2_5", "stability": 0.7},
				},
			},
			Memory: domain.SoulMemorySpec{
				EmbeddingProvider: "voyage",
				EmbeddingModel:    "voyage-3",
				Search:            &domain.SoulMemorySearchSpec{TopK: 10, ScoreThreshold: 0.7, Rerank: true, RerankModel: "cohere-rerank-v3"},
				Strategy:          "session-aware",
				AutoIndex:         true,
				RetentionDays:     90,
			},
		},
	}

	event, err := BuildSoulDraftEvent(draft)
	if err != nil {
		t.Fatalf("BuildSoulDraftEvent v2 error = %v", err)
	}
	if event.Kind != domain.KindSoulDraft {
		t.Fatalf("draft kind = %d", event.Kind)
	}
	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(event.Content), &raw); err != nil {
		t.Fatalf("draft content JSON error = %v", err)
	}
	if raw["schema"] != domain.SoulFactoryDraftSchemaV2 {
		t.Fatalf("draft schema = %#v", raw["schema"])
	}

	event.ID = soulTestID("draft-v2-event")
	parsed, err := ParseSoulDraftEvent(event)
	if err != nil {
		t.Fatalf("ParseSoulDraftEvent v2 error = %v", err)
	}
	if !parsed.Content.IsV2() || parsed.Content.Persona.SystemPromptSections["role"] != "You are Scout." {
		t.Fatalf("parsed persona/schema = %+v", parsed.Content)
	}
	if parsed.Content.Avatar.Generation == nil || parsed.Content.Avatar.Generation.Provider != "flux-comfyui" || parsed.Content.Avatar.Current != "generated" {
		t.Fatalf("parsed avatar = %+v", parsed.Content.Avatar)
	}
	if parsed.Content.Voice.Persona == nil || parsed.Content.Voice.Providers["elevenlabs"]["voice_id"] != "pNInz6obpgDQGcFmaJgB" {
		t.Fatalf("parsed voice = %+v", parsed.Content.Voice)
	}
	if parsed.Content.Memory.Search == nil || parsed.Content.Memory.Search.TopK != 10 || !parsed.Content.Memory.Search.Rerank {
		t.Fatalf("parsed memory = %+v", parsed.Content.Memory)
	}
}

func TestEventCodecRejectsInvalidDraftSpecReferences(t *testing.T) {
	cases := []struct {
		name    string
		content domain.SoulDraftContent
		want    string
	}{
		{
			name:    "avatar provider",
			content: domain.SoulDraftContent{Schema: domain.SoulFactoryDraftSchemaV2, Brief: "bad avatar", Avatar: domain.SoulAvatarSpec{Generation: &domain.SoulAvatarGenerationSpec{Prompt: "owl", Provider: "unknown-image"}}},
			want:    "avatar.generation.provider",
		},
		{
			name:    "voice model",
			content: domain.SoulDraftContent{Schema: domain.SoulFactoryDraftSchemaV2, Brief: "bad voice", Voice: domain.SoulVoiceSpec{Provider: "openai-tts", Providers: map[string]map[string]any{"openai-tts": {"voice_id": "alloy", "model": "not-a-tts-model"}}}},
			want:    "voice.providers.openai-tts.model",
		},
		{
			name:    "memory embedding model",
			content: domain.SoulDraftContent{Schema: domain.SoulFactoryDraftSchemaV2, Brief: "bad memory", Memory: domain.SoulMemorySpec{EmbeddingProvider: "voyage", EmbeddingModel: "text-embedding-3-small"}},
			want:    "memory.embedding_model",
		},
		{
			name:    "memory rerank model",
			content: domain.SoulDraftContent{Schema: domain.SoulFactoryDraftSchemaV2, Brief: "bad rerank", Memory: domain.SoulMemorySpec{EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small", Search: &domain.SoulMemorySearchSpec{Rerank: true, RerankModel: "not-a-reranker"}}},
			want:    "memory.search.rerank_model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := BuildSoulDraftEvent(&domain.SoulDraft{AgentID: "bad", Content: tc.content})
			if err == nil {
				t.Fatal("BuildSoulDraftEvent error = nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("BuildSoulDraftEvent error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestMergeSoulDraftContentPartialUpdateDeepMerges(t *testing.T) {
	base := domain.SoulDraftContent{
		Schema: domain.SoulFactoryDraftSchemaV2,
		Brief:  "Monitor deployments",
		Persona: domain.SoulPersonaSpec{
			Traits: []string{"curious"},
			Tone:   "friendly",
			SystemPromptSections: map[string]string{
				"role":       "You are Scout.",
				"guidelines": "Be precise.",
			},
		},
		Avatar: domain.SoulAvatarSpec{Generation: &domain.SoulAvatarGenerationSpec{Prompt: "Pixel art owl", StylePreset: "pixel-art", Width: 512, Height: 512, Provider: "flux-comfyui"}},
		Voice: domain.SoulVoiceSpec{
			Provider:   "elevenlabs",
			SampleText: "Hello.",
			Providers:  map[string]map[string]any{"elevenlabs": {"voice_id": "pNInz6obpgDQGcFmaJgB", "model": "eleven_turbo_v2_5", "stability": 0.7}},
		},
		Memory: domain.SoulMemorySpec{EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small", Search: &domain.SoulMemorySearchSpec{TopK: 5, ScoreThreshold: 0.5}, AutoIndex: true},
	}
	patch := map[string]interface{}{
		"persona": map[string]interface{}{
			"tone": nil,
			"system_prompt_sections": map[string]interface{}{
				"guidelines": "Be precise and cite sources.",
			},
		},
		"avatar": map[string]interface{}{
			"generated_ref": "blossom:new",
			"current":       "generated",
		},
		"voice": map[string]interface{}{
			"sample_text": nil,
			"providers": map[string]interface{}{
				"elevenlabs": map[string]interface{}{"stability": 0.85},
			},
		},
		"memory": map[string]interface{}{
			"auto_index": false,
			"search": map[string]interface{}{
				"top_k": 12,
			},
		},
	}

	merged, err := MergeSoulDraftContent(base, patch)
	if err != nil {
		t.Fatalf("MergeSoulDraftContent error = %v", err)
	}
	if merged.Schema != domain.SoulFactoryDraftSchemaV2 || merged.Brief != base.Brief {
		t.Fatalf("merged schema/brief = %q/%q", merged.Schema, merged.Brief)
	}
	if merged.Persona.Tone != "" || merged.Persona.SystemPromptSections["role"] != "You are Scout." || merged.Persona.SystemPromptSections["guidelines"] != "Be precise and cite sources." {
		t.Fatalf("merged persona = %+v", merged.Persona)
	}
	if merged.Avatar.GeneratedRef != "blossom:new" || merged.Avatar.Generation == nil || merged.Avatar.Generation.Prompt != "Pixel art owl" {
		t.Fatalf("merged avatar = %+v", merged.Avatar)
	}
	if merged.Voice.SampleText != "" || merged.Voice.Providers["elevenlabs"]["voice_id"] != "pNInz6obpgDQGcFmaJgB" || merged.Voice.Providers["elevenlabs"]["stability"] != 0.85 {
		t.Fatalf("merged voice = %+v", merged.Voice)
	}
	if merged.Memory.AutoIndex || merged.Memory.Search == nil || merged.Memory.Search.TopK != 12 || merged.Memory.Search.ScoreThreshold != 0.5 {
		t.Fatalf("merged memory = %+v", merged.Memory)
	}
}

func TestEventCodecMaintainsV1DraftBackwardCompatibility(t *testing.T) {
	event := &nostr.Event{
		ID:        soulTestID("legacy-draft-event"),
		Kind:      nostr.Kind(domain.KindSoulDraft),
		CreatedAt: nostr.Now(),
		PubKey:    soulTestPubKey("operator"),
		Tags:      nostr.Tags{{"d", "legacy"}, {"name", "Legacy"}, {"tier", "lightweight"}},
		Content:   `{"brief":"Legacy brief","avatar_prompt":"Pixel art owl","allowed_kinds":[1,30023],"assets":{"avatar_ref":"blossom:avatar","voice_ref":"voice:legacy"}}`,
	}

	parsed, err := ParseSoulDraftEvent(event)
	if err != nil {
		t.Fatalf("ParseSoulDraftEvent legacy error = %v", err)
	}
	if parsed.Content.Schema != "" || parsed.Content.SchemaVersion() != domain.SoulFactoryDraftSchemaV1 || parsed.Content.IsV2() {
		t.Fatalf("legacy schema = %q version=%q", parsed.Content.Schema, parsed.Content.SchemaVersion())
	}
	if parsed.Name != "Legacy" || parsed.Tier != domain.SoulTierLightweight || parsed.Content.AvatarPrompt != "Pixel art owl" || parsed.Content.Assets.VoiceRef != "voice:legacy" {
		t.Fatalf("legacy draft = %+v", parsed)
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
	draftEvent.ID = soulTestID("draft-event-id")
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
		Soul:           RuntimeSoulRef{ID: "scout", Draft: draftEvent.ID.Hex(), SpecHash: "sha256:draft"},
		Params:         map[string]interface{}{"identity": map[string]interface{}{"name": "Scout"}},
	}
	runtimeEvent, err := BuildRuntimeControlRequestEvent(envelope)
	if err != nil {
		t.Fatalf("BuildRuntimeControlRequestEvent error = %v", err)
	}
	if runtimeEvent.Kind != nostr.Kind(domain.KindRuntimeControlRequest) || findTag(runtimeEvent, "schema") != domain.SoulFactoryRuntimeControlSchema {
		t.Fatalf("runtime event kind/tags = %d %+v", runtimeEvent.Kind, runtimeEvent.Tags)
	}
	parsedEnvelope, err := ParseRuntimeControlRequestEvent(runtimeEvent)
	if err != nil {
		t.Fatalf("ParseRuntimeControlRequestEvent error = %v", err)
	}
	if parsedEnvelope.Method != "soulfactory.provision" || parsedEnvelope.Target.Runtime != domain.RuntimeTargetOpenClaw || parsedEnvelope.Soul.SpecHash != "sha256:draft" || parsedEnvelope.Soul.Draft != draftEvent.ID.Hex() {
		t.Fatalf("parsed runtime envelope = %+v", parsedEnvelope)
	}

	tagOnlyRuntime := &nostr.Event{
		Kind: nostr.Kind(domain.KindRuntimeControlRequest),
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
