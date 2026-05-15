# SoulFactory Agent Customization Slice — Plan

> **Status**: Ready for implementation planning  
> **Date**: 2026-05-15  
> **Scope**: Voice generation, avatar generation, memory tuning, personality construction, and live update/redeployment  
> **Prerequisite**: SoulFactory Nostr Agent Lifecycle plan (2026-05-14) runtime control foundation

---

## Goal

Extend SoulFactory's Nostr-native agent management with full UX and protocol support for detailed agent customization:

1. **Avatar generation** — AI-generated agent portraits with style/prompt control, preview, and live regeneration
2. **Voice configuration** — TTS provider/persona binding, voice sample preview, and runtime voice switching
3. **Memory tuning** — Embedding provider selection, search config, memory strategy, and live reindexing
4. **Personality construction** — System prompt authoring, persona traits, identity theming, and character consistency
5. **Live updates** — Hot-reload configuration changes without full reprovisioning; incremental redeployment

All customization flows through Nostr events (`31952` drafts, `1950` actions, `38384` runtime control), with no REST control APIs. The UX should allow operators to iterate on agent personality and capabilities in real-time while the agent remains active.

---

## Background

### Existing Infrastructure

**Bahia domain (`internal/domain/soul.go`)**:
- `SoulAssetRefs`: `avatar_ref`, `voice_ref` — currently simple string references
- `SoulIdentitySpec`: `name`, `purpose`, `tier`, `nip05`
- `SoulDraftContent`: `brief`, `soul_md`, `identity_md`, `avatar_prompt`
- `SoulGeneratorOutput`: `personality_tags` from LLM generation
- Eight provisioning steps include `StepAvatar`, `StepMemory`

**Bahia avatar generation (`internal/adapters/llm/avatar.go`)**:
- `AvatarGenerator` using FLUX/ComfyUI via Lemmy endpoint
- `GenerateDefault()` builds pixel-art style robot prompts
- Returns `AvatarResult` with `ImageData`, `ContentType`, `Seed`
- Fixed 512x512 dimensions, simple prompt templating

**OpenClaw TTS configuration (`src/config/types.tts.ts`)**:
- `TtsConfig`: provider, persona, personas map, auto mode, model overrides
- `TtsPersonaConfig`: label, description, provider pref, fallback policy, prompt config
- `TtsPersonaPromptConfig`: profile, scene, style, accent, pacing, constraints
- `TtsProviderConfigMap`: provider-specific settings (ElevenLabs, Azure Speech, etc.)
- `TtsModelOverrideConfig`: allow model to override voice/settings dynamically

**OpenClaw agent identity (`src/config/types.base.ts`)**:
- `IdentityConfig`: name, theme, emoji, avatar path
- Minimal — no persona/character depth beyond name

**OpenClaw agent defaults (`src/config/types.agent-defaults.ts`)**:
- `systemPromptOverride`: full system prompt replacement
- `promptOverlays`: personality overlays for GPT-5-family models
- `memorySearch`: vector search config
- `compaction`: custom instructions for persona continuity
- `Gpt5PromptOverlayConfig`: personality mode (friendly/on/off)

**OpenClaw memory extensions**:
- `memory-core`: embedding providers, search runtime, index management
- `memory-lancedb`: LanceDB vector store backend
- `memory-wiki`: Obsidian/wiki-style memory organization
- `MemorySearchConfig`: top-k, score threshold, reranking

**Bahia web UX (`web/src/routes/souls/new/+page.svelte`)**:
- Simple text inputs for `avatarRef`, `voiceRef`
- No generation UI, no preview, no style selection
- No memory or personality configuration panels

### Gaps to Fill

1. **Avatar generation UX**: No prompt editing, no style presets, no preview/regeneration, no seed control
2. **Voice UX**: No provider selection, no persona editor, no sample playback, no live switching
3. **Memory UX**: No embedding config, no search tuning, no memory strategy selection
4. **Personality UX**: No system prompt editor, no trait/persona builder, no character sheet
5. **Live updates**: No hot-reload path, full reprovision required for any change
6. **Draft schema**: Asset/voice/memory/personality specs not rich enough for full customization

---

## Approach

### Extended Draft Schema (`31952`)

Expand `SoulDraftContent` with nested configuration objects that mirror OpenClaw's runtime config shapes while remaining Nostr-portable:

```json
{
  "schema": "soulfactory-draft/v2",
  "brief": "...",
  "identity": {
    "name": "Scout",
    "purpose": "Research assistant",
    "tier": "standard",
    "nip05": "scout@bahia.ai",
    "theme": "warm",
    "emoji": "🔍"
  },
  "persona": {
    "traits": ["curious", "thorough", "patient"],
    "style": "conversational",
    "tone": "friendly professional",
    "constraints": ["Always cite sources", "Never speculate beyond data"],
    "system_prompt_sections": {
      "role": "You are Scout, a research assistant...",
      "guidelines": "When answering questions...",
      "red_lines": "Never fabricate citations..."
    }
  },
  "avatar": {
    "generation": {
      "prompt": "Pixel art owl with magnifying glass...",
      "style_preset": "pixel-art",
      "seed": "scout-v1",
      "width": 512,
      "height": 512,
      "provider": "flux-comfyui"
    },
    "uploaded_ref": null,
    "generated_ref": "blossom:abc123...",
    "current": "generated"
  },
  "voice": {
    "provider": "elevenlabs",
    "persona_id": "scout-voice",
    "persona": {
      "label": "Scout Voice",
      "profile": "Young professional researcher",
      "style": "articulate",
      "accent": "neutral american",
      "pacing": "measured",
      "elevenlabs": {
        "voice_id": "pNInz6obpgDQGcFmaJgB",
        "stability": 0.7,
        "similarity_boost": 0.8,
        "style": 0.5
      }
    },
    "auto_mode": "tagged",
    "sample_text": "Hello, I'm Scout. Let me help you find what you're looking for."
  },
  "memory": {
    "embedding_provider": "voyage",
    "embedding_model": "voyage-3",
    "search": {
      "top_k": 10,
      "score_threshold": 0.7,
      "rerank": true,
      "rerank_model": "cohere-rerank-v3"
    },
    "strategy": "session-aware",
    "auto_index": true,
    "retention_days": 90
  },
  "runtime": { ... },
  "permissions": { ... },
  "relay_policy": { ... },
  "workspace": { ... }
}
```

### New Protocol Elements

**Extended `1950` action types**:
- `update-avatar`: regenerate or replace avatar
- `update-voice`: switch voice provider/persona
- `update-memory`: reconfigure memory/reindex
- `update-persona`: modify personality/system prompt
- `hot-reload`: apply draft changes without full reprovision

**Runtime control methods (`38384`)**:
- `soulfactory.avatar.generate`: trigger avatar generation on runtime
- `soulfactory.avatar.set`: set avatar from uploaded/existing ref
- `soulfactory.voice.configure`: update TTS config
- `soulfactory.voice.sample`: generate voice sample
- `soulfactory.memory.configure`: update memory config
- `soulfactory.memory.reindex`: trigger memory reindex
- `soulfactory.persona.update`: hot-reload persona/system prompt
- `soulfactory.config.reload`: reload full agent config from draft

### Backend Components

**Bahia domain extensions**:
```go
// Extended persona specification
type SoulPersonaSpec struct {
    Traits                []string                       `json:"traits,omitempty"`
    Style                 string                         `json:"style,omitempty"`
    Tone                  string                         `json:"tone,omitempty"`
    Constraints           []string                       `json:"constraints,omitempty"`
    SystemPromptSections  map[string]string              `json:"system_prompt_sections,omitempty"`
}

// Extended avatar specification  
type SoulAvatarSpec struct {
    Generation    *SoulAvatarGenerationSpec `json:"generation,omitempty"`
    UploadedRef   string                    `json:"uploaded_ref,omitempty"`
    GeneratedRef  string                    `json:"generated_ref,omitempty"`
    Current       string                    `json:"current,omitempty"` // "generated" | "uploaded"
}

type SoulAvatarGenerationSpec struct {
    Prompt      string `json:"prompt,omitempty"`
    StylePreset string `json:"style_preset,omitempty"`
    Seed        string `json:"seed,omitempty"`
    Width       int    `json:"width,omitempty"`
    Height      int    `json:"height,omitempty"`
    Provider    string `json:"provider,omitempty"`
}

// Extended voice specification
type SoulVoiceSpec struct {
    Provider   string                    `json:"provider,omitempty"`
    PersonaID  string                    `json:"persona_id,omitempty"`
    Persona    *SoulVoicePersonaSpec     `json:"persona,omitempty"`
    AutoMode   string                    `json:"auto_mode,omitempty"`
    SampleText string                    `json:"sample_text,omitempty"`
    Providers  map[string]map[string]any `json:"providers,omitempty"`
}

type SoulVoicePersonaSpec struct {
    Label   string   `json:"label,omitempty"`
    Profile string   `json:"profile,omitempty"`
    Style   string   `json:"style,omitempty"`
    Accent  string   `json:"accent,omitempty"`
    Pacing  string   `json:"pacing,omitempty"`
}

// Extended memory specification
type SoulMemorySpec struct {
    EmbeddingProvider string                 `json:"embedding_provider,omitempty"`
    EmbeddingModel    string                 `json:"embedding_model,omitempty"`
    Search            *SoulMemorySearchSpec  `json:"search,omitempty"`
    Strategy          string                 `json:"strategy,omitempty"`
    AutoIndex         bool                   `json:"auto_index,omitempty"`
    RetentionDays     int                    `json:"retention_days,omitempty"`
}

type SoulMemorySearchSpec struct {
    TopK           int     `json:"top_k,omitempty"`
    ScoreThreshold float64 `json:"score_threshold,omitempty"`
    Rerank         bool    `json:"rerank,omitempty"`
    RerankModel    string  `json:"rerank_model,omitempty"`
}
```

**Avatar generation service upgrades**:
- Multiple provider support (FLUX/ComfyUI, Fal.ai, Replicate)
- Style presets library (pixel-art, anime, realistic, abstract, corporate)
- Seed-based reproducibility and variation control
- Async generation with progress events
- Blossom blob storage integration for generated images

**Voice configuration service**:
- Provider registry (ElevenLabs, Azure Speech, OpenAI TTS, local CLI)
- Persona template library
- Sample generation with preview audio
- Voice cloning workflow (for supported providers)
- Live voice switching via runtime control

**Memory configuration service**:
- Embedding provider registry (Voyage, OpenAI, Cohere, local)
- Reindex orchestration with progress tracking
- Memory strategy templates (session-aware, long-term, ephemeral)
- Search parameter optimization suggestions

### UX Components

**Avatar Studio panel**:
- Prompt editor with suggestions
- Style preset gallery
- Seed input with randomize button
- Live preview with regenerate
- Upload alternative with crop/resize
- History of generated variants
- Blossom/blob reference display

**Voice Studio panel**:
- Provider selector with available voices
- Persona editor (profile, style, accent, pacing)
- Provider-specific settings (ElevenLabs stability/similarity)
- Sample text input with play button
- Voice comparison tool
- Auto-mode selector (off/always/tagged)

**Memory Configuration panel**:
- Embedding provider selector
- Model selection (per-provider)
- Search parameter sliders (top-k, threshold)
- Reranking toggle with model selection
- Strategy selector with descriptions
- Retention policy configuration
- Reindex trigger with progress

**Personality Builder panel**:
- Traits tag editor with suggestions
- Style/tone dropdowns
- Constraints list editor
- System prompt section editors (role, guidelines, red lines)
- Character consistency preview
- Import/export personality templates

**Live Update controls**:
- Draft diff viewer (current vs. proposed)
- Selective update checkboxes (avatar, voice, memory, persona)
- Hot-reload button (no reprovision)
- Full redeploy button (with confirmation)
- Update progress tracker
- Rollback to previous version

---

## Work Items

### Epic 1: Extended Draft Schema and Domain Model

**1.1 Extend SoulFactory domain types**
- Add `SoulPersonaSpec`, `SoulAvatarSpec`, `SoulVoiceSpec`, `SoulMemorySpec` to `soul.go`
- Update `SoulDraftContent` with new nested specs
- Add schema versioning (`soulfactory-draft/v2`) with v1 migration
- Add spec hash computation including new fields
- Tests: parsing, serialization, migration, hash stability

**1.2 Extend event codec for new specs**
- Update `event_codec.go` to parse/build extended draft content
- Add validation for provider/model references
- Support partial draft updates (merge semantics)
- Tests: round-trip all spec types, partial updates

**1.3 Update web client types and stores**
- Mirror Go types in `souls.svelte.js`
- Add draft content builders for each spec section
- Update `publishSoulDraft` for v2 schema
- Add diff computation for draft changes

### Epic 2: Avatar Generation Infrastructure

**2.1 Multi-provider avatar generation service**
- Refactor `AvatarGenerator` to provider-based architecture
- Add provider registry (flux-comfyui, fal, replicate)
- Implement async generation with progress events
- Add style preset library with prompt templates
- Tests: provider dispatch, preset expansion, progress events

**2.2 Blossom blob storage integration**
- Add Blossom upload for generated avatars
- Return blob refs (`blossom:hash`) as avatar references
- Support fallback to direct URL refs
- Add blob retrieval for preview
- Tests: upload, retrieval, ref resolution

**2.3 Avatar runtime control methods**
- Add `soulfactory.avatar.generate` handler in OpenClaw bridge
- Add `soulfactory.avatar.set` handler for existing refs
- Publish `38386` results with new avatar ref
- Update `31951` soul read model with new avatar
- Tests: generate flow, set flow, read model update

**2.4 Avatar Studio UX component**
- Create `AvatarStudio.svelte` component
- Prompt editor with character count
- Style preset gallery (grid with previews)
- Generation trigger with loading state
- Preview panel with zoom
- Upload alternative with image picker
- History carousel of variants
- Wire to draft publish flow

### Epic 3: Voice Configuration Infrastructure

**3.1 Voice provider registry service**
- Create provider registry in Bahia
- Enumerate available voices per provider
- Cache provider capabilities
- Add voice metadata (language, gender, style tags)
- Tests: registry population, capability queries

**3.2 Voice persona configuration service**
- Map `SoulVoiceSpec` to OpenClaw `TtsPersonaConfig`
- Support provider-specific persona bindings
- Generate persona preview samples
- Tests: spec mapping, sample generation

**3.3 Voice runtime control methods**
- Add `soulfactory.voice.configure` handler in OpenClaw bridge
- Add `soulfactory.voice.sample` handler for preview generation
- Hot-reload TTS config without restart
- Publish results with sample audio refs
- Tests: configure flow, sample flow, hot-reload

**3.4 Voice Studio UX component**
- Create `VoiceStudio.svelte` component
- Provider selector with voice list
- Persona editor form (profile, style, accent, pacing)
- Provider-specific settings panel
- Sample text input with play button
- Auto-mode selector
- Wire to draft publish flow

### Epic 4: Memory Configuration Infrastructure

**4.1 Memory configuration service**
- Create memory config mapping service in Bahia
- Map `SoulMemorySpec` to OpenClaw memory config
- Support embedding provider selection
- Add search parameter validation
- Tests: spec mapping, validation

**4.2 Memory reindex orchestration**
- Add reindex trigger via runtime control
- Track reindex progress via `6950` status events
- Support incremental vs full reindex
- Add retention policy enforcement
- Tests: reindex trigger, progress tracking

**4.3 Memory runtime control methods**
- Add `soulfactory.memory.configure` handler in OpenClaw bridge
- Add `soulfactory.memory.reindex` handler
- Hot-reload memory search config
- Publish results with reindex stats
- Tests: configure flow, reindex flow

**4.4 Memory Configuration UX component**
- Create `MemoryConfig.svelte` component
- Embedding provider selector
- Model selection dropdown
- Search parameter controls (sliders/inputs)
- Reranking toggle with model selection
- Strategy selector with descriptions
- Reindex trigger with progress bar
- Wire to draft publish flow

### Epic 5: Personality Construction Infrastructure

**5.1 Personality spec service**
- Create personality mapping service in Bahia
- Map `SoulPersonaSpec` to OpenClaw system prompt sections
- Build composite system prompt from sections
- Add trait/constraint validation
- Tests: spec mapping, prompt assembly

**5.2 System prompt generation**
- Integrate with existing LLM generator for prompt refinement
- Support section-based prompt composition
- Add persona consistency checking
- Tests: generation, composition, consistency

**5.3 Persona runtime control methods**
- Add `soulfactory.persona.update` handler in OpenClaw bridge
- Hot-reload system prompt sections
- Update identity config (name, theme, emoji)
- Preserve persona across compaction
- Tests: update flow, hot-reload, compaction

**5.4 Personality Builder UX component**
- Create `PersonalityBuilder.svelte` component
- Traits tag editor with autocomplete
- Style/tone selectors
- Constraints list editor
- System prompt section editors (collapsible)
- Character preview panel
- Import/export buttons
- Wire to draft publish flow

### Epic 6: Live Update and Hot-Reload Infrastructure

**6.1 Hot-reload action handler**
- Add `hot-reload` action type to lifecycle handler
- Diff current vs proposed draft
- Dispatch selective runtime control requests
- Track hot-reload progress via `6950`
- Publish `7950` result with applied changes
- Tests: diff computation, selective dispatch, progress

**6.2 Config reload runtime method**
- Add `soulfactory.config.reload` handler in OpenClaw bridge
- Accept partial config patches
- Apply changes without session restart
- Report what was reloaded
- Tests: partial reload, full reload, no-restart

**6.3 Rollback support**
- Store previous draft event refs in `31951`
- Add `rollback` action type
- Restore previous draft and trigger hot-reload
- Tests: rollback flow, restore verification

**6.4 Live Update UX component**
- Create `LiveUpdate.svelte` component
- Draft diff viewer (side-by-side)
- Section checkboxes (avatar, voice, memory, persona)
- Hot-reload button with progress
- Full redeploy button with confirmation
- Rollback button with version selector
- Status display (current vs pending)
- Wire to action publish flow

### Epic 7: Bahia Web Integration

**7.1 Soul creation wizard upgrade**
- Add tabbed panels for each customization area
- Integrate Avatar Studio, Voice Studio, Memory Config, Personality Builder
- Progressive disclosure (basic → advanced)
- Save draft at each step
- Preview mode before provision

**7.2 Soul detail/edit page upgrade**
- Add customization panels to existing soul detail
- Show current config with edit buttons
- Live update controls
- Version history sidebar
- Audit log of changes

**7.3 Template customization presets**
- Allow templates to include default customization specs
- Preset library for common configurations
- Clone from existing souls

**7.4 Cross-cutting UX polish**
- Loading states for all async operations
- Error handling with retry
- Validation feedback
- Responsive layout
- Keyboard navigation

### Epic 8: OpenClaw Bridge Extensions

**8.1 Customization method handlers**
- Implement all `soulfactory.avatar.*` methods
- Implement all `soulfactory.voice.*` methods
- Implement all `soulfactory.memory.*` methods
- Implement all `soulfactory.persona.*` methods
- Implement `soulfactory.config.reload`

**8.2 Hot-reload integration**
- Wire TTS config hot-reload
- Wire memory config hot-reload
- Wire system prompt hot-reload
- Wire identity config hot-reload

**8.3 Capability advertisement**
- Update `30317` to advertise customization methods
- Include supported providers/features
- Version capability schema

### Epic 9: Metiq Bridge Extensions

**9.1 Customization method handlers**
- Mirror OpenClaw customization handlers in Metiq
- Use existing control bus infrastructure
- Adapt to Go runtime config model

**9.2 Capability advertisement**
- Update Metiq `30317` for customization methods
- Feature parity indication

### Epic 10: Testing and Verification

**10.1 Unit tests**
- Domain type parsing/serialization
- Event codec round-trips
- Spec validation
- Provider dispatch

**10.2 Integration tests**
- End-to-end customization flows
- Hot-reload scenarios
- Rollback scenarios
- Multi-runtime parity

**10.3 PSTF verification**
- Add `SOUL_FACTORY_AGENT_CUSTOMIZATION` feature
- Acceptance criteria for each customization type
- Live update verification
- No-REST boundary enforcement

---

## Acceptance Criteria

- [ ] `31952` drafts support full avatar/voice/memory/persona specs with v2 schema
- [ ] Avatar Studio allows prompt editing, style presets, preview, and regeneration
- [ ] Generated avatars are stored in Blossom and referenced by blob hash
- [ ] Voice Studio allows provider/persona selection with sample playback
- [ ] Memory Configuration allows embedding/search tuning with reindex trigger
- [ ] Personality Builder allows trait/constraint/system-prompt editing
- [ ] Hot-reload applies selective changes without full reprovisioning
- [ ] All customization flows use Nostr events only (no REST)
- [ ] OpenClaw and Metiq bridges support all customization runtime methods
- [ ] `31951` read model reflects current customization state
- [ ] Live updates are observable via `6950` progress and `7950` results
- [ ] Rollback restores previous configuration and triggers hot-reload
- [ ] UI provides clear feedback for async operations and errors
- [ ] Customization presets can be saved and applied to new souls

---

## Risks and Mitigations

| Risk | Mitigation |
|------|------------|
| Provider availability varies | Capability-gate UI on `30317` advertised features; graceful degradation |
| Hot-reload breaks running sessions | Implement reload-safe config application; test with active sessions |
| Avatar generation is slow | Async generation with progress; allow upload as fast alternative |
| Voice sample generation costs | Rate limit samples; cache generated samples |
| Memory reindex is expensive | Support incremental reindex; background processing with progress |
| System prompt drift after compaction | Include persona in compaction custom instructions |
| Draft schema v1/v2 incompatibility | Additive schema; v1 fields remain valid; explicit migration |
| Too many concurrent customization requests | Queue at runtime; throttle at SoulFactory |
| Provider-specific config complexity | Abstract common patterns; expose provider-specific only when needed |

---

## Dependencies

- SoulFactory Nostr Agent Lifecycle plan (2026-05-14) must be complete:
  - Resilient relay bus
  - Runtime control protocol
  - OpenClaw/Metiq bridges with capability announcement
  - Draft-backed provisioning
- Blossom blob storage for avatar images
- Voice provider API keys (ElevenLabs, Azure, etc.)
- Embedding provider API keys (Voyage, OpenAI, Cohere)

---

## Open Questions

1. **Voice cloning workflow**: Should SoulFactory support custom voice cloning, or only pre-existing voices?
2. **Avatar animation**: Should avatars support animated variants (GIF, video) for richer presence?
3. **Persona inheritance**: Should child/spawned agents inherit parent persona with modifications?
4. **Memory sharing**: Should multiple agents share memory collections, or always isolated?
5. **Customization marketplace**: Should there be a public repository of persona/style presets?

---

## References

- `docs/plans/soulfactory-nostr-agent-lifecycle-2026-05-14.md`
- `docs/soulfactory-runtime-control.md`
- `internal/domain/soul.go`
- `internal/adapters/llm/avatar.go`
- `openclaw/src/config/types.tts.ts`
- `openclaw/src/config/types.agent-defaults.ts`
- `openclaw/src/config/types.base.ts`
- `openclaw/extensions/memory-core/`
- `openclaw/extensions/image-generation-core/`
- `openclaw/extensions/elevenlabs/`
- `web/src/routes/souls/new/+page.svelte`
- `web/src/lib/stores/souls.svelte.js`
