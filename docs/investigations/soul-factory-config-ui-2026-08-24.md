# Investigation: Soul Factory — Agent Config Updating, Fleet-wide Settings, Web UI Feature Completeness

## Summary
Soul Factory has a rich desired-state schema and a real Nostr-native provisioning spine, but (a) ordinary web edits never reach the running agent (`handleUpdate` updates read-model lineage only), (b) there is no fleet-wide OpenClaw config control plane via either Nostr or the web UI (the wrapper generates only `plugins` + `channels.nostr` per agent), and (c) many new/edit UI controls are draft-only, adapter-dependent, or display-only. Field-by-field wiring audit and a remediation plan are below.

## Questions
1. Does the current implementation allow quick/easy updating of agent config (incl. `SOUL.md`) through the web UI?
2. Does it support fleet-wide agent settings like `fleet-planning/docs/projects/openclaw-nostr/openclaw.json` (full OpenClaw config: auth, models, agents.defaults, bindings, channels.nostr incl. nip29 groups, gateway, mcp, skills, plugins, tools, diagnostics) via BOTH Nostr events and web UI?
3. Are all Web UI features in the new/edit agent (soul) interface actually wired to the backend, or are some display-only?

## Background / Prior Research
- Fleet-wide reference config: `fleet-planning/docs/projects/openclaw-nostr/openclaw.json` (469 lines) — templated OpenClaw agent config with `${AGENT_NAME}`-style placeholders. Key sections: logging, auth.profiles, models.providers, agents.defaults (model, contextPruning, heartbeat, memorySearch), bindings, messages, commands, session, hooks, channels.nostr (dmPolicy/allowFrom/profile/nostr_channels nip29+communikey), gateway, mcp.servers, skills.entries, plugins, tools.profile, diagnostics.
- Bahia areas of interest: `internal/soulfactory/` (+ `openclawcontrol`, `saga`), `cmd/openclaw-soulfactory-control`, `cmd/openclaw-soulfactory-sidecar`, `web/src/routes/souls/` (new, [id]/edit), `web/src/lib/components/souls/`, pstf features SOULFACTORY_* / SOUL_FACTORY_* / OPENCLAW_*.

## Investigator Findings

### Executive answers

1. **Create/provision is Nostr-native and does reach a running OpenClaw deployment, but ordinary web editing does not.** The new-soul wizard can persist structured identity/personality/avatar/voice/memory/runtime/permission/relay/workspace data into a signed kind `31952` draft and submit kind `5950`; the reactor ultimately sends addressed kind `38384` runtime control to the sidecar. The owned OpenClaw wrapper provisions a per-agent container and writes `IDENTITY.md`, `SOUL.md`, `AGENTS.md`, `MEMORY.md`, and a minimal `openclaw.json`. In contrast, the edit page publishes a new `31952` plus kind `1950` action=`update`, but the lifecycle handler explicitly changes only the `31951` read-model's draft/spec lineage and **does not invoke any runtime adapter**. Thus the edit UI is not a quick/easy live agent-config or `SOUL.md` editor.
2. **There is no fleet-wide full-OpenClaw-config control plane in either Nostr or the web UI.** Soul Factory kinds and draft/config-reload schemas are agent-scoped and allow only Soul Factory sections. The owned wrapper generates only `plugins` and `channels.nostr`, with model, bindings, and required plugins supplied by sidecar environment/config. It does not load or merge the fleet `openclaw.json` template.
3. **The new page has a real end-to-end provisioning spine, but many customization controls stop at draft/domain serialization or depend on optional/custom runtimes. The edit page's visible fields are currently draft/event-only with respect to the running agent.** Several buttons are presentation/local-only, and there are no general LLM model/provider, skills, channel/group, gateway, MCP, plugin, tools-profile, or diagnostics controls.

### 1. Full update path and effective latency

#### New soul: UI -> Nostr -> reactor -> sidecar -> OpenClaw

1. The wizard assembles a `soulfactory-draft/v2` document with identity, persona, avatar, voice, memory, runtime, permissions, relay policy, workspace, and assets (`web/src/routes/souls/new/+page.svelte:280-330`). Leaving a panel or pressing Save publishes a checkpoint (`web/src/routes/souls/new/+page.svelte:338-372`).
2. `publishSoulDraft` normalizes/hashes the document, signs a parameterized-replaceable kind `31952`, publishes it directly to relays, verifies at least one relay acceptance, and updates the local replaceable-event view (`web/src/lib/stores/souls.svelte.js:883-920`). There is no REST mutation.
3. Provision saves another draft and publishes kind `5950` with draft coordinate/event ID, spec hash, runtime target/pubkey, and capability reference (`web/src/routes/souls/new/+page.svelte:423-479`; `web/src/lib/stores/souls.svelte.js:1019-1091`).
4. The reactor subscribes to `5950` and dispatches provisioning (`internal/soulfactory/reactor.go:172-187,266-272`). The resolved draft is converted into runtime params containing every Soul Factory section (`internal/soulfactory/provisioning_resolution.go:207-248`; `internal/soulfactory/runtime_adapter.go:647-675`) and the runtime adapter emits addressed kind `38384 soulfactory.provision` (`internal/soulfactory/provisioner_full.go:839-845`).
5. The OpenClaw sidecar accepts only trusted-controller, correctly addressed control events and delegates the invocation JSON to the configured local command (`cmd/openclaw-soulfactory-sidecar/main.go:47-68,74-127`; `internal/soulfactory/openclaw_sidecar.go:254-266,472-480`).
6. The owned wrapper writes per-agent markdown (`internal/soulfactory/openclawcontrol/control.go:878-909`), renders a dedicated minimal config (`internal/soulfactory/openclawcontrol/control.go:695-739`), runs per-agent Docker Compose, and waits up to 120 seconds for health (`internal/soulfactory/openclawcontrol/runtime_orchestrator.go:149-179`). It then runs `openclaw agents add --model ...`, `agents set-identity`, and `agents bind` (`internal/soulfactory/openclawcontrol/control.go:919-942`).
7. Progress/terminal/read-model truth is published as `6950`, `7950`, then replaceable `31951` (`internal/soulfactory/event_codec.go:745-796,819-847`).

**Latency/steps:** initial application is an eight-stage provisioning saga (generation/draft resolution, Signet, avatar, profile, Qdrant, memory, workspace, deploy) before runtime readiness (`internal/soulfactory/provisioner_full.go:180-509`), plus Compose's 120-second health ceiling. It is not a simple live config write.

#### What `SOUL.md`/personality actually becomes

- The new UI has **no raw `SOUL.md` or `soul_md` editor** (no occurrence in either form/component tree); it exposes a structured personality builder.
- Provisioning passes the structured persona to the wrapper. The wrapper generates `SOUL.md` from agent/soul IDs, spec hash, name, purpose, and a JSON rendering of persona; `IDENTITY.md` is generated from the identity map (`internal/soulfactory/openclawcontrol/control.go:1357-1380`). These files are bind-mounted into the running per-agent gateway (`internal/soulfactory/openclawcontrol/runtime_orchestrator.go:266-279`).
- A lower-level `soulfactory.persona.update` method can write `.openclaw/persona.json`, replace `SOUL.md` and `AGENTS.md` with a composed system prompt, and record `agent_defaults_patch`; however, it only records that patch in persona/provenance metadata and warns that live runtime hot reload is not confirmed (`internal/soulfactory/openclawcontrol/control.go:973-1024`). It does not merge the patch into `openclaw.json`.
- A lower-level `soulfactory.update` wrapper also exists: it resolves merge/replace, rewrites `IDENTITY.md`, `SOUL.md`, `AGENTS.md`, provenance, and calls `agents set-identity` (`internal/soulfactory/openclawcontrol/control.go:245-303`). That is a file/identity update, not full OpenClaw config regeneration/reconciliation.

#### Edit soul: desired state only, not runtime application

- Edit loads `31951` and its referenced `31952`, builds a reduced `soulfactory-draft/v1`, publishes it, then emits kind `1950` action=`update` with `resolved_spec` and `update_mode=replace` (`web/src/routes/souls/[id]/edit/+page.svelte:90-179`; `web/src/lib/stores/souls.svelte.js:962-1017`).
- The backend contains an explicit implementation comment: `handleUpdate` “records additive lifecycle customization refs without invoking runtime adapters.” It only assigns `DraftRef`, `PreviousSpecHash`, and `SpecHash`, then reports `updated: true` (`internal/soulfactory/lifecycle_handler.go:862-894`).
- Therefore a successful `7950` for this action proves workflow completion/read-model lineage, **not that any visible edit was applied to OpenClaw**. The existing lower-level wrapper update method is unreachable from this normal `1950 update` handler.

A real `1950 hot-reload` path exists separately. It diffs only avatar, voice, memory, and persona/identity, emits section-specific `38384` calls, and rolls back on runtime failure (`internal/soulfactory/lifecycle_handler.go:273-403,498-589`). But the only web component that publishes hot-reload/config-reload is `web/src/lib/components/LiveUpdate.svelte:109-186`, and a source-wide search finds no import/mount of that component. Moreover the default command driver advertises only provision, update, persona-update, and revoke—not config reload (`internal/soulfactory/openclaw_sidecar.go:48-55`). So this is not an available standard web workflow and requires a custom capable driver.

### 2. Fleet-wide settings: not implemented via either Nostr or UI

#### Event/API scope

- Canonical kinds are agent/soul scoped: `1950` action, `5950` provision, `6950` progress, `7950` result, `30317` capability, `38384/38386` runtime control/result, and `31950/31951/31952` template/read-model/draft (`internal/kinds/kinds.go:191-201`; `internal/domain/soul.go:17-34`).
- The web app publishes mutations directly to Nostr. Kind policy explicitly classifies the Soul Factory family as open interop exchanged directly by the web app/reactors rather than legacy Bahia control-plane projections (`internal/kinds/policy.go:42-46,82-96`); no Soul Factory mutation projector/decoder was found under `internal/adapters/nostr`. The only Soul Factory REST endpoint is read-only `GET /soulfactory/runtimes`, exposing enabled runtime names (`internal/api/handlers/soulfactory.go:9-38`; `internal/api/router/router.go:346-350`).
- Even the generic runtime reload contract allowlists only `identity`, `persona`, `avatar`, `voice`, `memory`, `runtime`, `permissions`, `relay_policy`, `workspace`, and `assets` (`internal/soulfactory/config_reload.go:12-75`). There is no fleet/global target or schema.

#### Actual generated/merged OpenClaw config

The owned wrapper creates a fresh object with only:

```json
{
  "plugins": { "allow": [], "entries": {} },
  "channels": { "nostr": {} }
}
```

It forces Nostr enabled/defaultAccount, uses `relay_policy.control` as `channels.nostr.relays` only if relays are absent, and injects file-backed NIP-46 secrets (`internal/soulfactory/openclawcontrol/control.go:695-739`). No base file/template is read or merged.

- Agent model: taken from `runtime.model`, else `OPENCLAW_SOULFACTORY_DEFAULT_MODEL`, and applied via `agents add --model`; the web draft exposes no model field (`internal/soulfactory/openclawcontrol/control.go:647-652,919-930`).
- Bindings: always `nostr:<account>` plus `OPENCLAW_SOULFACTORY_DEFAULT_BINDINGS` (`internal/soulfactory/openclawcontrol/control.go:166-168,935-942`).
- Plugins: installed/allowlisted from `OPENCLAW_SOULFACTORY_REQUIRED_PLUGINS`, not from draft/Nostr/UI (`internal/soulfactory/openclawcontrol/control.go:164-168,624-646,795-817`).
- Production rejects shared `existing-container` for an externally reachable soul and requires `per-agent-compose` (`internal/soulfactory/openclawcontrol/control.go:434-450`), reinforcing per-agent rather than fleet-wide config ownership.

Compared with the fleet template, Bahia does **not** surface or preserve: `logging`, `auth.profiles`, `models.providers`, `agents.defaults` (model/context pruning/heartbeat/concurrency/timezone/memorySearch), top-level `bindings`, `messages`, `commands`, `session`, `hooks`, full `channels.nostr` policy/profile/NIP-29 groups/Communikeys, `gateway`, `mcp.servers`, `skills`, general plugin entries/discovery, `tools.profile`, or `diagnostics` (reference: `fleet-planning/docs/projects/openclaw-nostr/openclaw.json:2-467`). Some fleet membership is assigned by backend-configured NIP-29/Communikey managers during provisioning (`internal/soulfactory/provisioner_full.go:264-313`), but it is neither the reference OpenClaw channel config nor Nostr/UI-settable full fleet configuration.

**Answer:** configuration is per-agent and only a narrow Soul Factory subset is event/UI-addressable. A few operator-side defaults are fleet-ish environment settings, but they are not writable by Nostr or web UI and do not constitute template merge/support.

### 3. New/edit UI wiring audit

Classification used below:

- **Wired E2E:** user value crosses the UI/event/backend boundary and has a concrete create-time side effect.
- **Partial:** value is signed/persisted and parsed/passed, but only some subfields are used, application is optional/adapter-dependent, or it affects Bahia metadata rather than OpenClaw runtime config.
- **Display/local-only:** no backend/runtime mutation from that control in the mounted page.

#### New soul wizard

| Control(s) | Evidence and actual effect | Classification |
|---|---|---|
| Name, agent ID, purpose, tier, NIP-05, identity theme, emoji | Declared/bound at `web/src/routes/souls/new/+page.svelte:72-80,634-644`; serialized into identity at `:286-297`; emitted to runtime params at `internal/soulfactory/runtime_adapter.go:651-659`. All identity values are rendered into OpenClaw markdown (`internal/soulfactory/openclawcontrol/control.go:1357-1380`), and agent ID/allowed tier drive provisioning. The requested NIP-05 is **not** copied by `applyToSoul`; when configured, the NIP-05 manager derives/overrides it from agent ID before profile publication (`internal/soulfactory/provisioning_resolution.go:180-203`; `internal/soulfactory/provisioner_full.go:351-355`). | **Wired E2E on create for runtime identity docs**; **NIP-05 is partial** for the published profile. Theme/emoji are markdown identity data, not OpenClaw appearance config. |
| Preset library | Selection locally merges preset identity/persona/avatar/voice/memory (`web/src/routes/souls/new/+page.svelte:155-181,651-659`). The resulting values persist, but selected preset ID does not. | **Partial/local helper**. |
| Clone customization from existing soul | Copies parsed values locally (`web/src/routes/souls/new/+page.svelte:183-195,663-670`); source soul ID is not part of the draft schema. | **Partial/local helper**. |
| Template selector | Produces `template_ref` in draft/provision (`web/src/routes/souls/new/+page.svelte:263-289`; `web/src/lib/stores/souls.svelte.js:896-898,1058-1076`) and backend resolution retains/merges template provenance. | **Wired E2E on create**. |
| Personality: traits, style, tone, constraints, role, guidelines, red lines, advanced notes | Component edits the bound persona (`web/src/lib/components/PersonalityBuilder.svelte:187-255`); v2 draft and runtime params preserve it; OpenClaw provision renders it as JSON inside `SOUL.md` (`internal/soulfactory/runtime_adapter.go:660-663`; `internal/soulfactory/openclawcontrol/control.go:1367-1380`). | **Wired to create-time SOUL.md**, but **partial as native OpenClaw config**: no raw `SOUL.md` editor and no applied `agents.defaults.systemPromptOverride` merge. |
| Personality Import/Retry | Browser parses a local JSON file into the bound persona, which is saved on the next draft publish (`web/src/lib/components/PersonalityBuilder.svelte:137-184`). | **Wired-to-draft local helper**. |
| Personality Export, collapse/expand, character preview | Browser download or UI state/presentation only (`web/src/lib/components/PersonalityBuilder.svelte:126-135,243-269`). | **Display/local-only**. |
| Avatar prompt | Saved in `avatar.generation.prompt`; provisioning copies only that prompt to the legacy avatar generator (`internal/soulfactory/provisioner_full.go:188-203,313-340`). Generator/storage may be skipped if not configured. | **Partial**. |
| Avatar provider, style preset, seed/randomize, width, height | Bound and preserved (`web/src/lib/components/souls/AvatarStudio.svelte:177-199,228-229`; `internal/domain/soul.go:203-218`), but the create-time provisioning avatar call consumes only prompt and the owned OpenClaw wrapper does not apply these fields. | **Partial (draft/domain only in standard path)**. |
| Avatar generated/uploaded/current refs | Saved as avatar/assets refs and can reach read-model/profile state. | **Partial**. |
| Upload image | Creates `URL.createObjectURL(file)` and saves the browser `blob:` URL; no upload/storage call (`web/src/lib/components/souls/AvatarStudio.svelte:137-143,204-221`). | **Display/local-only / broken durability**. |
| Generate avatar / Retry | Calls optional `onGenerate`; the new page mounts `AvatarStudio` without it (`web/src/lib/components/souls/AvatarStudio.svelte:111-135`; `web/src/routes/souls/new/+page.svelte:676`). It only marks the draft generated and shows a message. | **Display/local-only on new page**. |
| Avatar zoom/history selection | Local state only (`web/src/lib/components/souls/AvatarStudio.svelte:75-82,237-256`). | **Display-only**. |
| Voice provider, voice ID, persona label/profile/style/accent/pacing, auto mode, persona ID, existing voice ref, sample text | Bound into v2 voice/assets and passed in runtime params (`web/src/lib/components/souls/VoiceStudio.svelte:181-234,260-264`; `internal/domain/soul.go:221-237`; `internal/soulfactory/runtime_adapter.go:660-675`). The owned provision wrapper does not apply voice config. | **Partial (draft/domain; requires another capable runtime/bridge)**. |
| Voice provider model, speed, format, ElevenLabs stability/similarity, Azure locale/style degree, local command | Preserved only inside a dynamic provider settings map (`web/src/lib/components/souls/VoiceStudio.svelte:245-257`; `internal/domain/soul.go:223-228`). This “Model” is a TTS model, **not the agent LLM model**. | **Partial/provider-dependent**. |
| Play sample / Retry | Exits without a deployed `soul`; new page passes none (`web/src/lib/components/souls/VoiceStudio.svelte:122-125`; `web/src/routes/souls/new/+page.svelte:678`). PSTF explicitly deferred sample generation/storage (`pstf/features/SOUL_FACTORY_VOICE_PROVIDER_REGISTRY/hitl_decisions.md:5-8`). | **Display/local-only on new page**. |
| Memory embedding provider/model, strategy, top-K, threshold, auto-index, rerank enabled/model, retention | Bound and preserved (`web/src/lib/components/MemoryConfig.svelte:164-263`; `internal/domain/soul.go:241-253`). Standard provisioning creates default Qdrant/seed memory if configured but does not apply these UI tuning fields to generated `openclaw.json` (`internal/soulfactory/provisioner_full.go:354-420`; `internal/soulfactory/openclawcontrol/control.go:695-739`). | **Partial**. |
| Trigger reindex / Retry | `canReindex` requires a deployed `soul`; new page passes none (`web/src/lib/components/MemoryConfig.svelte:45-53,106-148,268-281`; `web/src/routes/souls/new/+page.svelte:679-680`). | **Disabled/display-only on new page**. |
| Runtime capability/target | UI filters live `30317` capabilities for `soulfactory.provision` and serializes target/pubkey/ref (`web/src/routes/souls/new/+page.svelte:96-100,301-305,696-702`). Reactor uses it to select the adapter. | **Wired E2E**. |
| Allowed Nostr kinds | Parsed into permissions and used when provisioning the Signet agent (`web/src/routes/souls/new/+page.svelte:307-312,707`; `internal/soulfactory/provisioner_full.go:217-257`). | **Wired E2E for signer policy**. |
| Tool grants | Parsed/stored/passed (`web/src/routes/souls/new/+page.svelte:307-312,708`; `internal/soulfactory/runtime_adapter.go:665-669`), but no evidence the owned OpenClaw wrapper converts them to `tools`, MCP, skills, or plugin policy. | **Partial**. |
| Approval policy | Stored/passed in permissions (`web/src/routes/souls/new/+page.svelte:307-312,709`; `internal/soulfactory/runtime_adapter.go:665-669`); no owned-wrapper enforcement found. | **Partial**. |
| NIP-65 discovery; read/write/control relays | All persist in relay policy (`web/src/routes/souls/new/+page.svelte:314-319,715-718`). The wrapper uses only `control` as the fallback OpenClaw Nostr relay list (`internal/soulfactory/openclawcontrol/control.go:704-711`); read/write/NIP-65 are control-plane policy/hints. | **Partial**. |
| Repository picker/repo | Resolves a repository coordinate and is used by workspace/Bahia integration (`web/src/routes/souls/new/+page.svelte:268-271,321-325,724`; `internal/soulfactory/provisioning_resolution.go:197-203`). | **Wired/adapter-dependent**. |
| Branch, environment | Persist and pass (`web/src/routes/souls/new/+page.svelte:321-325,727-728`) but are not used by the owned OpenClaw config renderer. | **Partial**. |
| Save panel/Save draft | Signs and publishes `31952` with verified relay acceptance (`web/src/routes/souls/new/+page.svelte:338-372,738`; `web/src/lib/stores/souls.svelte.js:883-920`). | **Wired E2E to Nostr desired state**. |
| Provision Soul | Signs/publishes `5950`, tracks `6950/7950`, and reaches runtime provisioning (`web/src/routes/souls/new/+page.svelte:423-479,772-773`). | **Wired E2E**. |
| Basic/Advanced, tabs, Back/Next, preview, Cancel/navigation | UI disclosure/navigation only, although changing tabs triggers draft save (`web/src/routes/souls/new/+page.svelte:610-629,737-753,771`). | **Display/local-only**, except the explicit save side effect. |

#### Edit page

The edit form exposes: name, purpose, tier, NIP-05, runtime capability, allowed kinds, tool grants, approval policy, NIP-65, read/write/control relays, repository, branch, environment, avatar ref, voice ref, update reason, Save, and Cancel (`web/src/routes/souls/[id]/edit/+page.svelte:194-242`).

- **All value fields are partial/draft-only with respect to the running agent.** They are included in the new `31952` and `1950 update`, but `handleUpdate` ignores `resolved_spec` and never calls the runtime adapter (`internal/soulfactory/lifecycle_handler.go:862-894`).
- **Update reason is wired only as event metadata**, not desired runtime state (`web/src/routes/souls/[id]/edit/+page.svelte:170-179`; `web/src/lib/stores/souls.svelte.js:923-940`).
- **Save is wired to Nostr workflow evidence, not runtime effect**; Cancel is navigation only.
- The edit builder is also schema-incomplete relative to new: it emits v1 and omits structured persona, avatar, voice, memory, identity theme, and emoji, while requesting replace mode (`web/src/routes/souls/[id]/edit/+page.svelte:111-179`). Publishing it replaces the latest `31952` coordinate with a reduced draft and would discard those omitted sections if the lower-level replace update were later connected.

#### Controls that do not exist

Searches of the two routes and `web/src/lib/components/souls` find no agent-LLM model selector, model-provider/auth-profile editor, skills editor, channel/NIP-29 group editor, gateway config, MCP server editor, plugin editor, tools profile, diagnostics, or raw `SOUL.md` editor. The sole “Model” label is inside provider-specific voice/TTS settings (`web/src/lib/components/souls/VoiceStudio.svelte:245-257`).

### Intended vs. implemented PSTF gaps

- The customization acceptance criteria require OpenClaw to receive memory provider/model/query fields, define persona `agent_defaults_patch`, and apply TTS, memorySearch, systemPromptOverride, and identity through config hot reload without respawning (`pstf/features/SOUL_FACTORY_AGENT_CUSTOMIZATION/acceptance_criteria.json:55-66,93-141`). The current owned Go wrapper only records the persona patch and warns hot reload is unconfirmed; its generated config omits TTS/memorySearch/agents.defaults.
- The OpenClaw customization verification report says avatar/voice/memory/persona bridge methods exist but also says `memory.reindex` and `config.reload` return explicit not-implemented errors (`pstf/features/OPENCLAW_SOULFACTORY_CUSTOMIZATION/verification_report.md:12-19`). The standard Bahia command driver does not advertise config reload (`internal/soulfactory/openclaw_sidecar.go:48-55`).
- PSTF verifies v2 customization round-trip and partial hot-reload merge (`pstf/features/SOUL_FACTORY_AGENT_CUSTOMIZATION/acceptance_criteria.json:142-169`), while the current edit page emits a reduced v1 replace draft and uses the non-runtime `update` action.
- The control-wrapper verification documents `soulfactory.update` as already implemented and notes per-soul gateways are required for independent identities (`pstf/features/OPENCLAW_SOULFACTORY_CONTROL_WRAPPER/verification_report.md:35-39,49-58`), but the web lifecycle update handler does not dispatch that implemented method.
- Provider catalog synchronization and voice sample storage were explicitly deferred (`pstf/features/SOUL_FACTORY_VOICE_PROVIDER_REGISTRY/hitl_decisions.md:5-8`), matching the hard-coded/local-only UI behavior.

### Root cause

The implementation has three layers that are easy to conflate:

1. **Rich desired-state schema/UI** (v2 draft and customization components),
2. **Nostr lifecycle/read-model orchestration** (kinds `31952/5950/1950/6950/7950/31951`), and
3. **Runtime-specific application** (kind `38384` driver/wrapper).

Creation connects all three, but the owned OpenClaw wrapper consumes only a narrow subset. Ordinary edit currently stops in layer 2. Hot-reload code exists across layers 2/3 but is not mounted in the UI and is not in the default wrapper method set. Fleet-wide OpenClaw configuration is a separate missing control plane entirely.

## Investigation Log

## Root Cause / Answers

**Q1 — Quick/easy agent config + SOUL.md updates via web UI: NO.**
- Create is end-to-end but heavyweight: an 8-stage provisioning saga + Docker Compose (120s health ceiling), not a live config write (`internal/soulfactory/provisioner_full.go:180-509`).
- There is no raw `SOUL.md` editor anywhere in the UI; `SOUL.md` is generated from structured persona at provision time (`internal/soulfactory/openclawcontrol/control.go:1357-1380`).
- The edit page publishes kind `31952` + `1950 update`, but `handleUpdate` explicitly "records additive lifecycle customization refs without invoking runtime adapters" — verified at `internal/soulfactory/lifecycle_handler.go:862-887`. Edits change Nostr read-model lineage only; the running agent is untouched.
- A hot-reload path (`1950 hot-reload` → section-diff → `38384` calls) exists but is unmounted in the UI (`LiveUpdate.svelte` is never imported) and the default sidecar driver doesn't advertise config reload (`internal/soulfactory/openclaw_sidecar.go:48-55`).

**Q2 — Fleet-wide settings à la `openclaw.json` via Nostr AND web UI: NO (neither).**
- All Soul Factory kinds (`1950/5950/6950/7950/30317/31950-31952/38384/38386`) are agent/soul-scoped; there is no fleet/global config kind or REST endpoint (only read-only `GET /soulfactory/runtimes`).
- The wrapper renders a fresh minimal config containing only `plugins` and `channels.nostr` — verified at `internal/soulfactory/openclawcontrol/control.go:693-739`. It never reads or merges a base template. Model, bindings, and required plugins come from sidecar env vars (`OPENCLAW_SOULFACTORY_DEFAULT_MODEL/BINDINGS/REQUIRED_PLUGINS`), which are operator-host settings, not Nostr/UI-settable.
- Unsupported vs. the fleet template: logging, auth.profiles, models.providers, agents.defaults, top-level bindings, messages, commands, session, hooks, full channels.nostr (dmPolicy/profile/NIP-29 groups/communikeys), gateway, mcp.servers, skills, plugin entries, tools.profile, diagnostics.

**Q3 — Are all UI features wired? NO.** See the wiring table above. Summary:
- Wired E2E (create only): identity core fields, template ref, runtime capability/target, allowed kinds (Signet policy), draft save, provision.
- Partial (persisted but not applied by the owned OpenClaw wrapper): avatar provider/style/dimensions, all voice config, memory tuning fields, tool grants, approval policy, read/write/NIP-65 relays, branch, environment, NIP-05.
- Display/local-only: avatar upload (blob: URL, not durable), generate-avatar & play-voice-sample & reindex buttons on the new page, personality export/preview, edit-page Save (w.r.t. runtime effect).
- Edit page additionally regresses the draft to a reduced v1 schema that would drop persona/avatar/voice/memory if replace-mode application were ever connected.

**Root cause:** three layers — (1) rich desired-state schema/UI, (2) Nostr lifecycle/read-model orchestration, (3) runtime application via `38384` — are only fully connected for creation, and even then the owned wrapper consumes a narrow subset. Edit stops at layer 2; fleet-wide config has no layer at all.

## Recommendations (Remediation/Upgrade Plan)

### Phase 1 — Make edit real (highest value, smallest scope)
1. **Wire `handleUpdate` to the runtime adapter** (`internal/soulfactory/lifecycle_handler.go:862-887`): resolve the referenced `31952`, diff vs. current spec, and dispatch the already-implemented `soulfactory.update` / `soulfactory.persona.update` wrapper methods (`openclawcontrol/control.go:245-303,973-1024`) instead of only recording spec-hash lineage. (Tracked as bahia-a1so.3 per the code comment.)
2. **Fix the edit page draft schema** (`web/src/routes/souls/[id]/edit/+page.svelte:90-179`): emit v2 with full persona/avatar/voice/memory sections (load from the current `31952`) so replace-mode updates don't silently drop customization.
3. **Mount the hot-reload UI**: integrate `LiveUpdate.svelte` into the soul detail/edit pages and add `config.reload` to the default sidecar driver method set (`internal/soulfactory/openclaw_sidecar.go:48-55`); implement the not-implemented `config.reload`/`memory.reindex` bridge methods flagged in `pstf/features/OPENCLAW_SOULFACTORY_CUSTOMIZATION/verification_report.md`.
4. **Add a raw `SOUL.md`/AGENTS.md editor** (advanced tab) that round-trips through `soulfactory.persona.update`, and surface the generated files read-only today.

### Phase 2 — Fleet-wide agent settings control plane
5. **Introduce a fleet config document**: a parameterized-replaceable Nostr kind (e.g. `31953 soulfactory-fleet-config/v1`) carrying an OpenClaw config template (the `openclaw.json` shape, with `${VAR}` placeholders), signed by trusted operators; validated against an allowlist schema.
6. **Template merge in the wrapper**: change `renderRuntimeFiles` (`openclawcontrol/control.go:693-739`) to deep-merge fleet template → per-agent Soul Factory sections → generated secrets/identity, instead of emitting only `plugins`+`channels.nostr`. Migrate env-var defaults (model/bindings/required plugins) into the fleet document.
7. **Web UI for fleet settings**: a settings route to view/edit/publish the fleet config event with section-level forms (models/providers, agents.defaults, channels incl. NIP-29 groups, mcp.servers, skills, plugins) plus raw-JSON escape hatch and diff preview.
8. **Reconciliation**: on fleet-config change, reactor fans out `1950 hot-reload` (or staged redeploy) to affected souls, with per-soul `6950/7950` progress and rollback.

### Phase 3 — Close per-field wiring gaps
9. Apply memory tuning (embedding provider/model, top-K, rerank, retention) and voice/TTS config into generated `openclaw.json` (`memorySearch`, TTS sections) per `SOUL_FACTORY_AGENT_CUSTOMIZATION` acceptance criteria.
10. Map tool grants/approval policy to OpenClaw `tools`/plugin/MCP policy or explicitly gate them in the UI as "control-plane only".
11. Fix avatar upload durability (upload to Blossom/asset store instead of `blob:` URL); wire `onGenerate`/voice-sample/reindex callbacks on the new page or hide the buttons pre-deploy.
12. Add an agent LLM model selector (feeding `runtime.model` → `agents add --model`), currently entirely absent from the UI.
13. **Honesty pass**: gate every not-yet-wired control behind an explicit "saved to draft, applied on next provision" or disabled state (extends bahia-2ju3 "Replace Soul Factory mock/no-op surfaces").

## Preventive Measures
- PSTF acceptance criteria should include an end-to-end "UI field → running-agent config" assertion per control; the customization criteria exist but weren't enforced against the edit path.
- Add an integration test that provisions, edits via the web flow, and asserts the runtime container's `SOUL.md`/`openclaw.json` changed.
- Keep a single source-of-truth wiring table (this doc's audit) in docs/user-guide and update it per feature.
