package soulfactory

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"fiatjaf.com/nostr"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestCustomizationDomainSpecsParseAndSerialize(t *testing.T) {
	content := completeCustomizationDraftContent()

	data, err := content.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON() error = %v", err)
	}
	if !strings.Contains(data, `"schema":"`+domain.SoulFactoryDraftSchemaV2+`"`) {
		t.Fatalf("ToJSON() did not serialize v2 schema: %s", data)
	}

	parsed, err := domain.ParseDraftContent(data)
	if err != nil {
		t.Fatalf("ParseDraftContent() error = %v", err)
	}
	if parsed.Schema != domain.SoulFactoryDraftSchemaV2 {
		t.Fatalf("schema = %q, want %q", parsed.Schema, domain.SoulFactoryDraftSchemaV2)
	}
	if !reflect.DeepEqual(parsed.Identity, content.Identity) {
		t.Fatalf("identity mismatch: %#v", parsed.Identity)
	}
	if !reflect.DeepEqual(parsed.Persona, content.Persona) {
		t.Fatalf("persona mismatch: %#v", parsed.Persona)
	}
	if !reflect.DeepEqual(parsed.Avatar, content.Avatar) {
		t.Fatalf("avatar mismatch: %#v", parsed.Avatar)
	}
	if !reflect.DeepEqual(parsed.Voice, content.Voice) {
		t.Fatalf("voice mismatch: %#v", parsed.Voice)
	}
	if !reflect.DeepEqual(parsed.Memory, content.Memory) {
		t.Fatalf("memory mismatch: %#v", parsed.Memory)
	}
	if !reflect.DeepEqual(parsed.Runtime, content.Runtime) {
		t.Fatalf("runtime mismatch: %#v", parsed.Runtime)
	}
	if !reflect.DeepEqual(parsed.Permissions, content.Permissions) {
		t.Fatalf("permissions mismatch: %#v", parsed.Permissions)
	}
	if !reflect.DeepEqual(parsed.RelayPolicy, content.RelayPolicy) {
		t.Fatalf("relay policy mismatch: %#v", parsed.RelayPolicy)
	}
	if !reflect.DeepEqual(parsed.Workspace, content.Workspace) {
		t.Fatalf("workspace mismatch: %#v", parsed.Workspace)
	}
	if !reflect.DeepEqual(parsed.Assets, content.Assets) {
		t.Fatalf("assets mismatch: %#v", parsed.Assets)
	}
}

func TestCustomizationDraftV2EventCodecRoundTrip(t *testing.T) {
	draft := &domain.SoulDraft{
		AgentID:     "scout",
		Name:        "Scout",
		Tier:        domain.SoulTierHeavy,
		TemplateRef: "template:scout",
		Content:     completeCustomizationDraftContent(),
	}

	event, err := BuildSoulDraftEvent(draft)
	if err != nil {
		t.Fatalf("BuildSoulDraftEvent() error = %v", err)
	}
	event.ID = soulTestID("draft-event-id")
	event.PubKey = soulTestPubKey("draft-author")

	parsed, err := ParseSoulDraftEvent(event)
	if err != nil {
		t.Fatalf("ParseSoulDraftEvent() error = %v", err)
	}
	if parsed.AgentID != draft.AgentID || parsed.Name != draft.Name || parsed.Tier != draft.Tier || parsed.TemplateRef != draft.TemplateRef {
		t.Fatalf("parsed draft metadata = %#v", parsed)
	}
	if parsed.Content.Schema != domain.SoulFactoryDraftSchemaV2 {
		t.Fatalf("parsed schema = %q", parsed.Content.Schema)
	}
	if !reflect.DeepEqual(parsed.Content.Avatar, draft.Content.Avatar) {
		t.Fatalf("avatar round trip mismatch: %#v", parsed.Content.Avatar)
	}
	if !reflect.DeepEqual(parsed.Content.Voice, draft.Content.Voice) {
		t.Fatalf("voice round trip mismatch: %#v", parsed.Content.Voice)
	}
	if !reflect.DeepEqual(parsed.Content.Memory, draft.Content.Memory) {
		t.Fatalf("memory round trip mismatch: %#v", parsed.Content.Memory)
	}
}

func TestCustomizationDraftValidationRequiredValuesAndConstraints(t *testing.T) {
	base := completeCustomizationDraftContent()
	tests := []struct {
		name string
		edit func(*domain.SoulDraftContent)
		want string
	}{
		{"v1_with_v2_specs", func(c *domain.SoulDraftContent) { c.Schema = domain.SoulFactoryDraftSchemaV1 }, "v2 customization specs require schema"},
		{"unsupported_schema", func(c *domain.SoulDraftContent) { c.Schema = "soulfactory-draft/v9" }, "schema"},
		{"avatar_prompt_required", func(c *domain.SoulDraftContent) { c.Avatar.Generation.Prompt = " " }, "avatar.generation.prompt is required"},
		{"avatar_provider", func(c *domain.SoulDraftContent) { c.Avatar.Generation.Provider = "unknown" }, "avatar.generation.provider"},
		{"avatar_dimensions", func(c *domain.SoulDraftContent) { c.Avatar.Generation.Width = 65 }, "avatar.generation.width must be a multiple"},
		{"voice_auto_mode", func(c *domain.SoulDraftContent) { c.Voice.AutoMode = "sometimes" }, "voice.auto_mode"},
		{"voice_provider_model", func(c *domain.SoulDraftContent) { c.Voice.Providers[VoiceProviderOpenAITTS]["model"] = "missing-model" }, "voice.providers.openai-tts.model"},
		{"memory_provider", func(c *domain.SoulDraftContent) { c.Memory.EmbeddingProvider = "missing" }, "embedding_provider"},
		{"memory_model_requires_provider", func(c *domain.SoulDraftContent) { c.Memory.EmbeddingProvider = "auto" }, "memory.embedding_model requires"},
		{"memory_rerank_model", func(c *domain.SoulDraftContent) { c.Memory.Search.RerankModel = "missing-reranker" }, "memory.search.rerank_model"},
		{"persona_unknown_section", func(c *domain.SoulDraftContent) { c.Persona.SystemPromptSections["unknown"] = "nope" }, "unsupported system prompt section"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content := base
			content.Avatar.Generation = &domain.SoulAvatarGenerationSpec{Prompt: base.Avatar.Generation.Prompt, StylePreset: base.Avatar.Generation.StylePreset, Seed: base.Avatar.Generation.Seed, Width: base.Avatar.Generation.Width, Height: base.Avatar.Generation.Height, Provider: base.Avatar.Generation.Provider}
			content.Voice.Providers = map[string]map[string]any{VoiceProviderOpenAITTS: {"model": "gpt-4o-mini-tts", "voice": "alloy"}}
			content.Memory.Search = &domain.SoulMemorySearchSpec{TopK: base.Memory.Search.TopK, ScoreThreshold: base.Memory.Search.ScoreThreshold, Rerank: base.Memory.Search.Rerank, RerankModel: base.Memory.Search.RerankModel}
			content.Persona.SystemPromptSections = map[string]string{"role": "You are Scout."}
			tt.edit(&content)

			err := ValidateSoulDraftContent(content)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ValidateSoulDraftContent() error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestCustomizationProviderDispatch(t *testing.T) {
	ctx := context.Background()

	voiceMapping, err := NewVoicePersonaService(nil).Map(ctx, domain.SoulVoiceSpec{
		Provider:  "azure",
		PersonaID: "en-US-AvaMultilingualNeural",
		Persona:   &domain.SoulVoicePersonaSpec{Label: "Ava", Profile: "clear narrator", Pacing: "measured"},
	})
	if err != nil {
		t.Fatalf("voice Map() error = %v", err)
	}
	if voiceMapping.Provider != VoiceProviderAzureSpeech || voiceMapping.VoiceID != "en-US-AvaMultilingualNeural" {
		t.Fatalf("voice provider dispatch = provider %q voice %q", voiceMapping.Provider, voiceMapping.VoiceID)
	}
	if voiceMapping.OpenClaw.Provider != VoiceProviderAzureSpeech || voiceMapping.OpenClaw.Persona != "en-US-AvaMultilingualNeural" {
		t.Fatalf("OpenClaw voice dispatch = %#v", voiceMapping.OpenClaw)
	}

	memory, err := MapSoulMemorySpec(domain.SoulMemorySpec{EmbeddingProvider: "voyage-ai", EmbeddingModel: "voyage-3", Strategy: "long_term", AutoIndex: true})
	if err != nil {
		t.Fatalf("MapSoulMemorySpec() error = %v", err)
	}
	if memory.Provider != MemoryEmbeddingProviderVoyage || memory.Strategy != MemoryStrategyLongTerm {
		t.Fatalf("memory dispatch = provider %q strategy %q", memory.Provider, memory.Strategy)
	}

	avatarSpec, err := avatarGenerationSpecFromParams(map[string]interface{}{"generation": map[string]interface{}{"prompt": "portrait", "provider": "fal", "style_preset": "anime", "width": 512, "height": 512}})
	if err != nil {
		t.Fatalf("avatarGenerationSpecFromParams() error = %v", err)
	}
	if avatarSpec.Provider != "fal" || avatarSpec.StylePreset != "anime" || avatarSpec.Width != 512 {
		t.Fatalf("avatar dispatch spec = %#v", avatarSpec)
	}
}

func TestCustomizationOpenClawSidecarRuntimeMethodValidation(t *testing.T) {
	_, err := NewOpenClawSidecar(OpenClawSidecarConfig{
		RuntimePubkey:            strings.Repeat("b", 64),
		Signer:                   newFakeSigner(t),
		TrustedControllerPubkeys: []string{strings.Repeat("a", 64)},
		Transport:                noopRuntimeTransport{},
		Driver:                   customizationControlDriver{},
	})
	if err == nil || !strings.Contains(err.Error(), "must support at least one") {
		t.Fatalf("NewOpenClawSidecar(empty methods) error = %v", err)
	}

	sidecar, err := NewOpenClawSidecar(OpenClawSidecarConfig{
		RuntimePubkey:            strings.Repeat("b", 64),
		Signer:                   newFakeSigner(t),
		TrustedControllerPubkeys: []string{strings.Repeat("a", 64)},
		Transport:                noopRuntimeTransport{},
		Driver: customizationControlDriver{methods: []string{
			RuntimeMethodAvatarGenerate,
			RuntimeMethodVoiceConfigure,
			RuntimeMethodMemoryConfigure,
			RuntimeMethodPersonaConfigure,
		}},
	})
	if err != nil {
		t.Fatalf("NewOpenClawSidecar(customization methods) error = %v", err)
	}
	defer sidecar.Close()
	for _, method := range []string{RuntimeMethodAvatarGenerate, RuntimeMethodVoiceConfigure, RuntimeMethodMemoryConfigure, RuntimeMethodPersonaConfigure} {
		if !stringSliceContains(sidecar.methods, method) {
			t.Fatalf("sidecar methods %v missing %s", sidecar.methods, method)
		}
	}
}

func completeCustomizationDraftContent() domain.SoulDraftContent {
	return domain.SoulDraftContent{
		Schema: domain.SoulFactoryDraftSchemaV2,
		Brief:  "A customizable scout agent.",
		Identity: domain.SoulIdentitySpec{
			Name: "Scout", Purpose: "Explore and summarize", Tier: domain.SoulTierHeavy, NIP05: "scout@example.com", Theme: "forest", Emoji: "🧭",
		},
		Persona: domain.SoulPersonaSpec{
			Traits: []string{"curious", "precise"}, Style: "concise", Tone: "friendly", Constraints: []string{"cite uncertainty"}, SystemPromptSections: map[string]string{"role": "You are Scout."},
		},
		Avatar: domain.SoulAvatarSpec{
			Generation: &domain.SoulAvatarGenerationSpec{Prompt: "friendly scout portrait", StylePreset: "anime", Seed: "seed-1", Width: 512, Height: 512, Provider: "fal"},
			Current:    "generated",
		},
		Voice: domain.SoulVoiceSpec{
			Provider: "openai-tts", PersonaID: "alloy", Persona: &domain.SoulVoicePersonaSpec{Label: "Scout Voice", Profile: "bright narrator", Style: "warm", Accent: "neutral", Pacing: "quick"}, AutoMode: "tagged", SampleText: "Ready to explore.", Providers: map[string]map[string]any{VoiceProviderOpenAITTS: {"model": "gpt-4o-mini-tts", "voice": "alloy"}},
		},
		Memory: domain.SoulMemorySpec{
			EmbeddingProvider: "openai", EmbeddingModel: "text-embedding-3-small", Strategy: "session-aware", AutoIndex: true, RetentionDays: 30, Search: &domain.SoulMemorySearchSpec{TopK: 8, ScoreThreshold: 0.7, Rerank: true, RerankModel: "rerank-v3.5"},
		},
		Runtime:     domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw, RuntimePubkey: strings.Repeat("c", 64), CapabilityRef: "capability", RuntimeBinding: "binding", State: "bound"},
		Permissions: domain.SoulPermissionSpec{AllowedKinds: []int{1, domain.KindSoulAction}, ToolGrants: []domain.ToolGrant{{MCPServer: "filesystem", Scopes: []string{"read"}}}, ApprovalPolicy: "trusted"},
		RelayPolicy: domain.SoulRelayPolicySpec{Read: []string{"wss://read.example"}, Write: []string{"wss://write.example"}, Control: []string{"wss://control.example"}, NIP65Discovery: true},
		Workspace:   domain.SoulWorkspaceSpec{Repo: "https://git.example/scout.git", Branch: "main", Environment: "prod"},
		Assets:      domain.SoulAssetRefs{AvatarRef: "blossom://avatar", VoiceRef: "voice://alloy"},
		SpecHash:    "sha256:spec",
	}
}

type customizationControlDriver struct {
	methods []string
}

func (d customizationControlDriver) Methods() []string { return d.methods }
func (d customizationControlDriver) Execute(context.Context, OpenClawControlInvocation) (*OpenClawControlOutcome, error) {
	return &OpenClawControlOutcome{Status: "completed"}, nil
}

type noopRuntimeTransport struct{}

func (noopRuntimeTransport) Publish(context.Context, nostr.Event) (int, error) { return 1, nil }
func (noopRuntimeTransport) SubscribeAllWithEOSE(context.Context, []nostr.Filter) (*RelayBusSubscription, error) {
	return nil, nil
}
func (noopRuntimeTransport) Close() {}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
