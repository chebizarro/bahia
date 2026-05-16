package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/openagentsinc/bahia/internal/domain"
)

const (
	// AvatarProviderFluxComfyUI is the local FLUX/ComfyUI backend exposed by Lemmy.
	AvatarProviderFluxComfyUI = "flux-comfyui"
	// AvatarProviderFal is the hosted fal.ai image generation backend.
	AvatarProviderFal = "fal"
	// AvatarProviderReplicate is the hosted Replicate image generation backend.
	AvatarProviderReplicate = "replicate"

	defaultAvatarWidth  = 512
	defaultAvatarHeight = 512
	minAvatarDimension  = 64
	maxAvatarDimension  = 2048
	avatarDimensionStep = 8
)

// AvatarProgressStage identifies a generation lifecycle step.
type AvatarProgressStage string

const (
	AvatarProgressQueued      AvatarProgressStage = "queued"
	AvatarProgressDispatching AvatarProgressStage = "dispatching"
	AvatarProgressSubmitted   AvatarProgressStage = "submitted"
	AvatarProgressDownloading AvatarProgressStage = "downloading"
	AvatarProgressCompleted   AvatarProgressStage = "completed"
	AvatarProgressFailed      AvatarProgressStage = "failed"
)

// AvatarGenerator generates agent avatars through registered providers.
type AvatarGenerator struct {
	registry        *AvatarProviderRegistry
	defaultProvider string
	logger          *slog.Logger
}

// AvatarConfig holds avatar generator configuration.
type AvatarConfig struct {
	LemmyURL string        // Lemmy ComfyUI API endpoint
	Timeout  time.Duration // Request timeout
	Provider string        // Default provider name; must match a configured provider
}

// AvatarGenerationRequest is the provider-ready avatar generation input.
type AvatarGenerationRequest struct {
	Provider       string
	Prompt         string
	OriginalPrompt string
	StylePreset    string
	NegativePrompt string
	Seed           string
	Width          int
	Height         int
}

// AvatarResult contains the generated avatar.
type AvatarResult struct {
	ImageData   []byte // PNG image data
	ContentType string // MIME type (image/png)
	Seed        string // Generation seed for reproducibility
	Provider    string // Provider that produced the image
	SourceURL   string // Optional provider URL suitable for direct-reference fallback
}

// AvatarProgressEvent reports asynchronous avatar generation progress.
type AvatarProgressEvent struct {
	Provider string
	Stage    AvatarProgressStage
	Message  string
	Percent  int
	Result   *AvatarResult
	Error    string
}

// AvatarProgressFunc receives generation progress events.
type AvatarProgressFunc func(AvatarProgressEvent)

// AvatarProvider generates avatars for one backend.
// Providers should emit intermediate progress only; AvatarGenerator owns terminal
// completed/failed events so callers receive exactly one terminal event.
type AvatarProvider interface {
	Name() string
	GenerateAvatar(ctx context.Context, req AvatarGenerationRequest, progress AvatarProgressFunc) (*AvatarResult, error)
}

// AvatarProviderInfo describes a registered provider and whether it is usable.
type AvatarProviderInfo struct {
	Name      string
	Available bool
	Reason    string
}

type avatarProviderAvailability interface {
	Available() (bool, string)
}

// AvatarProviderRegistry stores avatar generation providers by name.
type AvatarProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]AvatarProvider
}

// NewAvatarProviderRegistry creates a registry preloaded with providers.
func NewAvatarProviderRegistry(providers ...AvatarProvider) *AvatarProviderRegistry {
	r := &AvatarProviderRegistry{providers: make(map[string]AvatarProvider)}
	for _, provider := range providers {
		_ = r.Register(provider)
	}
	return r
}

// Register adds or replaces an avatar provider.
func (r *AvatarProviderRegistry) Register(provider AvatarProvider) error {
	if provider == nil {
		return errors.New("avatar provider is nil")
	}
	name := normalizeAvatarProvider(provider.Name())
	if name == "" {
		return errors.New("avatar provider name is empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[name] = provider
	return nil
}

// Provider returns a provider by name.
func (r *AvatarProviderRegistry) Provider(name string) (AvatarProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	provider, ok := r.providers[normalizeAvatarProvider(name)]
	return provider, ok
}

// Names returns currently available provider names in stable order.
func (r *AvatarProviderRegistry) Names() []string {
	infos := r.ProviderInfos()
	names := make([]string, 0, len(infos))
	for _, info := range infos {
		if info.Available {
			names = append(names, info.Name)
		}
	}
	return names
}

// ProviderInfos returns registered providers with availability metadata.
func (r *AvatarProviderRegistry) ProviderInfos() []AvatarProviderInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]AvatarProviderInfo, 0, len(r.providers))
	for name, provider := range r.providers {
		info := AvatarProviderInfo{Name: name, Available: true}
		if availability, ok := provider.(avatarProviderAvailability); ok {
			info.Available, info.Reason = availability.Available()
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// AvatarStylePreset describes a reusable style prompt template.
type AvatarStylePreset struct {
	ID             string
	Name           string
	Description    string
	PromptTemplate string
	NegativePrompt string
}

var avatarStylePresets = map[string]AvatarStylePreset{
	"pixel-art": {
		ID:             "pixel-art",
		Name:           "Pixel Art",
		Description:    "Crisp retro game-style avatar with readable silhouette.",
		PromptTemplate: "Pixel art style avatar for an AI agent: {{prompt}}. Clean 32-bit sprite aesthetic, friendly appearance, centered composition, simple background, high quality, detailed.",
		NegativePrompt: "blurry, photorealistic, noisy, cluttered background, distorted face",
	},
	"anime": {
		ID:             "anime",
		Name:           "Anime",
		Description:    "Expressive illustrated anime portrait with characterful details.",
		PromptTemplate: "Anime-style character portrait for an AI agent: {{prompt}}. Expressive eyes, polished digital illustration, clean linework, soft lighting, centered bust portrait, high quality.",
		NegativePrompt: "low quality, extra limbs, distorted anatomy, messy background, text, watermark",
	},
	"realistic": {
		ID:             "realistic",
		Name:           "Realistic",
		Description:    "Realistic cinematic portrait suitable for a professional agent profile.",
		PromptTemplate: "Realistic cinematic portrait avatar for an AI agent: {{prompt}}. Natural lighting, detailed materials, shallow depth of field, clean neutral background, high-end editorial quality.",
		NegativePrompt: "cartoon, uncanny, distorted face, low resolution, harsh shadows, watermark",
	},
	"abstract": {
		ID:             "abstract",
		Name:           "Abstract",
		Description:    "Symbolic abstract identity mark for non-human or conceptual agents.",
		PromptTemplate: "Abstract symbolic avatar for an AI agent: {{prompt}}. Geometric composition, vibrant but balanced color palette, clean vector-like forms, centered icon, modern generative art style.",
		NegativePrompt: "photorealistic face, clutter, text, watermark, muddy colors, low contrast",
	},
	"corporate": {
		ID:             "corporate",
		Name:           "Corporate",
		Description:    "Polished professional avatar for enterprise-facing assistants.",
		PromptTemplate: "Corporate professional avatar for an AI agent: {{prompt}}. Trustworthy, approachable, polished brand-safe design, clean background, balanced lighting, premium product illustration.",
		NegativePrompt: "casual selfie, chaotic background, meme style, low quality, text, watermark",
	},
}

// AvatarStylePresets returns all style presets in stable order.
func AvatarStylePresets() []AvatarStylePreset {
	presets := make([]AvatarStylePreset, 0, len(avatarStylePresets))
	for _, preset := range avatarStylePresets {
		presets = append(presets, preset)
	}
	sort.Slice(presets, func(i, j int) bool { return presets[i].ID < presets[j].ID })
	return presets
}

// AvatarStylePresetByID returns one style preset by ID.
func AvatarStylePresetByID(id string) (AvatarStylePreset, bool) {
	preset, ok := avatarStylePresets[normalizeAvatarPreset(id)]
	return preset, ok
}

// ExpandAvatarStylePreset applies a named style preset to a base prompt.
func ExpandAvatarStylePreset(prompt, presetID string) (expandedPrompt string, negativePrompt string, err error) {
	prompt = strings.TrimSpace(prompt)
	presetID = normalizeAvatarPreset(presetID)
	if presetID == "" {
		return prompt, "", nil
	}
	preset, ok := avatarStylePresets[presetID]
	if !ok {
		return "", "", fmt.Errorf("unknown avatar style preset %q", presetID)
	}
	return strings.ReplaceAll(preset.PromptTemplate, "{{prompt}}", prompt), preset.NegativePrompt, nil
}

// NewAvatarGenerator creates a new provider-based avatar generator.
func NewAvatarGenerator(config AvatarConfig, logger *slog.Logger) *AvatarGenerator {
	config.LemmyURL = strings.TrimRight(strings.TrimSpace(config.LemmyURL), "/")
	if config.Timeout == 0 {
		config.Timeout = 60 * time.Second
	}
	if logger == nil {
		logger = slog.Default()
	}

	componentLogger := logger.With("component", "avatar")
	client := &http.Client{Timeout: config.Timeout}
	registry := NewAvatarProviderRegistry()
	if config.LemmyURL != "" {
		_ = registry.Register(NewFluxComfyUIAvatarProvider(config.LemmyURL, client, componentLogger))
	}

	defaultProvider := normalizeAvatarProvider(config.Provider)
	if defaultProvider == "" {
		names := registry.Names()
		if len(names) == 1 {
			defaultProvider = names[0]
		}
	}

	return &AvatarGenerator{
		registry:        registry,
		defaultProvider: defaultProvider,
		logger:          componentLogger,
	}
}

// RegisterProvider adds or replaces a provider in this generator's registry.
func (g *AvatarGenerator) RegisterProvider(provider AvatarProvider) error {
	if err := g.registry.Register(provider); err != nil {
		return err
	}
	if g.defaultProvider == "" {
		names := g.registry.Names()
		if len(names) == 1 {
			g.defaultProvider = names[0]
		}
	}
	return nil
}

// ProviderNames returns currently available provider names.
func (g *AvatarGenerator) ProviderNames() []string {
	return g.registry.Names()
}

// ProviderInfos returns registered providers with availability metadata.
func (g *AvatarGenerator) ProviderInfos() []AvatarProviderInfo {
	return g.registry.ProviderInfos()
}

// Generate creates an avatar from a prompt using the default provider.
func (g *AvatarGenerator) Generate(ctx context.Context, prompt string, seed string) (*AvatarResult, error) {
	return g.GenerateWithSpec(ctx, domain.SoulAvatarGenerationSpec{
		Prompt:   prompt,
		Seed:     seed,
		Provider: g.defaultProvider,
	}, nil)
}

// GenerateWithSpec generates an avatar from a SoulAvatarGenerationSpec.
func (g *AvatarGenerator) GenerateWithSpec(ctx context.Context, spec domain.SoulAvatarGenerationSpec, progress AvatarProgressFunc) (*AvatarResult, error) {
	req, err := g.requestFromSpec(spec)
	if err != nil {
		emitAvatarProgress(progress, AvatarProgressEvent{Provider: normalizeAvatarProvider(spec.Provider), Stage: AvatarProgressFailed, Percent: 100, Error: err.Error()})
		return nil, err
	}

	emitAvatarProgress(progress, AvatarProgressEvent{Provider: req.Provider, Stage: AvatarProgressQueued, Percent: 0, Message: "avatar generation queued"})
	provider, ok := g.registry.Provider(req.Provider)
	if !ok {
		err := fmt.Errorf("avatar provider %q is not registered", req.Provider)
		emitAvatarProgress(progress, AvatarProgressEvent{Provider: req.Provider, Stage: AvatarProgressFailed, Percent: 100, Error: err.Error()})
		return nil, err
	}

	g.logger.Info("generating avatar", "provider", req.Provider, "prompt_length", len(req.Prompt), "style_preset", req.StylePreset)
	emitAvatarProgress(progress, AvatarProgressEvent{Provider: req.Provider, Stage: AvatarProgressDispatching, Percent: 10, Message: "dispatching avatar generation to provider"})

	terminalSent := false
	result, err := provider.GenerateAvatar(ctx, req, func(event AvatarProgressEvent) {
		if event.Provider == "" {
			event.Provider = req.Provider
		}
		if event.Stage == AvatarProgressCompleted || event.Stage == AvatarProgressFailed {
			if terminalSent {
				return
			}
			terminalSent = true
		}
		emitAvatarProgress(progress, event)
	})
	if err != nil {
		if !terminalSent {
			emitAvatarProgress(progress, AvatarProgressEvent{Provider: req.Provider, Stage: AvatarProgressFailed, Percent: 100, Error: err.Error()})
		}
		return nil, err
	}
	if result != nil && result.Provider == "" {
		result.Provider = req.Provider
	}
	if !terminalSent {
		emitAvatarProgress(progress, AvatarProgressEvent{Provider: req.Provider, Stage: AvatarProgressCompleted, Percent: 100, Message: "avatar generation complete", Result: result})
	}
	return result, nil
}

// GenerateAsync starts avatar generation in a goroutine and streams progress events.
func (g *AvatarGenerator) GenerateAsync(ctx context.Context, spec domain.SoulAvatarGenerationSpec) <-chan AvatarProgressEvent {
	events := make(chan AvatarProgressEvent, 8)
	go func() {
		defer close(events)
		_, _ = g.GenerateWithSpec(ctx, spec, func(event AvatarProgressEvent) {
			select {
			case events <- event:
			case <-ctx.Done():
			}
		})
	}()
	return events
}

// GenerateDefault generates an avatar with default pixel-art styling.
func (g *AvatarGenerator) GenerateDefault(ctx context.Context, agentName, purpose string) (*AvatarResult, error) {
	prompt := fmt.Sprintf("robot avatar for an AI agent named %s. %s", agentName, purpose)
	return g.GenerateWithSpec(ctx, domain.SoulAvatarGenerationSpec{
		Prompt:      prompt,
		StylePreset: "pixel-art",
		Seed:        agentName,
		Provider:    g.defaultProvider,
	}, nil)
}

func (g *AvatarGenerator) requestFromSpec(spec domain.SoulAvatarGenerationSpec) (AvatarGenerationRequest, error) {
	provider := normalizeAvatarProvider(spec.Provider)
	if provider == "" {
		provider = g.defaultProvider
	}
	if provider == "" {
		names := g.registry.Names()
		if len(names) == 1 {
			provider = names[0]
		}
	}
	if provider == "" {
		return AvatarGenerationRequest{}, errors.New("no avatar providers configured")
	}

	width := spec.Width
	if width <= 0 {
		width = defaultAvatarWidth
	}
	height := spec.Height
	if height <= 0 {
		height = defaultAvatarHeight
	}
	if err := validateAvatarDimensions(width, height); err != nil {
		return AvatarGenerationRequest{}, err
	}

	prompt := strings.TrimSpace(spec.Prompt)
	if prompt == "" {
		return AvatarGenerationRequest{}, errors.New("avatar prompt is required")
	}
	expandedPrompt, negativePrompt, err := ExpandAvatarStylePreset(prompt, spec.StylePreset)
	if err != nil {
		return AvatarGenerationRequest{}, err
	}

	return AvatarGenerationRequest{
		Provider:       provider,
		Prompt:         expandedPrompt,
		OriginalPrompt: prompt,
		StylePreset:    normalizeAvatarPreset(spec.StylePreset),
		NegativePrompt: negativePrompt,
		Seed:           strings.TrimSpace(spec.Seed),
		Width:          width,
		Height:         height,
	}, nil
}

// FluxComfyUIAvatarProvider generates avatars through the Lemmy FLUX/ComfyUI HTTP API.
type FluxComfyUIAvatarProvider struct {
	baseURL    string
	httpClient *http.Client
	logger     *slog.Logger
}

// NewFluxComfyUIAvatarProvider creates a FLUX/ComfyUI provider.
func NewFluxComfyUIAvatarProvider(baseURL string, httpClient *http.Client, logger *slog.Logger) *FluxComfyUIAvatarProvider {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 60 * time.Second}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &FluxComfyUIAvatarProvider{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: httpClient,
		logger:     logger.With("provider", AvatarProviderFluxComfyUI),
	}
}

// Name returns the provider name.
func (p *FluxComfyUIAvatarProvider) Name() string { return AvatarProviderFluxComfyUI }

// Available reports whether the provider has enough configuration to generate avatars.
func (p *FluxComfyUIAvatarProvider) Available() (bool, string) {
	if p.baseURL == "" {
		return false, "flux-comfyui base URL is empty"
	}
	return true, ""
}

// GenerateAvatar creates an avatar through the FLUX/ComfyUI backend.
func (p *FluxComfyUIAvatarProvider) GenerateAvatar(ctx context.Context, req AvatarGenerationRequest, progress AvatarProgressFunc) (*AvatarResult, error) {
	if p.baseURL == "" {
		return nil, errors.New("flux-comfyui base URL is empty")
	}
	emitAvatarProgress(progress, AvatarProgressEvent{Provider: p.Name(), Stage: AvatarProgressSubmitted, Percent: 25, Message: "submitted avatar prompt to flux-comfyui"})

	reqBody := map[string]interface{}{
		"prompt": req.Prompt,
		"seed":   req.Seed,
		"width":  req.Width,
		"height": req.Height,
	}
	if req.NegativePrompt != "" {
		reqBody["negative_prompt"] = req.NegativePrompt
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	url := fmt.Sprintf("%s/api/generate", p.baseURL)
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := p.httpClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.HasPrefix(contentType, "application/json") {
		var apiResp struct {
			OutputPath string `json:"output_path"`
			ImageURL   string `json:"image_url"`
			Seed       string `json:"seed"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
			return nil, fmt.Errorf("decode response: %w", err)
		}

		imageURL := apiResp.ImageURL
		if imageURL == "" && apiResp.OutputPath != "" {
			imageURL = fmt.Sprintf("%s/outputs/%s", p.baseURL, strings.TrimLeft(apiResp.OutputPath, "/"))
		}
		if imageURL == "" {
			return nil, errors.New("flux-comfyui response did not include image_url or output_path")
		}

		emitAvatarProgress(progress, AvatarProgressEvent{Provider: p.Name(), Stage: AvatarProgressDownloading, Percent: 75, Message: "downloading generated avatar image"})
		imageData, imageContentType, err := p.fetchImage(ctx, imageURL)
		if err != nil {
			return nil, fmt.Errorf("fetch image: %w", err)
		}
		if apiResp.Seed == "" {
			apiResp.Seed = req.Seed
		}
		return &AvatarResult{ImageData: imageData, ContentType: imageContentType, Seed: apiResp.Seed, Provider: p.Name(), SourceURL: imageURL}, nil
	}

	imageData, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read image: %w", err)
	}
	if contentType == "" {
		contentType = "image/png"
	}
	return &AvatarResult{ImageData: imageData, ContentType: contentType, Seed: req.Seed, Provider: p.Name()}, nil
}

func (p *FluxComfyUIAvatarProvider) fetchImage(ctx context.Context, url string) ([]byte, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, "", err
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("image fetch error %d", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/png"
	}
	imageData, err := io.ReadAll(resp.Body)
	return imageData, contentType, err
}

// UnavailableAvatarProvider reserves a registry slot for a provider whose concrete
// credentials/client have not been configured yet.
type UnavailableAvatarProvider struct {
	NameValue string
	Reason    string
}

// Name returns the provider name.
func (p UnavailableAvatarProvider) Name() string { return p.NameValue }

// Available reports that this placeholder provider is not currently usable.
func (p UnavailableAvatarProvider) Available() (bool, string) {
	if p.Reason != "" {
		return false, p.Reason
	}
	return false, fmt.Sprintf("avatar provider %q is not available", p.NameValue)
}

// GenerateAvatar reports why this provider cannot currently generate avatars.
func (p UnavailableAvatarProvider) GenerateAvatar(context.Context, AvatarGenerationRequest, AvatarProgressFunc) (*AvatarResult, error) {
	_, reason := p.Available()
	return nil, errors.New(reason)
}

func validateAvatarDimensions(width, height int) error {
	if width < minAvatarDimension || width > maxAvatarDimension || height < minAvatarDimension || height > maxAvatarDimension {
		return fmt.Errorf("avatar dimensions must be between %d and %d pixels", minAvatarDimension, maxAvatarDimension)
	}
	if width%avatarDimensionStep != 0 || height%avatarDimensionStep != 0 {
		return fmt.Errorf("avatar dimensions must be multiples of %d pixels", avatarDimensionStep)
	}
	return nil
}

func emitAvatarProgress(progress AvatarProgressFunc, event AvatarProgressEvent) {
	if progress != nil {
		progress(event)
	}
}

func normalizeAvatarProvider(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

func normalizeAvatarPreset(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

var _ AvatarProvider = (*FluxComfyUIAvatarProvider)(nil)
var _ AvatarProvider = UnavailableAvatarProvider{}
