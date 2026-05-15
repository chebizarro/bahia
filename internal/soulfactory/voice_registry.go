package soulfactory

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	// Built-in voice provider IDs used by SoulFactory draft voice specs.
	VoiceProviderElevenLabs  = "elevenlabs"
	VoiceProviderAzureSpeech = "azure-speech"
	VoiceProviderOpenAITTS   = "openai-tts"
	VoiceProviderLocalCLI    = "local-cli"

	// DefaultVoiceCapabilitiesCacheTTL is intentionally short-lived enough for
	// provider catalog refreshes while avoiding repeated capability calls on hot
	// paths such as draft validation and UI capability queries.
	DefaultVoiceCapabilitiesCacheTTL = 10 * time.Minute
)

var (
	ErrVoiceProviderNotFound = errors.New("voice provider not found")
	ErrVoiceNotFound         = errors.New("voice not found")
)

// VoiceGender captures coarse voice presentation metadata for UI filtering.
type VoiceGender string

const (
	VoiceGenderUnspecified VoiceGender = "unspecified"
	VoiceGenderFeminine    VoiceGender = "feminine"
	VoiceGenderMasculine   VoiceGender = "masculine"
	VoiceGenderNeutral     VoiceGender = "neutral"
)

// VoiceMetadata describes a selectable TTS voice exposed by a provider.
type VoiceMetadata struct {
	ID               string         `json:"id"`
	Name             string         `json:"name"`
	Provider         string         `json:"provider"`
	Description      string         `json:"description,omitempty"`
	Languages        []string       `json:"languages,omitempty"`
	Gender           VoiceGender    `json:"gender,omitempty"`
	StyleTags        []string       `json:"style_tags,omitempty"`
	Models           []string       `json:"models,omitempty"`
	SampleText       string         `json:"sample_text,omitempty"`
	ProviderSettings map[string]any `json:"provider_settings,omitempty"`
}

// VoiceProviderCapabilities is the cached capability payload for a provider.
type VoiceProviderCapabilities struct {
	Provider              string          `json:"provider"`
	DisplayName           string          `json:"display_name"`
	Voices                []VoiceMetadata `json:"voices"`
	Models                []string        `json:"models,omitempty"`
	SupportsStreaming     bool            `json:"supports_streaming,omitempty"`
	SupportsSSML          bool            `json:"supports_ssml,omitempty"`
	SupportsVoiceCloning  bool            `json:"supports_voice_cloning,omitempty"`
	SupportsVoiceSettings bool            `json:"supports_voice_settings,omitempty"`
	Metadata              map[string]any  `json:"metadata,omitempty"`
	UpdatedAt             time.Time       `json:"updated_at,omitempty"`
}

// VoiceProvider is implemented by concrete TTS provider capability sources.
// Implementations may be static, configuration-backed, or API-backed, but must
// return a complete snapshot without requiring request/response control flows.
type VoiceProvider interface {
	ID() string
	DisplayName() string
	Capabilities(context.Context) (VoiceProviderCapabilities, error)
}

type voiceCapabilitiesCacheEntry struct {
	capabilities VoiceProviderCapabilities
	cachedAt     time.Time
}

// VoiceProviderRegistry stores providers and caches capability snapshots.
type VoiceProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]VoiceProvider
	cache     map[string]voiceCapabilitiesCacheEntry
	cacheTTL  time.Duration
	now       func() time.Time
}

// VoiceSpecResolution is the registry's interpretation of a SoulVoiceSpec.
type VoiceSpecResolution struct {
	Spec           domain.SoulVoiceSpec         `json:"spec"`
	Provider       VoiceProviderCapabilities    `json:"provider"`
	Voice          *VoiceMetadata               `json:"voice,omitempty"`
	VoiceID        string                       `json:"voice_id,omitempty"`
	Persona        *domain.SoulVoicePersonaSpec `json:"persona,omitempty"`
	ProviderConfig map[string]any               `json:"provider_config,omitempty"`
}

// NewVoiceProviderRegistry constructs a registry with the supplied providers.
func NewVoiceProviderRegistry(providers ...VoiceProvider) *VoiceProviderRegistry {
	registry := &VoiceProviderRegistry{
		providers: make(map[string]VoiceProvider),
		cache:     make(map[string]voiceCapabilitiesCacheEntry),
		cacheTTL:  DefaultVoiceCapabilitiesCacheTTL,
		now:       time.Now,
	}
	for _, provider := range providers {
		_ = registry.RegisterProvider(provider)
	}
	return registry
}

// NewDefaultVoiceProviderRegistry registers SoulFactory's built-in TTS catalogs.
func NewDefaultVoiceProviderRegistry() *VoiceProviderRegistry {
	return NewVoiceProviderRegistry(
		NewElevenLabsVoiceProvider(),
		newStaticVoiceProvider(azureSpeechCapabilities()),
		newStaticVoiceProvider(openAITTSCapabilities()),
		newStaticVoiceProvider(localCLICapabilities()),
	)
}

// SetCacheTTL updates the registry cache TTL and clears existing snapshots.
func (r *VoiceProviderRegistry) SetCacheTTL(ttl time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cacheTTL = ttl
	r.cache = make(map[string]voiceCapabilitiesCacheEntry)
}

// RegisterProvider adds or replaces a provider. Re-registering clears its cache.
func (r *VoiceProviderRegistry) RegisterProvider(provider VoiceProvider) error {
	if provider == nil {
		return fmt.Errorf("register voice provider: nil provider")
	}
	id := NormalizeVoiceProviderID(provider.ID())
	if id == "" {
		return fmt.Errorf("register voice provider: empty provider id")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[id] = provider
	delete(r.cache, id)
	return nil
}

// ProviderIDs returns registered provider IDs in stable order.
func (r *VoiceProviderRegistry) ProviderIDs() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	ids := make([]string, 0, len(r.providers))
	for id := range r.providers {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// Capabilities returns a cached provider capability snapshot.
func (r *VoiceProviderRegistry) Capabilities(ctx context.Context, providerID string) (VoiceProviderCapabilities, error) {
	id := NormalizeVoiceProviderID(providerID)
	if id == "" {
		return VoiceProviderCapabilities{}, fmt.Errorf("%w: empty provider id", ErrVoiceProviderNotFound)
	}

	r.mu.RLock()
	if entry, ok := r.cache[id]; ok && r.cacheEntryFresh(entry) {
		capabilities := cloneVoiceProviderCapabilities(entry.capabilities)
		r.mu.RUnlock()
		return capabilities, nil
	}
	provider := r.providers[id]
	r.mu.RUnlock()
	if provider == nil {
		return VoiceProviderCapabilities{}, fmt.Errorf("%w: %s", ErrVoiceProviderNotFound, id)
	}

	capabilities, err := provider.Capabilities(ctx)
	if err != nil {
		return VoiceProviderCapabilities{}, err
	}
	capabilities = normalizeVoiceProviderCapabilities(id, provider.DisplayName(), capabilities)

	r.mu.Lock()
	r.cache[id] = voiceCapabilitiesCacheEntry{capabilities: cloneVoiceProviderCapabilities(capabilities), cachedAt: r.now()}
	r.mu.Unlock()

	return capabilities, nil
}

// AllCapabilities returns capability snapshots for every registered provider.
func (r *VoiceProviderRegistry) AllCapabilities(ctx context.Context) ([]VoiceProviderCapabilities, error) {
	ids := r.ProviderIDs()
	capabilities := make([]VoiceProviderCapabilities, 0, len(ids))
	for _, id := range ids {
		providerCapabilities, err := r.Capabilities(ctx, id)
		if err != nil {
			return nil, err
		}
		capabilities = append(capabilities, providerCapabilities)
	}
	return capabilities, nil
}

// Voices returns all voices for one provider.
func (r *VoiceProviderRegistry) Voices(ctx context.Context, providerID string) ([]VoiceMetadata, error) {
	capabilities, err := r.Capabilities(ctx, providerID)
	if err != nil {
		return nil, err
	}
	return cloneVoiceMetadataSlice(capabilities.Voices), nil
}

// FindVoice returns a voice by ID or name within a provider catalog.
func (r *VoiceProviderRegistry) FindVoice(ctx context.Context, providerID, voiceID string) (*VoiceMetadata, error) {
	voiceID = strings.TrimSpace(voiceID)
	if voiceID == "" {
		return nil, fmt.Errorf("%w: empty voice id", ErrVoiceNotFound)
	}

	capabilities, err := r.Capabilities(ctx, providerID)
	if err != nil {
		return nil, err
	}
	return findVoiceInCapabilities(capabilities, voiceID)
}

func findVoiceInCapabilities(capabilities VoiceProviderCapabilities, voiceID string) (*VoiceMetadata, error) {
	for _, voice := range capabilities.Voices {
		if strings.EqualFold(voice.ID, voiceID) || strings.EqualFold(voice.Name, voiceID) {
			matched := cloneVoiceMetadata(voice)
			return &matched, nil
		}
	}
	return nil, fmt.Errorf("%w: %s/%s", ErrVoiceNotFound, capabilities.Provider, voiceID)
}

// ResolveVoiceSpec resolves provider capabilities and explicit provider voice
// bindings from a SoulVoiceSpec. PersonaID is preserved even when it represents
// an app-level persona rather than a concrete provider voice.
func (r *VoiceProviderRegistry) ResolveVoiceSpec(ctx context.Context, spec domain.SoulVoiceSpec) (*VoiceSpecResolution, error) {
	providerID := NormalizeVoiceProviderID(spec.Provider)
	if providerID == "" {
		providerID = onlyProviderFromSpec(spec)
	}
	if providerID == "" {
		return nil, fmt.Errorf("%w: SoulVoiceSpec.provider is required", ErrVoiceProviderNotFound)
	}

	capabilities, err := r.Capabilities(ctx, providerID)
	if err != nil {
		return nil, err
	}

	providerConfig := providerConfigFor(spec, providerID)
	voiceID, explicitProviderVoiceID := voiceIDFromProviderConfig(providerConfig)
	if voiceID == "" {
		voiceID = strings.TrimSpace(spec.PersonaID)
	}

	var voice *VoiceMetadata
	if voiceID != "" {
		matched, findErr := findVoiceInCapabilities(capabilities, voiceID)
		if findErr == nil {
			voice = matched
		} else if explicitProviderVoiceID {
			return nil, findErr
		}
	}

	return &VoiceSpecResolution{
		Spec:           spec,
		Provider:       capabilities,
		Voice:          voice,
		VoiceID:        voiceID,
		Persona:        spec.Persona,
		ProviderConfig: providerConfig,
	}, nil
}

func (r *VoiceProviderRegistry) cacheEntryFresh(entry voiceCapabilitiesCacheEntry) bool {
	if r.cacheTTL < 0 {
		return false
	}
	if r.cacheTTL == 0 {
		return true
	}
	return r.now().Sub(entry.cachedAt) < r.cacheTTL
}

// NormalizeVoiceProviderID maps common config aliases to canonical registry IDs.
func NormalizeVoiceProviderID(providerID string) string {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "eleven", "eleven-labs", "elevenlabs":
		return VoiceProviderElevenLabs
	case "azure", "azure-speech", "microsoft", "microsoft-speech":
		return VoiceProviderAzureSpeech
	case "openai", "openai-tts", "openai-speech":
		return VoiceProviderOpenAITTS
	case "local", "local-cli", "tts-local-cli", "cli":
		return VoiceProviderLocalCLI
	default:
		return strings.ToLower(strings.TrimSpace(providerID))
	}
}

// ElevenLabsVoiceProvider exposes the default ElevenLabs voice catalog used by
// SoulFactory until a credential-backed provider source is wired in.
type ElevenLabsVoiceProvider struct {
	capabilities VoiceProviderCapabilities
}

func NewElevenLabsVoiceProvider() *ElevenLabsVoiceProvider {
	return &ElevenLabsVoiceProvider{capabilities: elevenLabsCapabilities()}
}

func (p *ElevenLabsVoiceProvider) ID() string { return VoiceProviderElevenLabs }

func (p *ElevenLabsVoiceProvider) DisplayName() string { return "ElevenLabs" }

func (p *ElevenLabsVoiceProvider) Capabilities(context.Context) (VoiceProviderCapabilities, error) {
	return cloneVoiceProviderCapabilities(p.capabilities), nil
}

type staticVoiceProvider struct {
	capabilities VoiceProviderCapabilities
}

func newStaticVoiceProvider(capabilities VoiceProviderCapabilities) *staticVoiceProvider {
	return &staticVoiceProvider{capabilities: capabilities}
}

func (p *staticVoiceProvider) ID() string { return p.capabilities.Provider }

func (p *staticVoiceProvider) DisplayName() string { return p.capabilities.DisplayName }

func (p *staticVoiceProvider) Capabilities(context.Context) (VoiceProviderCapabilities, error) {
	return cloneVoiceProviderCapabilities(p.capabilities), nil
}

func elevenLabsCapabilities() VoiceProviderCapabilities {
	models := []string{"eleven_multilingual_v2", "eleven_turbo_v2_5", "eleven_v3"}
	return VoiceProviderCapabilities{
		Provider:              VoiceProviderElevenLabs,
		DisplayName:           "ElevenLabs",
		Models:                models,
		SupportsStreaming:     true,
		SupportsVoiceCloning:  true,
		SupportsVoiceSettings: true,
		Voices: []VoiceMetadata{
			{
				ID:          "21m00Tcm4TlvDq8ikWAM",
				Name:        "Rachel",
				Description: "Calm, conversational English narration voice.",
				Languages:   []string{"en"},
				Gender:      VoiceGenderFeminine,
				StyleTags:   []string{"calm", "conversational", "narration"},
				Models:      models,
			},
			{
				ID:          "pNInz6obpgDQGcFmaJgB",
				Name:        "Adam",
				Description: "Deep, steady English voice suited to agent narration.",
				Languages:   []string{"en"},
				Gender:      VoiceGenderMasculine,
				StyleTags:   []string{"deep", "steady", "professional"},
				Models:      models,
			},
			{
				ID:          "EXAVITQu4vr4xnSDxMaL",
				Name:        "Bella",
				Description: "Expressive English voice for warm assistant personas.",
				Languages:   []string{"en"},
				Gender:      VoiceGenderFeminine,
				StyleTags:   []string{"expressive", "warm", "friendly"},
				Models:      models,
			},
			{
				ID:          "TxGEqnHWrfWFTfGW9XjX",
				Name:        "Josh",
				Description: "Conversational English voice with relaxed pacing.",
				Languages:   []string{"en"},
				Gender:      VoiceGenderMasculine,
				StyleTags:   []string{"conversational", "relaxed", "assistant"},
				Models:      models,
			},
		},
		Metadata: map[string]any{
			"api_key_env": []string{"ELEVENLABS_API_KEY", "XI_API_KEY"},
		},
	}
}

func azureSpeechCapabilities() VoiceProviderCapabilities {
	models := []string{"azure-neural-tts"}
	return VoiceProviderCapabilities{
		Provider:              VoiceProviderAzureSpeech,
		DisplayName:           "Azure Speech",
		Models:                models,
		SupportsSSML:          true,
		SupportsVoiceSettings: true,
		Voices: []VoiceMetadata{
			{ID: "en-US-JennyNeural", Name: "Jenny", Languages: []string{"en-US"}, Gender: VoiceGenderFeminine, StyleTags: []string{"friendly", "assistant"}, Models: models},
			{ID: "en-US-GuyNeural", Name: "Guy", Languages: []string{"en-US"}, Gender: VoiceGenderMasculine, StyleTags: []string{"clear", "professional"}, Models: models},
			{ID: "en-GB-SoniaNeural", Name: "Sonia", Languages: []string{"en-GB"}, Gender: VoiceGenderFeminine, StyleTags: []string{"formal", "narration"}, Models: models},
			{ID: "es-ES-ElviraNeural", Name: "Elvira", Languages: []string{"es-ES"}, Gender: VoiceGenderFeminine, StyleTags: []string{"clear", "conversational"}, Models: models},
		},
		Metadata: map[string]any{"config_keys": []string{"subscriptionKey", "region"}},
	}
}

func openAITTSCapabilities() VoiceProviderCapabilities {
	models := []string{"gpt-4o-mini-tts", "tts-1", "tts-1-hd"}
	voices := []string{"alloy", "echo", "fable", "nova", "onyx", "shimmer"}
	metadata := make([]VoiceMetadata, 0, len(voices))
	for _, voice := range voices {
		metadata = append(metadata, VoiceMetadata{
			ID:        voice,
			Name:      strings.Title(voice),
			Languages: []string{"multilingual"},
			Gender:    VoiceGenderNeutral,
			StyleTags: []string{"general", "assistant"},
			Models:    models,
		})
	}
	return VoiceProviderCapabilities{
		Provider:              VoiceProviderOpenAITTS,
		DisplayName:           "OpenAI TTS",
		Models:                models,
		SupportsStreaming:     true,
		SupportsVoiceSettings: true,
		Voices:                metadata,
		Metadata:              map[string]any{"api_key_env": []string{"OPENAI_API_KEY"}},
	}
}

func localCLICapabilities() VoiceProviderCapabilities {
	models := []string{"local-cli"}
	return VoiceProviderCapabilities{
		Provider:    VoiceProviderLocalCLI,
		DisplayName: "Local CLI",
		Models:      models,
		Voices: []VoiceMetadata{
			{ID: "default", Name: "Default CLI Voice", Languages: []string{"system"}, Gender: VoiceGenderUnspecified, StyleTags: []string{"local", "offline"}, Models: models},
			{ID: "system", Name: "System Voice", Languages: []string{"system"}, Gender: VoiceGenderUnspecified, StyleTags: []string{"system", "fallback"}, Models: models},
		},
		Metadata: map[string]any{"config_keys": []string{"command", "args", "outputFormat"}},
	}
}

func normalizeVoiceProviderCapabilities(providerID, displayName string, capabilities VoiceProviderCapabilities) VoiceProviderCapabilities {
	capabilities = cloneVoiceProviderCapabilities(capabilities)
	capabilities.Provider = providerID
	if strings.TrimSpace(capabilities.DisplayName) == "" {
		capabilities.DisplayName = displayName
	}
	if capabilities.UpdatedAt.IsZero() {
		capabilities.UpdatedAt = time.Now().UTC()
	}
	for i := range capabilities.Voices {
		capabilities.Voices[i].Provider = providerID
		if capabilities.Voices[i].Gender == "" {
			capabilities.Voices[i].Gender = VoiceGenderUnspecified
		}
	}
	sort.SliceStable(capabilities.Voices, func(i, j int) bool {
		return strings.ToLower(capabilities.Voices[i].Name) < strings.ToLower(capabilities.Voices[j].Name)
	})
	return capabilities
}

func providerConfigFor(spec domain.SoulVoiceSpec, providerID string) map[string]any {
	if len(spec.Providers) == 0 {
		return nil
	}
	for key, config := range spec.Providers {
		if NormalizeVoiceProviderID(key) == providerID {
			return cloneMap(config)
		}
	}
	return nil
}

func onlyProviderFromSpec(spec domain.SoulVoiceSpec) string {
	if len(spec.Providers) != 1 {
		return ""
	}
	for providerID := range spec.Providers {
		return NormalizeVoiceProviderID(providerID)
	}
	return ""
}

func voiceIDFromProviderConfig(config map[string]any) (string, bool) {
	for _, key := range []string{"voice_id", "voiceId", "voice", "id"} {
		value, ok := config[key]
		if !ok {
			continue
		}
		if voiceID, ok := value.(string); ok && strings.TrimSpace(voiceID) != "" {
			return strings.TrimSpace(voiceID), true
		}
	}
	return "", false
}

func cloneVoiceProviderCapabilities(capabilities VoiceProviderCapabilities) VoiceProviderCapabilities {
	clone := capabilities
	clone.Voices = cloneVoiceMetadataSlice(capabilities.Voices)
	clone.Models = cloneStringSlice(capabilities.Models)
	clone.Metadata = cloneMap(capabilities.Metadata)
	return clone
}

func cloneVoiceMetadataSlice(voices []VoiceMetadata) []VoiceMetadata {
	if voices == nil {
		return nil
	}
	clone := make([]VoiceMetadata, len(voices))
	for i, voice := range voices {
		clone[i] = cloneVoiceMetadata(voice)
	}
	return clone
}

func cloneVoiceMetadata(voice VoiceMetadata) VoiceMetadata {
	clone := voice
	clone.Languages = cloneStringSlice(voice.Languages)
	clone.StyleTags = cloneStringSlice(voice.StyleTags)
	clone.Models = cloneStringSlice(voice.Models)
	clone.ProviderSettings = cloneMap(voice.ProviderSettings)
	return clone
}

func cloneStringSlice(values []string) []string {
	if values == nil {
		return nil
	}
	clone := append([]string(nil), values...)
	return clone
}

func cloneMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	clone := make(map[string]any, len(values))
	for key, value := range values {
		clone[key] = deepCopyVoiceValue(value)
	}
	return clone
}

func deepCopyVoiceValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		clone := make([]any, len(typed))
		for i, item := range typed {
			clone[i] = deepCopyVoiceValue(item)
		}
		return clone
	case []string:
		return cloneStringSlice(typed)
	case []int:
		return append([]int(nil), typed...)
	case []float64:
		return append([]float64(nil), typed...)
	case []bool:
		return append([]bool(nil), typed...)
	default:
		return value
	}
}
