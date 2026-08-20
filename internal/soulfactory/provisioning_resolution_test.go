package soulfactory

import (
	"encoding/json"
	"strings"
	"testing"

	"fiatjaf.com/nostr"
	"github.com/google/uuid"
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

func TestProvisionRuntimeParamsCarrySanitizedSoulCheckpoint(t *testing.T) {
	resolved := resolvedProvisioningSpec{
		AgentID:  "scout",
		Name:     "Scout",
		Brief:    "Observe deploys",
		Tier:     domain.SoulTierStandard,
		Runtime:  domain.SoulRuntimeSpec{Target: domain.RuntimeTargetOpenClaw},
		SpecHash: "sha256:spec",
		SignetIdentity: &OpenClawSignetIdentityContract{
			Schema: OpenClawSignetIdentityContractSchema, AgentID: "scout",
			ManagedPubkey: strings.Repeat("a", 64), ClientPubkey: strings.Repeat("b", 64),
			BunkerURL:    "bunker://" + strings.Repeat("c", 64) + "?relay=wss%3A%2F%2Frelay.example",
			ClientKeyRef: "/run/openclaw/signet/scout.nip46-client",
		},
	}
	soul := &domain.AgentSoul{
		ID:             uuid.New(),
		AgentID:        "scout",
		Name:           "Scout",
		Status:         domain.SoulStatusProvisioning,
		NostrPubkey:    strings.Repeat("a", 64),
		NostrNpub:      "npub1scout",
		BunkerURI:      "bunker://must-not-leak?secret=one-time",
		SoulMD:         "# Scout",
		SpecHash:       "sha256:spec",
		AllowedKinds:   []int{1, domain.KindAgentSoul},
		PermissionSpec: domain.SoulPermissionSpec{AllowedKinds: []int{1, domain.KindAgentSoul}},
	}

	params := resolved.provisionRuntimeParams(soul)
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("marshal params: %v", err)
	}
	if strings.Contains(string(encoded), "must-not-leak") || strings.Contains(string(encoded), "one-time") {
		t.Fatalf("runtime params leaked bunker URI: %s", encoded)
	}
	bahia := params["bahia"].(map[string]interface{})
	checkpoint := bahia["soul_checkpoint"].(domain.AgentSoul)
	if checkpoint.AgentID != soul.AgentID || checkpoint.SoulMD != soul.SoulMD || checkpoint.SpecHash != soul.SpecHash {
		t.Fatalf("checkpoint = %+v, want durable soul state", checkpoint)
	}
	if checkpoint.BunkerURI != "" {
		t.Fatalf("checkpoint bunker URI = %q, want empty", checkpoint.BunkerURI)
	}
	contract, ok := bahia["signet_identity"].(*OpenClawSignetIdentityContract)
	if !ok || contract.ClientKeyRef != "/run/openclaw/signet/scout.nip46-client" || contract.ManagedPubkey != soul.NostrPubkey {
		t.Fatalf("runtime Signet identity contract = %#v", bahia["signet_identity"])
	}
}

func TestSoulFromRuntimeCheckpointProjectsLateSuccess(t *testing.T) {
	runtimeKey := nostr.Generate()
	runtimePubkey := runtimeKey.Public().Hex()
	soulID := uuid.New()
	checkpoint := domain.AgentSoul{
		ID:          soulID,
		AgentID:     "scout",
		Name:        "Scout",
		Status:      domain.SoulStatusProvisioning,
		NostrPubkey: strings.Repeat("a", 64),
		SoulMD:      "# Scout",
		SpecHash:    "sha256:spec",
		BunkerURI:   "bunker://must-be-cleared",
	}
	control := &RuntimeControlEnvelope{
		Schema: domain.SoulFactoryRuntimeControlSchema,
		Method: RuntimeMethodProvision,
		Soul:   RuntimeSoulRef{ID: "scout", SpecHash: "sha256:spec"},
		Target: RuntimeTargetRef{
			Runtime:       domain.RuntimeTargetOpenClaw,
			RuntimePubkey: runtimePubkey,
			AgentID:       "scout",
		},
		Params: map[string]interface{}{
			"bahia": map[string]interface{}{"soul_checkpoint": checkpoint},
		},
	}
	resultEvent := &nostr.Event{Kind: nostr.Kind(domain.KindRuntimeControlResult), PubKey: runtimeKey.Public()}
	if err := resultEvent.Sign(runtimeKey); err != nil {
		t.Fatalf("sign result: %v", err)
	}
	result := &RuntimeControlResultEnvelope{
		Status: "success",
		Result: map[string]interface{}{
			"runtime_binding": "openclaw://agents/scout",
			"state":           "running",
		},
		Event: resultEvent,
	}

	soul, err := soulFromRuntimeCheckpoint(control, result)
	if err != nil {
		t.Fatalf("soulFromRuntimeCheckpoint: %v", err)
	}
	if soul.Status != domain.SoulStatusActive || soul.Runtime.State != "running" ||
		soul.Runtime.RuntimeBinding != "openclaw://agents/scout" || soul.Runtime.RuntimePubkey != runtimePubkey {
		t.Fatalf("recovered soul = %+v", soul)
	}
	if soul.BunkerURI != "" || soul.LastResultRef != resultEvent.ID.Hex() || soul.ProvisionedAt == nil {
		t.Fatalf("recovered soul secret/result fields = %+v", soul)
	}
}

func TestLateRuntimeReconciliationOnlyAcceptsRuntimeStageErrors(t *testing.T) {
	runtimeError := &nostr.Event{
		Kind:    nostr.Kind(domain.KindProvisioningResult),
		Tags:    nostr.Tags{{tagStatus, "error"}, {tagStep, string(domain.StepDeploy)}},
		Content: "runtime provision openclaw: context deadline exceeded",
	}
	if !provisioningResultNeedsRuntimeReconciliation(runtimeError) {
		t.Fatal("runtime-stage error was not marked for reconciliation")
	}
	for _, event := range []*nostr.Event{
		{Kind: nostr.Kind(domain.KindProvisioningResult), Tags: nostr.Tags{{tagStatus, "success"}, {tagStep, string(domain.StepDeploy)}}, Content: "runtime provision openclaw"},
		{Kind: nostr.Kind(domain.KindProvisioningResult), Tags: nostr.Tags{{tagStatus, "error"}, {tagStep, string(domain.StepSignet)}}, Content: "runtime provision openclaw"},
		{Kind: nostr.Kind(domain.KindProvisioningResult), Tags: nostr.Tags{{tagStatus, "error"}, {tagStep, string(domain.StepDeploy)}}, Content: "NIP-05 registration failed"},
	} {
		if provisioningResultNeedsRuntimeReconciliation(event) {
			t.Fatalf("non-runtime terminal result was marked for reconciliation: %+v", event)
		}
	}
}
