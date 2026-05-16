package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/openagentsinc/bahia/internal/domain"
)

type fakeAvatarProvider struct {
	name string
	got  AvatarGenerationRequest
}

func (f *fakeAvatarProvider) Name() string { return f.name }

func (f *fakeAvatarProvider) GenerateAvatar(_ context.Context, req AvatarGenerationRequest, progress AvatarProgressFunc) (*AvatarResult, error) {
	f.got = req
	progress(AvatarProgressEvent{Stage: AvatarProgressSubmitted, Percent: 40, Message: "fake provider started"})
	return &AvatarResult{ImageData: []byte("fake-image"), ContentType: "image/png", Seed: req.Seed}, nil
}

func TestFluxComfyUIProviderExpandsPresetAndReportsProgress(t *testing.T) {
	var seen struct {
		Prompt         string `json:"prompt"`
		NegativePrompt string `json:"negative_prompt"`
		Seed           string `json:"seed"`
		Width          int    `json:"width"`
		Height         int    `json:"height"`
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/generate":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&seen); err != nil {
				t.Fatalf("decode request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{
				"image_url": serverURL(r) + "/image.png",
				"seed":      "provider-seed",
			})
		case "/image.png":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("png-data"))
		default:
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	generator := NewAvatarGenerator(AvatarConfig{LemmyURL: server.URL}, nil)
	var events []AvatarProgressEvent
	result, err := generator.GenerateWithSpec(t.Context(), domain.SoulAvatarGenerationSpec{
		Prompt:      "owl analyst with magnifying glass",
		StylePreset: "pixel-art",
		Seed:        "draft-seed",
		Width:       128,
		Height:      256,
		Provider:    AvatarProviderFluxComfyUI,
	}, func(event AvatarProgressEvent) { events = append(events, event) })
	if err != nil {
		t.Fatalf("GenerateWithSpec: %v", err)
	}
	if string(result.ImageData) != "png-data" || result.ContentType != "image/png" || result.Seed != "provider-seed" || result.Provider != AvatarProviderFluxComfyUI {
		t.Fatalf("unexpected result: %#v", result)
	}
	if !strings.Contains(seen.Prompt, "Pixel art style avatar") || !strings.Contains(seen.Prompt, "owl analyst") {
		t.Fatalf("preset did not expand prompt: %q", seen.Prompt)
	}
	if seen.NegativePrompt == "" {
		t.Fatalf("expected preset negative prompt")
	}
	if seen.Seed != "draft-seed" || seen.Width != 128 || seen.Height != 256 {
		t.Fatalf("request fields not forwarded: %#v", seen)
	}
	assertProgressStages(t, events, AvatarProgressQueued, AvatarProgressDispatching, AvatarProgressSubmitted, AvatarProgressDownloading, AvatarProgressCompleted)
}

func TestAvatarProviderRegistryDispatchesBySpecProvider(t *testing.T) {
	generator := NewAvatarGenerator(AvatarConfig{}, nil)
	fake := &fakeAvatarProvider{name: "test-provider"}
	if err := generator.RegisterProvider(fake); err != nil {
		t.Fatalf("register provider: %v", err)
	}

	result, err := generator.GenerateWithSpec(t.Context(), domain.SoulAvatarGenerationSpec{
		Prompt:      "friendly compliance assistant",
		StylePreset: "corporate",
		Seed:        "seed-1",
		Provider:    "test-provider",
	}, nil)
	if err != nil {
		t.Fatalf("GenerateWithSpec: %v", err)
	}
	if result.Provider != "test-provider" || string(result.ImageData) != "fake-image" {
		t.Fatalf("unexpected dispatch result: %#v", result)
	}
	if fake.got.Provider != "test-provider" || fake.got.Seed != "seed-1" {
		t.Fatalf("provider request not populated: %#v", fake.got)
	}
	if !strings.Contains(fake.got.Prompt, "Corporate professional avatar") || !strings.Contains(fake.got.Prompt, "friendly compliance assistant") {
		t.Fatalf("corporate preset did not expand prompt: %q", fake.got.Prompt)
	}

	names := generator.ProviderNames()
	if !slices.Contains(names, "test-provider") {
		t.Fatalf("available provider missing from names %v", names)
	}
	if slices.Contains(names, AvatarProviderFal) || slices.Contains(names, AvatarProviderReplicate) || slices.Contains(names, AvatarProviderFluxComfyUI) {
		t.Fatalf("unconfigured providers should not be in available names %v", names)
	}
	for _, info := range generator.ProviderInfos() {
		switch info.Name {
		case AvatarProviderFal, AvatarProviderReplicate, AvatarProviderFluxComfyUI:
			t.Fatalf("unconfigured provider %q should not be advertised in infos %#v", info.Name, generator.ProviderInfos())
		}
	}
}

func TestAvatarGeneratorAsyncProgressEvents(t *testing.T) {
	generator := NewAvatarGenerator(AvatarConfig{}, nil)
	fake := &fakeAvatarProvider{name: "async-provider"}
	if err := generator.RegisterProvider(fake); err != nil {
		t.Fatalf("register provider: %v", err)
	}

	var events []AvatarProgressEvent
	for event := range generator.GenerateAsync(t.Context(), domain.SoulAvatarGenerationSpec{
		Prompt:   "abstract fox",
		Seed:     "seed-async",
		Provider: "async-provider",
	}) {
		events = append(events, event)
	}

	assertProgressStages(t, events, AvatarProgressQueued, AvatarProgressDispatching, AvatarProgressSubmitted, AvatarProgressCompleted)
	last := events[len(events)-1]
	if last.Result == nil || last.Result.Provider != "async-provider" || last.Result.Seed != "seed-async" {
		t.Fatalf("completion event missing result: %#v", last)
	}
}

func TestAvatarGeneratorUsesFirstRegisteredProviderAsDefault(t *testing.T) {
	generator := NewAvatarGenerator(AvatarConfig{}, nil)
	fake := &fakeAvatarProvider{name: "registered-default"}
	if err := generator.RegisterProvider(fake); err != nil {
		t.Fatalf("register provider: %v", err)
	}

	result, err := generator.Generate(t.Context(), "plain prompt", "seed-default")
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if result.Provider != "registered-default" || fake.got.Provider != "registered-default" {
		t.Fatalf("registered provider was not used as default: result=%#v request=%#v", result, fake.got)
	}
}

func TestAvatarStylePresetLibrary(t *testing.T) {
	presets := AvatarStylePresets()
	if len(presets) != 5 {
		t.Fatalf("preset count = %d", len(presets))
	}
	for _, want := range []string{"abstract", "anime", "corporate", "pixel-art", "realistic"} {
		if _, ok := AvatarStylePresetByID(want); !ok {
			t.Fatalf("missing style preset %q", want)
		}
	}
	if _, _, err := ExpandAvatarStylePreset("owl", "missing"); err == nil {
		t.Fatalf("expected unknown preset error")
	}
}

func TestAvatarGeneratorLegacyEntrypoints(t *testing.T) {
	generator := NewAvatarGenerator(AvatarConfig{Provider: "legacy-provider"}, nil)
	fake := &fakeAvatarProvider{name: "legacy-provider"}
	if err := generator.RegisterProvider(fake); err != nil {
		t.Fatalf("register provider: %v", err)
	}

	if _, err := generator.Generate(t.Context(), "plain prompt", "plain-seed"); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if fake.got.Prompt != "plain prompt" || fake.got.Seed != "plain-seed" || fake.got.Width != 512 || fake.got.Height != 512 {
		t.Fatalf("legacy Generate request = %#v", fake.got)
	}

	if _, err := generator.GenerateDefault(t.Context(), "Scout", "Observes deployments"); err != nil {
		t.Fatalf("GenerateDefault: %v", err)
	}
	if fake.got.Seed != "Scout" || fake.got.Width != 512 || fake.got.Height != 512 {
		t.Fatalf("legacy GenerateDefault request fields = %#v", fake.got)
	}
	if !strings.Contains(fake.got.Prompt, "Pixel art style avatar") || !strings.Contains(fake.got.Prompt, "Scout") || !strings.Contains(fake.got.Prompt, "Observes deployments") {
		t.Fatalf("GenerateDefault did not apply pixel-art prompt: %q", fake.got.Prompt)
	}
}

func TestAvatarGeneratorValidatesDimensions(t *testing.T) {
	generator := NewAvatarGenerator(AvatarConfig{}, nil)
	fake := &fakeAvatarProvider{name: "dimension-provider"}
	if err := generator.RegisterProvider(fake); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	_, err := generator.GenerateWithSpec(t.Context(), domain.SoulAvatarGenerationSpec{
		Prompt:   "oversized avatar",
		Provider: "dimension-provider",
		Width:    4096,
		Height:   512,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "dimensions") {
		t.Fatalf("expected dimension validation error, got %v", err)
	}

	_, err = generator.GenerateWithSpec(t.Context(), domain.SoulAvatarGenerationSpec{
		Prompt:   "bad step avatar",
		Provider: "dimension-provider",
		Width:    513,
		Height:   512,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "multiples") {
		t.Fatalf("expected dimension step validation error, got %v", err)
	}
}

func TestAvatarGeneratorNoProvidersConfiguredFailsClosed(t *testing.T) {
	generator := NewAvatarGenerator(AvatarConfig{}, nil)
	if names := generator.ProviderNames(); len(names) != 0 {
		t.Fatalf("ProviderNames() = %v, want no advertised providers", names)
	}
	if infos := generator.ProviderInfos(); len(infos) != 0 {
		t.Fatalf("ProviderInfos() = %#v, want no placeholder providers", infos)
	}

	_, err := generator.GenerateWithSpec(t.Context(), domain.SoulAvatarGenerationSpec{
		Prompt: "owl analyst",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "no avatar providers configured") {
		t.Fatalf("GenerateWithSpec() error = %v, want no providers configured", err)
	}
}

func TestAvatarGeneratorUnconfiguredProviderFailsBeforeRequest(t *testing.T) {
	generator := NewAvatarGenerator(AvatarConfig{Provider: AvatarProviderFal}, nil)
	_, err := generator.GenerateWithSpec(t.Context(), domain.SoulAvatarGenerationSpec{
		Prompt: "owl analyst",
	}, nil)
	if err == nil || !strings.Contains(err.Error(), `avatar provider "fal" is not registered`) {
		t.Fatalf("GenerateWithSpec() error = %v, want unregistered provider", err)
	}
}

func assertProgressStages(t *testing.T, events []AvatarProgressEvent, want ...AvatarProgressStage) {
	t.Helper()
	if len(events) != len(want) {
		t.Fatalf("progress events length mismatch: got %#v want stages %v", events, want)
	}
	terminalCount := 0
	for i, stage := range want {
		if events[i].Stage != stage {
			t.Fatalf("progress stage[%d] = %q, want %q; all events=%#v", i, events[i].Stage, stage, events)
		}
		if events[i].Stage == AvatarProgressCompleted || events[i].Stage == AvatarProgressFailed {
			terminalCount++
		}
	}
	if terminalCount != 1 {
		t.Fatalf("expected exactly one terminal progress event, got %d in %#v", terminalCount, events)
	}
}

func serverURL(r *http.Request) string {
	return "http://" + r.Host
}
