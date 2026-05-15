package soulfactory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	VoicePersonaRuntimeParamsSchema = "soulfactory-voice-persona/v1"
	DefaultVoicePersonaID           = "default"
	DefaultVoiceSampleText          = "Hello, I'm ready to help."
)

// VoicePersonaService maps Bahia SoulVoiceSpec values to OpenClaw TTS config
// shapes. It is pure and deterministic: sample generation is represented as
// runtime params, not performed through a request/response API here.
type VoicePersonaService struct {
	registry *VoiceProviderRegistry
}

func NewVoicePersonaService(registry *VoiceProviderRegistry) VoicePersonaService {
	if registry == nil {
		registry = NewDefaultVoiceProviderRegistry()
	}
	return VoicePersonaService{registry: registry}
}

type OpenClawTtsPromptConfig struct {
	Profile       string   `json:"profile,omitempty"`
	SampleContext string   `json:"sampleContext,omitempty"`
	Style         string   `json:"style,omitempty"`
	Accent        string   `json:"accent,omitempty"`
	Pacing        string   `json:"pacing,omitempty"`
	Constraints   []string `json:"constraints,omitempty"`
}

type OpenClawTtsPersonaConfig struct {
	Label          string                    `json:"label,omitempty"`
	Description    string                    `json:"description,omitempty"`
	Provider       string                    `json:"provider,omitempty"`
	FallbackPolicy string                    `json:"fallbackPolicy,omitempty"`
	Prompt         OpenClawTtsPromptConfig   `json:"prompt,omitempty"`
	Providers      map[string]map[string]any `json:"providers,omitempty"`
}

type OpenClawTtsConfig struct {
	Auto      string                              `json:"auto,omitempty"`
	Provider  string                              `json:"provider,omitempty"`
	Persona   string                              `json:"persona,omitempty"`
	Personas  map[string]OpenClawTtsPersonaConfig `json:"personas,omitempty"`
	Providers map[string]map[string]any           `json:"providers,omitempty"`
}

type VoicePersonaMapping struct {
	Schema     string               `json:"schema"`
	Provider   string               `json:"provider"`
	PersonaID  string               `json:"persona_id"`
	SampleText string               `json:"sample_text,omitempty"`
	VoiceID    string               `json:"voice_id,omitempty"`
	OpenClaw   OpenClawTtsConfig    `json:"openclaw"`
	Resolution *VoiceSpecResolution `json:"-"`
}

type VoiceSampleRuntimeParams struct {
	Schema     string            `json:"schema"`
	Provider   string            `json:"provider"`
	PersonaID  string            `json:"persona_id"`
	SampleText string            `json:"sample_text"`
	TTS        OpenClawTtsConfig `json:"tts"`
}

func (s VoicePersonaService) Map(ctx context.Context, spec domain.SoulVoiceSpec) (*VoicePersonaMapping, error) {
	resolution, err := s.registry.ResolveVoiceSpec(ctx, spec)
	if err != nil {
		return nil, err
	}
	providerID := resolution.Provider.Provider
	personaID := strings.TrimSpace(spec.PersonaID)
	if personaID == "" {
		personaID = DefaultVoicePersonaID
	}
	autoMode := normalizeVoiceAutoMode(spec.AutoMode)
	persona := buildOpenClawTtsPersona(providerID, resolution)
	providers := cloneProviders(spec.Providers)
	if len(providers) == 0 && len(resolution.ProviderConfig) > 0 {
		providers = map[string]map[string]any{providerID: cloneMap(resolution.ProviderConfig)}
	}
	openclaw := OpenClawTtsConfig{
		Auto:      autoMode,
		Provider:  providerID,
		Persona:   personaID,
		Personas:  map[string]OpenClawTtsPersonaConfig{personaID: persona},
		Providers: providers,
	}
	return &VoicePersonaMapping{
		Schema:     VoicePersonaRuntimeParamsSchema,
		Provider:   providerID,
		PersonaID:  personaID,
		SampleText: strings.TrimSpace(spec.SampleText),
		VoiceID:    resolution.VoiceID,
		OpenClaw:   openclaw,
		Resolution: resolution,
	}, nil
}

func (s VoicePersonaService) BuildConfigureRuntimeParams(ctx context.Context, spec domain.SoulVoiceSpec) (map[string]interface{}, error) {
	mapping, err := s.Map(ctx, spec)
	if err != nil {
		return nil, err
	}
	return voiceMappingToMap(mapping)
}

func (s VoicePersonaService) BuildSampleRuntimeParams(ctx context.Context, spec domain.SoulVoiceSpec) (map[string]interface{}, error) {
	mapping, err := s.Map(ctx, spec)
	if err != nil {
		return nil, err
	}
	sampleText := mapping.SampleText
	if sampleText == "" {
		sampleText = DefaultVoiceSampleText
	}
	params := VoiceSampleRuntimeParams{
		Schema:     VoicePersonaRuntimeParamsSchema,
		Provider:   mapping.Provider,
		PersonaID:  mapping.PersonaID,
		SampleText: sampleText,
		TTS:        mapping.OpenClaw,
	}
	return jsonStructToMap(params)
}

func MapSoulVoiceSpecToOpenClaw(ctx context.Context, spec domain.SoulVoiceSpec) (*VoicePersonaMapping, error) {
	return NewVoicePersonaService(nil).Map(ctx, spec)
}

func BuildVoiceConfigureRuntimeParams(ctx context.Context, spec domain.SoulVoiceSpec) (map[string]interface{}, error) {
	return NewVoicePersonaService(nil).BuildConfigureRuntimeParams(ctx, spec)
}

func BuildVoiceSampleRuntimeParams(ctx context.Context, spec domain.SoulVoiceSpec) (map[string]interface{}, error) {
	return NewVoicePersonaService(nil).BuildSampleRuntimeParams(ctx, spec)
}

func buildOpenClawTtsPersona(providerID string, resolution *VoiceSpecResolution) OpenClawTtsPersonaConfig {
	persona := resolution.Persona
	cfg := OpenClawTtsPersonaConfig{
		Provider:       providerID,
		FallbackPolicy: "preserve-persona",
		Providers:      cloneProviders(resolution.Spec.Providers),
	}
	if len(cfg.Providers) == 0 && len(resolution.ProviderConfig) > 0 {
		cfg.Providers = map[string]map[string]any{providerID: cloneMap(resolution.ProviderConfig)}
	}
	if persona != nil {
		cfg.Label = strings.TrimSpace(persona.Label)
		cfg.Description = strings.TrimSpace(persona.Profile)
		cfg.Prompt = OpenClawTtsPromptConfig{
			Profile: strings.TrimSpace(persona.Profile),
			Style:   strings.TrimSpace(persona.Style),
			Accent:  strings.TrimSpace(persona.Accent),
			Pacing:  voicePacingToOpenClaw(persona.Pacing),
		}
	}
	if cfg.Label == "" {
		cfg.Label = "Default Voice"
	}
	if resolution.Voice != nil {
		cfg.Description = strings.TrimSpace(strings.Join(nonEmptyStrings(cfg.Description, resolution.Voice.Description), " "))
		cfg.Prompt.Constraints = append(cfg.Prompt.Constraints, "Use provider voice "+resolution.Voice.Name+".")
	}
	return cfg
}

func normalizeVoiceAutoMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "always", "tagged", "inbound", "off":
		return strings.ToLower(strings.TrimSpace(mode))
	case "":
		return "off"
	default:
		return strings.ToLower(strings.TrimSpace(mode))
	}
}

func voicePacingToOpenClaw(pacing string) string {
	value := strings.ToLower(strings.TrimSpace(pacing))
	switch value {
	case "fast", "quick", "upbeat":
		return "fast"
	case "slow", "deliberate", "measured", "calm":
		return "slow"
	default:
		return strings.TrimSpace(pacing)
	}
}

func cloneProviders(values map[string]map[string]any) map[string]map[string]any {
	if values == nil {
		return nil
	}
	clone := make(map[string]map[string]any, len(values))
	for provider, config := range values {
		clone[NormalizeVoiceProviderID(provider)] = cloneMap(config)
	}
	return clone
}

func voiceMappingToMap(mapping *VoicePersonaMapping) (map[string]interface{}, error) {
	return jsonStructToMap(struct {
		Schema     string            `json:"schema"`
		Provider   string            `json:"provider"`
		PersonaID  string            `json:"persona_id"`
		SampleText string            `json:"sample_text,omitempty"`
		VoiceID    string            `json:"voice_id,omitempty"`
		OpenClaw   OpenClawTtsConfig `json:"openclaw"`
	}{
		Schema:     mapping.Schema,
		Provider:   mapping.Provider,
		PersonaID:  mapping.PersonaID,
		SampleText: mapping.SampleText,
		VoiceID:    mapping.VoiceID,
		OpenClaw:   mapping.OpenClaw,
	})
}

func jsonStructToMap(value any) (map[string]interface{}, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("marshal voice persona params: %w", err)
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("decode voice persona params: %w", err)
	}
	return out, nil
}

func nonEmptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			out = append(out, strings.TrimSpace(value))
		}
	}
	return out
}
