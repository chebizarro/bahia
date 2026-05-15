package domain

import "testing"

func TestSoulDraftContentV2CustomizationSpecsRoundTrip(t *testing.T) {
	content := SoulDraftContent{
		Schema: SoulFactoryDraftSchemaV2,
		Brief:  "Monitor deployments",
		Identity: SoulIdentitySpec{
			Name:    "Scout",
			Purpose: "Watch deploys",
			Tier:    SoulTierStandard,
			Theme:   "warm",
			Emoji:   "🔍",
		},
		Persona: SoulPersonaSpec{
			Traits:      []string{"curious", "thorough"},
			Style:       "conversational",
			Tone:        "friendly professional",
			Constraints: []string{"Always cite sources"},
			SystemPromptSections: map[string]string{
				"role":       "You are Scout.",
				"guidelines": "Be precise.",
			},
		},
		Avatar: SoulAvatarSpec{
			Generation: &SoulAvatarGenerationSpec{
				Prompt:      "Pixel art owl with magnifying glass",
				StylePreset: "pixel-art",
				Seed:        "scout-v1",
				Width:       512,
				Height:      512,
				Provider:    "flux-comfyui",
			},
			UploadedRef:  "blossom:uploaded",
			GeneratedRef: "blossom:generated",
			Current:      "generated",
		},
		Voice: SoulVoiceSpec{
			Provider:   "elevenlabs",
			PersonaID:  "scout-voice",
			Persona:    &SoulVoicePersonaSpec{Label: "Scout Voice", Profile: "Researcher", Style: "articulate", Accent: "neutral american", Pacing: "measured"},
			AutoMode:   "tagged",
			SampleText: "Hello, I'm Scout.",
			Providers: map[string]map[string]any{
				"elevenlabs": {"voice_id": "voice-123", "stability": 0.7},
			},
		},
		Memory: SoulMemorySpec{
			EmbeddingProvider: "voyage",
			EmbeddingModel:    "voyage-3",
			Search:            &SoulMemorySearchSpec{TopK: 10, ScoreThreshold: 0.7, Rerank: true, RerankModel: "cohere-rerank-v3"},
			Strategy:          "session-aware",
			AutoIndex:         true,
			RetentionDays:     90,
		},
	}

	jsonContent, err := content.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error = %v", err)
	}
	parsed, err := ParseDraftContent(jsonContent)
	if err != nil {
		t.Fatalf("ParseDraftContent error = %v", err)
	}

	if !parsed.IsV2() || parsed.SchemaVersion() != SoulFactoryDraftSchemaV2 {
		t.Fatalf("schema version = %q, want v2", parsed.SchemaVersion())
	}
	if parsed.Identity.Theme != "warm" || parsed.Identity.Emoji != "🔍" {
		t.Fatalf("identity theme/emoji = %q/%q", parsed.Identity.Theme, parsed.Identity.Emoji)
	}
	if got := parsed.Persona.SystemPromptSections["role"]; got != "You are Scout." {
		t.Fatalf("persona role = %q", got)
	}
	if parsed.Avatar.Generation == nil || parsed.Avatar.Generation.Provider != "flux-comfyui" || parsed.Avatar.Current != "generated" {
		t.Fatalf("avatar spec = %+v", parsed.Avatar)
	}
	if parsed.Voice.Persona == nil || parsed.Voice.Persona.Pacing != "measured" || parsed.Voice.Providers["elevenlabs"]["voice_id"] != "voice-123" {
		t.Fatalf("voice spec = %+v", parsed.Voice)
	}
	if parsed.Memory.Search == nil || parsed.Memory.Search.TopK != 10 || !parsed.Memory.Search.Rerank {
		t.Fatalf("memory spec = %+v", parsed.Memory)
	}
}

func TestSoulDraftContentAutoMarksV2WhenCustomizationSpecsPresent(t *testing.T) {
	content := SoulDraftContent{
		Brief:   "Observe deploys",
		Persona: SoulPersonaSpec{Traits: []string{"curious"}},
	}
	jsonContent, err := content.ToJSON()
	if err != nil {
		t.Fatalf("ToJSON error = %v", err)
	}
	parsed, err := ParseDraftContent(jsonContent)
	if err != nil {
		t.Fatalf("ParseDraftContent error = %v", err)
	}
	if parsed.Schema != SoulFactoryDraftSchemaV2 || !parsed.IsV2() {
		t.Fatalf("auto schema = %q", parsed.Schema)
	}
	if content.Schema != "" {
		t.Fatalf("ToJSON mutated original schema = %q", content.Schema)
	}
}

func TestSoulDraftContentV1MigrationKeepsLegacyFields(t *testing.T) {
	legacy := `{"brief":"Observe deploys","allowed_kinds":[1,30023],"avatar_prompt":"Pixel art owl","assets":{"avatar_ref":"blossom:avatar","voice_ref":"voice:legacy"}}`
	parsed, err := ParseDraftContent(legacy)
	if err != nil {
		t.Fatalf("ParseDraftContent legacy error = %v", err)
	}
	if parsed.Schema != "" || parsed.SchemaVersion() != SoulFactoryDraftSchemaV1 || parsed.IsV2() {
		t.Fatalf("legacy schema = %q version=%q", parsed.Schema, parsed.SchemaVersion())
	}
	if parsed.AvatarPrompt != "Pixel art owl" || parsed.Assets.AvatarRef != "blossom:avatar" || parsed.Assets.VoiceRef != "voice:legacy" {
		t.Fatalf("legacy fields not preserved: %+v", parsed)
	}

	migrated := parsed.MigrateToLatest()
	if migrated.Schema != SoulFactoryDraftSchemaV2 || !migrated.IsV2() {
		t.Fatalf("migrated schema = %q", migrated.Schema)
	}
	if migrated.AvatarPrompt != parsed.AvatarPrompt || len(migrated.AllowedKinds) != 2 {
		t.Fatalf("migration dropped legacy fields: %+v", migrated)
	}
	if migrated.Avatar.Generation == nil || migrated.Avatar.Generation.Prompt != "Pixel art owl" {
		t.Fatalf("avatar generation migration = %+v", migrated.Avatar.Generation)
	}
	if migrated.Avatar.UploadedRef != "blossom:avatar" || migrated.Avatar.Current != "uploaded" {
		t.Fatalf("avatar ref migration = %+v", migrated.Avatar)
	}
	if migrated.Voice.PersonaID != "voice:legacy" {
		t.Fatalf("voice ref migration persona_id = %q", migrated.Voice.PersonaID)
	}
}
