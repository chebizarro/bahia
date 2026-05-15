package soulfactory

import (
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestHashDraftContentIncludesCustomizationSpecs(t *testing.T) {
	base := domain.SoulDraftContent{
		Schema: domain.SoulFactoryDraftSchemaV2,
		Brief:  "Monitor deployments",
		Identity: domain.SoulIdentitySpec{
			Name: "Scout",
			Tier: domain.SoulTierStandard,
		},
		Persona: domain.SoulPersonaSpec{Traits: []string{"curious"}},
		Avatar: domain.SoulAvatarSpec{Generation: &domain.SoulAvatarGenerationSpec{
			Prompt:      "Pixel art owl",
			StylePreset: "pixel-art",
			Provider:    "flux-comfyui",
		}},
		Voice: domain.SoulVoiceSpec{Provider: "elevenlabs", PersonaID: "scout-voice"},
		Memory: domain.SoulMemorySpec{
			EmbeddingProvider: "voyage",
			EmbeddingModel:    "voyage-3",
			Search:            &domain.SoulMemorySearchSpec{TopK: 10, ScoreThreshold: 0.7},
		},
	}

	first, err := hashDraftContent(base)
	if err != nil {
		t.Fatalf("hashDraftContent base error = %v", err)
	}
	second, err := hashDraftContent(base)
	if err != nil {
		t.Fatalf("hashDraftContent repeat error = %v", err)
	}
	if first != second || !strings.HasPrefix(first, "sha256:") {
		t.Fatalf("hash not stable or missing prefix: first=%q second=%q", first, second)
	}

	changed := base
	changed.Memory.Search.TopK = 20
	changedHash, err := hashDraftContent(changed)
	if err != nil {
		t.Fatalf("hashDraftContent changed error = %v", err)
	}
	if changedHash == first {
		t.Fatalf("hash did not change after customization spec update: %q", first)
	}
}

func TestBuildProvisionRuntimeParamsMigratesV1DraftFields(t *testing.T) {
	params := BuildProvisionRuntimeParamsFromDraft(domain.SoulDraftContent{
		Brief:        "Observe deploys",
		AvatarPrompt: "Pixel art owl",
		Assets:       domain.SoulAssetRefs{AvatarRef: "blossom:avatar", VoiceRef: "voice:legacy"},
	})

	if params["schema"] != domain.SoulFactoryDraftSchemaV2 {
		t.Fatalf("schema = %v", params["schema"])
	}
	avatar, ok := params["avatar"].(domain.SoulAvatarSpec)
	if !ok {
		t.Fatalf("avatar params type = %T", params["avatar"])
	}
	if avatar.Generation == nil || avatar.Generation.Prompt != "Pixel art owl" || avatar.UploadedRef != "blossom:avatar" || avatar.Current != "uploaded" {
		t.Fatalf("avatar params were not migrated: %+v", avatar)
	}
	voice, ok := params["voice"].(domain.SoulVoiceSpec)
	if !ok {
		t.Fatalf("voice params type = %T", params["voice"])
	}
	if voice.PersonaID != "voice:legacy" {
		t.Fatalf("voice persona id = %q", voice.PersonaID)
	}
}
