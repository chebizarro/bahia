package soulfactory

import (
	"context"
	"errors"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

func TestVoicePersonaServiceMapsSoulVoiceSpecToOpenClawTTSConfig(t *testing.T) {
	service := NewVoicePersonaService(NewDefaultVoiceProviderRegistry())
	spec := domain.SoulVoiceSpec{
		Provider:   "eleven-labs",
		PersonaID:  "scout-voice",
		AutoMode:   "tagged",
		SampleText: "Hello, I'm Scout.",
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
				"model":            "eleven_turbo_v2_5",
				"stability":        0.7,
				"similarity_boost": 0.8,
				"style":            0.5,
			},
		},
	}

	mapping, err := service.Map(context.Background(), spec)
	if err != nil {
		t.Fatalf("Map error = %v", err)
	}
	if mapping.Provider != VoiceProviderElevenLabs {
		t.Fatalf("Provider = %q, want %q", mapping.Provider, VoiceProviderElevenLabs)
	}
	if mapping.PersonaID != "scout-voice" || mapping.VoiceID != "pNInz6obpgDQGcFmaJgB" {
		t.Fatalf("persona/voice ids = %q/%q", mapping.PersonaID, mapping.VoiceID)
	}
	if mapping.OpenClaw.Auto != "tagged" || mapping.OpenClaw.Provider != VoiceProviderElevenLabs || mapping.OpenClaw.Persona != "scout-voice" {
		t.Fatalf("OpenClaw TTS identity = %+v", mapping.OpenClaw)
	}
	persona := mapping.OpenClaw.Personas["scout-voice"]
	if persona.Label != "Scout Voice" || persona.Provider != VoiceProviderElevenLabs || persona.FallbackPolicy != "preserve-persona" {
		t.Fatalf("persona config = %+v", persona)
	}
	if persona.Prompt.Profile != "Young professional researcher" || persona.Prompt.Style != "articulate" || persona.Prompt.Accent != "neutral american" {
		t.Fatalf("persona prompt = %+v", persona.Prompt)
	}
	if persona.Prompt.Pacing != "slow" {
		t.Fatalf("persona prompt pacing = %q, want slow", persona.Prompt.Pacing)
	}
	if persona.Providers[VoiceProviderElevenLabs]["voice_id"] != "pNInz6obpgDQGcFmaJgB" {
		t.Fatalf("persona provider binding = %+v", persona.Providers)
	}
	if mapping.OpenClaw.Providers[VoiceProviderElevenLabs]["stability"] != 0.7 {
		t.Fatalf("top-level provider binding = %+v", mapping.OpenClaw.Providers)
	}
}

func TestVoicePersonaServiceSelectsSupportedProviders(t *testing.T) {
	service := NewVoicePersonaService(NewDefaultVoiceProviderRegistry())
	cases := []struct {
		name     string
		spec     domain.SoulVoiceSpec
		provider string
		voiceID  string
	}{
		{
			name:     "azure",
			spec:     domain.SoulVoiceSpec{Provider: "azure", PersonaID: "narrator", Providers: map[string]map[string]any{"azure": {"voiceId": "en-US-JennyNeural"}}},
			provider: VoiceProviderAzureSpeech,
			voiceID:  "en-US-JennyNeural",
		},
		{
			name:     "openai",
			spec:     domain.SoulVoiceSpec{Provider: "openai", PersonaID: "nova", Providers: map[string]map[string]any{"openai": {"voice": "nova"}}},
			provider: VoiceProviderOpenAITTS,
			voiceID:  "nova",
		},
		{
			name:     "local",
			spec:     domain.SoulVoiceSpec{Provider: "local", Providers: map[string]map[string]any{"local": {"voice": "default", "command": "say"}}},
			provider: VoiceProviderLocalCLI,
			voiceID:  "default",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mapping, err := service.Map(context.Background(), tc.spec)
			if err != nil {
				t.Fatalf("Map error = %v", err)
			}
			if mapping.Provider != tc.provider || mapping.VoiceID != tc.voiceID {
				t.Fatalf("mapping provider/voice = %q/%q, want %q/%q", mapping.Provider, mapping.VoiceID, tc.provider, tc.voiceID)
			}
			if mapping.OpenClaw.Provider != tc.provider {
				t.Fatalf("OpenClaw provider = %q, want %q", mapping.OpenClaw.Provider, tc.provider)
			}
		})
	}
}

func TestVoicePersonaServiceBuildsSampleRuntimeParams(t *testing.T) {
	service := NewVoicePersonaService(NewDefaultVoiceProviderRegistry())
	params, err := service.BuildSampleRuntimeParams(context.Background(), domain.SoulVoiceSpec{
		Provider:   VoiceProviderOpenAITTS,
		PersonaID:  "preview",
		SampleText: "Testing preview voice.",
		Providers: map[string]map[string]any{
			VoiceProviderOpenAITTS: {"voice": "alloy"},
		},
	})
	if err != nil {
		t.Fatalf("BuildSampleRuntimeParams error = %v", err)
	}
	if params["schema"] != VoicePersonaRuntimeParamsSchema || params["provider"] != VoiceProviderOpenAITTS || params["persona_id"] != "preview" {
		t.Fatalf("sample params identity = %+v", params)
	}
	if params["sample_text"] != "Testing preview voice." {
		t.Fatalf("sample_text = %q", params["sample_text"])
	}
	tts := params["tts"].(map[string]interface{})
	if tts["provider"] != VoiceProviderOpenAITTS || tts["persona"] != "preview" {
		t.Fatalf("sample tts = %+v", tts)
	}
}

func TestVoicePersonaServiceDefaultsSampleTextAndPersonaID(t *testing.T) {
	service := NewVoicePersonaService(NewDefaultVoiceProviderRegistry())
	params, err := service.BuildSampleRuntimeParams(context.Background(), domain.SoulVoiceSpec{
		Provider:  VoiceProviderLocalCLI,
		Providers: map[string]map[string]any{VoiceProviderLocalCLI: {"voice": "default"}},
	})
	if err != nil {
		t.Fatalf("BuildSampleRuntimeParams error = %v", err)
	}
	if params["persona_id"] != DefaultVoicePersonaID {
		t.Fatalf("persona_id = %q, want %q", params["persona_id"], DefaultVoicePersonaID)
	}
	if params["sample_text"] != DefaultVoiceSampleText {
		t.Fatalf("sample_text = %q, want %q", params["sample_text"], DefaultVoiceSampleText)
	}
}

func TestVoicePersonaServiceRejectsUnknownExplicitVoice(t *testing.T) {
	service := NewVoicePersonaService(NewDefaultVoiceProviderRegistry())
	_, err := service.Map(context.Background(), domain.SoulVoiceSpec{
		Provider: VoiceProviderOpenAITTS,
		Providers: map[string]map[string]any{
			VoiceProviderOpenAITTS: {"voice": "missing"},
		},
	})
	if !errors.Is(err, ErrVoiceNotFound) {
		t.Fatalf("Map error = %v, want ErrVoiceNotFound", err)
	}
}
