# SOUL_FACTORY_AGENT_CUSTOMIZATION

## Feature name

SOUL_FACTORY_AGENT_CUSTOMIZATION

## Description

Full agent customization via Nostr events.

## Dependencies

- SOUL_FACTORY_PROVISIONING_TRACKING

## Protocol boundary

All customization state changes MUST flow through Nostr events and event-driven handlers:

- `31952` — Soul draft / editable desired customization spec
- `1950` — operator lifecycle/action request that references the desired draft/update intent
- `38384` — runtime control request for applying customization to a selected runtime

REST endpoints may read or render customization state, but MUST NOT mutate avatar, voice, memory, persona, or live-update configuration. Runtime bridges MUST publish/verify relay responses through the Nostr control flow and MUST NOT introduce REST mutation fallbacks for customization.

## Acceptance criteria

### Avatar customization

- Avatar generation is represented in the `31952` draft customization spec with provider, prompt, style/preset, and generation metadata sufficient to build a runtime control request.
- Avatar upload/current asset references are preserved as draft state and can be applied to runtime identity without dropping legacy `assets.avatar_ref` compatibility fields.
- Avatar provider selection is validated before event emission or parsing; unsupported provider/style combinations fail deterministically.
- Avatar runtime changes are applied via `38384` customization/config reload methods, not REST mutation endpoints.

### Voice customization

- Voice provider config is represented in the draft with provider/model/voice identifiers and provider-specific settings that round-trip through the event codec.
- Voice persona settings are represented separately from provider transport settings so persona changes can hot-reload without replacing the selected voice asset.
- Voice sample generation is expressed as a runtime customization action/control request and returns evidence through the Nostr control result path.
- Unsupported voice providers/models are rejected before runtime dispatch.

### Memory customization

- Memory embedding config captures provider, model, strategy, retention, and indexing behavior in a portable `soulfactory-memory-config/v1` contract.
- Memory search params capture top-k, score threshold, reranking intent, and session-memory behavior and map to OpenClaw `memorySearch` configuration.
- Memory reindex is represented as a Nostr runtime control action rather than a polling or REST mutation flow.
- Invalid provider, strategy, top-k, score threshold, retention, or rerank combinations fail validation before `38384` dispatch.

### Persona customization

- Persona traits, style, tone, and constraints are represented in the draft and normalize into deterministic OpenClaw prompt sections.
- System prompt sections support role, guidelines, and red-lines content with stable ordering and omission of empty sections.
- Persona updates build `soulfactory.persona.update` runtime params using the `soulfactory-persona/v1` contract.
- Malformed persona fields, duplicate traits, unsupported sections, and disallowed control characters are rejected before runtime dispatch.

### Live update / hot reload

- Customization changes hot-reload through Nostr runtime control/config reload methods without reprovisioning the agent.
- Hot reload MUST NOT delete or respawn the active managed session after initial provisioning.
- Partial draft updates merge without dropping existing nested customization fields.
- Runtime state sync clears stale optional customization config when a resolved spec removes it.

## Coverage artifacts

- `coverage-summary.json` in this directory summarizes PSTF acceptance coverage and links the relevant unit/contract/integration evidence.
- Integration evidence is linked in `coverage-summary.json` under `integration_tests` and in `test_matrix.json` (`SFAC-T-014` and related runtime control tests).
