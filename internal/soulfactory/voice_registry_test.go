package soulfactory

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestDefaultVoiceProviderRegistryPopulatesBuiltInProviders(t *testing.T) {
	registry := NewDefaultVoiceProviderRegistry()

	ids := registry.ProviderIDs()
	wantIDs := []string{VoiceProviderAzureSpeech, VoiceProviderElevenLabs, VoiceProviderLocalCLI, VoiceProviderOpenAITTS}
	if len(ids) != len(wantIDs) {
		t.Fatalf("ProviderIDs len = %d, want %d: %v", len(ids), len(wantIDs), ids)
	}
	for i, want := range wantIDs {
		if ids[i] != want {
			t.Fatalf("ProviderIDs[%d] = %q, want %q (all: %v)", i, ids[i], want, ids)
		}
	}

	capabilities, err := registry.AllCapabilities(context.Background())
	if err != nil {
		t.Fatalf("AllCapabilities error = %v", err)
	}
	if len(capabilities) != len(wantIDs) {
		t.Fatalf("AllCapabilities len = %d, want %d", len(capabilities), len(wantIDs))
	}

	eleven, err := registry.Capabilities(context.Background(), "eleven-labs")
	if err != nil {
		t.Fatalf("Capabilities(eleven-labs) error = %v", err)
	}
	if eleven.Provider != VoiceProviderElevenLabs || eleven.DisplayName != "ElevenLabs" {
		t.Fatalf("ElevenLabs capabilities identity = %+v", eleven)
	}
	if !eleven.SupportsStreaming || !eleven.SupportsVoiceCloning || !eleven.SupportsVoiceSettings {
		t.Fatalf("ElevenLabs feature flags = streaming:%v cloning:%v settings:%v", eleven.SupportsStreaming, eleven.SupportsVoiceCloning, eleven.SupportsVoiceSettings)
	}
	if len(eleven.Voices) == 0 {
		t.Fatal("ElevenLabs voices are empty")
	}
	for _, voice := range eleven.Voices {
		if voice.Provider != VoiceProviderElevenLabs {
			t.Fatalf("voice.Provider = %q, want %q", voice.Provider, VoiceProviderElevenLabs)
		}
		if voice.ID == "" || voice.Name == "" {
			t.Fatalf("voice missing ID/name: %+v", voice)
		}
		if len(voice.Languages) == 0 {
			t.Fatalf("voice missing language metadata: %+v", voice)
		}
		if len(voice.StyleTags) == 0 {
			t.Fatalf("voice missing style tags: %+v", voice)
		}
	}

	azure, err := registry.Capabilities(context.Background(), "azure")
	if err != nil {
		t.Fatalf("Capabilities(azure alias) error = %v", err)
	}
	if azure.Provider != VoiceProviderAzureSpeech {
		t.Fatalf("Capabilities(azure alias).Provider = %q, want %q", azure.Provider, VoiceProviderAzureSpeech)
	}
}

func TestVoiceProviderRegistryCachesCapabilities(t *testing.T) {
	provider := &countingVoiceProvider{
		id:          "test-provider",
		displayName: "Test Provider",
		capabilities: VoiceProviderCapabilities{
			Provider:    "test-provider",
			DisplayName: "Test Provider",
			Metadata: map[string]any{
				"api_key_env": []string{"TEST_PROVIDER_API_KEY"},
				"nested":      map[string]any{"styles": []any{"calm"}},
			},
			Voices: []VoiceMetadata{{
				ID:        "voice-1",
				Name:      "Voice One",
				Languages: []string{"en"},
				ProviderSettings: map[string]any{
					"aliases": []string{"primary"},
				},
			}},
		},
	}
	registry := NewVoiceProviderRegistry(provider)
	registry.SetCacheTTL(time.Hour)

	first, err := registry.Capabilities(context.Background(), provider.id)
	if err != nil {
		t.Fatalf("first Capabilities error = %v", err)
	}
	first.Voices[0].Name = "mutated by caller"
	first.Metadata["api_key_env"].([]string)[0] = "MUTATED_KEY"
	first.Metadata["nested"].(map[string]any)["styles"].([]any)[0] = "mutated-style"
	first.Voices[0].ProviderSettings["aliases"].([]string)[0] = "mutated-alias"

	second, err := registry.Capabilities(context.Background(), provider.id)
	if err != nil {
		t.Fatalf("second Capabilities error = %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want 1 cached call", provider.calls)
	}
	if second.Voices[0].Name != "Voice One" {
		t.Fatalf("cached capabilities leaked caller mutation: %+v", second.Voices[0])
	}
	if second.Metadata["api_key_env"].([]string)[0] != "TEST_PROVIDER_API_KEY" {
		t.Fatalf("cached metadata leaked caller mutation: %+v", second.Metadata)
	}
	if second.Metadata["nested"].(map[string]any)["styles"].([]any)[0] != "calm" {
		t.Fatalf("cached nested metadata leaked caller mutation: %+v", second.Metadata)
	}
	if second.Voices[0].ProviderSettings["aliases"].([]string)[0] != "primary" {
		t.Fatalf("cached provider settings leaked caller mutation: %+v", second.Voices[0].ProviderSettings)
	}

	registry.SetCacheTTL(-1)
	if _, err := registry.Capabilities(context.Background(), provider.id); err != nil {
		t.Fatalf("uncached Capabilities error = %v", err)
	}
	if provider.calls != 2 {
		t.Fatalf("provider calls after disabling cache = %d, want 2", provider.calls)
	}
}

func TestVoiceProviderRegistryResolveUsesSingleCapabilitySnapshot(t *testing.T) {
	provider := &countingVoiceProvider{
		id:          "test-provider",
		displayName: "Test Provider",
		capabilities: VoiceProviderCapabilities{
			Provider:    "test-provider",
			DisplayName: "Test Provider",
			Voices: []VoiceMetadata{{
				ID:        "voice-1",
				Name:      "Voice One",
				Languages: []string{"en"},
			}},
		},
	}
	registry := NewVoiceProviderRegistry(provider)
	registry.SetCacheTTL(-1)

	resolution, err := registry.ResolveVoiceSpec(context.Background(), domain.SoulVoiceSpec{
		Provider: provider.id,
		Providers: map[string]map[string]any{
			provider.id: {"voiceId": "voice-1"},
		},
	})
	if err != nil {
		t.Fatalf("ResolveVoiceSpec error = %v", err)
	}
	if resolution.Voice == nil || resolution.Voice.ID != "voice-1" {
		t.Fatalf("resolution voice = %+v", resolution.Voice)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d, want one snapshot per ResolveVoiceSpec", provider.calls)
	}
}

func TestVoiceProviderRegistryResolveSoulVoiceSpec(t *testing.T) {
	registry := NewDefaultVoiceProviderRegistry()
	spec := domain.SoulVoiceSpec{
		Provider:  "elevenlabs",
		PersonaID: "scout-voice",
		Persona: &domain.SoulVoicePersonaSpec{
			Label:   "Scout Voice",
			Profile: "Young professional researcher",
			Style:   "articulate",
			Accent:  "neutral american",
			Pacing:  "measured",
		},
		Providers: map[string]map[string]any{
			"elevenlabs": {
				"voice_id":         "pNInz6obpgDQGcFmaJgB",
				"stability":        0.7,
				"similarity_boost": 0.8,
			},
		},
	}

	resolution, err := registry.ResolveVoiceSpec(context.Background(), spec)
	if err != nil {
		t.Fatalf("ResolveVoiceSpec error = %v", err)
	}
	if resolution.Provider.Provider != VoiceProviderElevenLabs {
		t.Fatalf("resolution provider = %q, want %q", resolution.Provider.Provider, VoiceProviderElevenLabs)
	}
	if resolution.Persona == nil || resolution.Persona.Label != "Scout Voice" {
		t.Fatalf("resolution persona = %+v", resolution.Persona)
	}
	if resolution.Voice == nil || resolution.Voice.ID != "pNInz6obpgDQGcFmaJgB" || resolution.Voice.Name != "Adam" {
		t.Fatalf("resolution voice = %+v", resolution.Voice)
	}
	if resolution.VoiceID != "pNInz6obpgDQGcFmaJgB" {
		t.Fatalf("resolution VoiceID = %q", resolution.VoiceID)
	}
	if resolution.ProviderConfig["stability"] != 0.7 {
		t.Fatalf("resolution provider config = %+v", resolution.ProviderConfig)
	}
}

func TestVoiceProviderRegistryResolveSpecRejectsUnknownExplicitVoice(t *testing.T) {
	registry := NewDefaultVoiceProviderRegistry()
	spec := domain.SoulVoiceSpec{
		Provider: VoiceProviderElevenLabs,
		Providers: map[string]map[string]any{
			VoiceProviderElevenLabs: {"voiceId": "missing-voice"},
		},
	}

	_, err := registry.ResolveVoiceSpec(context.Background(), spec)
	if !errors.Is(err, ErrVoiceNotFound) {
		t.Fatalf("ResolveVoiceSpec error = %v, want ErrVoiceNotFound", err)
	}
}

type countingVoiceProvider struct {
	id           string
	displayName  string
	capabilities VoiceProviderCapabilities
	calls        int
}

func (p *countingVoiceProvider) ID() string { return p.id }

func (p *countingVoiceProvider) DisplayName() string { return p.displayName }

func (p *countingVoiceProvider) Capabilities(context.Context) (VoiceProviderCapabilities, error) {
	p.calls++
	return cloneVoiceProviderCapabilities(p.capabilities), nil
}
