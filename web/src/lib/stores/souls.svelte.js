// Soul Factory stores
import { SvelteMap } from 'svelte/reactivity';
import {
  nostr,
  parseSoulEvent,
  parseSoulDraftEvent,
  parseTemplateEvent,
  parseRuntimeCapabilityEvent,
  normalizeSoulDraftContent,
  upsertReplaceableEvent,
  isReplaceableTombstone,
  KINDS,
  SOUL_LIFECYCLE_ACTIONS,
  SOUL_RUNTIME_METHODS
} from '$lib/nostr/client.js';
import { authState, login, signWithAuth } from '$lib/stores/auth.js';
import { boot, getEventStore, getPool, getRelayUrls, getServicePubkeys, onStoreRefresh } from '$lib/nostr/boot.js';
import { toWebSocketUrl } from '$lib/nostr/pool-utils.js';
import { createPagedReader } from '$lib/nostr/store-first-backfill.js';
import { CAS_CONTROL_STATE, CP_STATE_TOPICS } from '$lib/nostr/kinds.gen.js';
import { soulRuntimePolicy } from '$lib/stores/operational-views.js';

/** @typedef {import('$lib/types/customization').SoulAvatarSpec} SoulAvatarSpec */
/** @typedef {import('$lib/types/customization').SoulDraftContentV2} SoulDraftContentV2 */
/** @typedef {import('$lib/types/customization').SoulDraftDiffEntry} SoulDraftDiffEntry */
/** @typedef {import('$lib/types/customization').SoulMemorySpec} SoulMemorySpec */
/** @typedef {import('$lib/types/customization').SoulPersonaSpec} SoulPersonaSpec */
/** @typedef {import('$lib/types/customization').SoulVoiceSpec} SoulVoiceSpec */

export const SOUL_DRAFT_SCHEMA_V2 = 'soulfactory-draft/v2';

// --- State ---

// All agent souls
export const souls = $state([]);

// All soul templates
export const templates = $state([]);

// Editable soul drafts (kind 31952)
export const drafts = $state([]);

// Runtime capability announcements (kind 30317)
export const runtimeCapabilities = $state([]);

// Server policy: administratively enabled SoulFactory agent runtimes reported
// by the signed Bahia runtime-policy state (non-secret). Empty means unknown (older server or
// SoulFactory disabled), in which case discovery falls back to capability-only.
export const serverAgentRuntimes = $state([]);

// serverPolicyKnown becomes true only after a verified policy event is available. Unknown
// policy must fail closed: never present a target the server has not enabled.
export const serverPolicy = $state({ known: false });

export const voiceProviders = $state([
  { id: 'openai', label: 'OpenAI TTS', description: 'OpenAI text-to-speech voices' },
  { id: 'elevenlabs', label: 'ElevenLabs', description: 'ElevenLabs hosted voice personas' },
  { id: 'azure', label: 'Azure Speech', description: 'Azure Speech voices' },
  { id: 'local', label: 'Local CLI', description: 'Local command-line TTS provider' }
]);

export const avatarProviders = $state([
  { id: 'flux-comfyui', label: 'FLUX / ComfyUI', description: 'Default SoulFactory avatar generator', presets: ['pixel-art', 'corporate', 'abstract', 'anime'] },
  { id: 'fal', label: 'Fal.ai', description: 'Fal.ai image generation provider', presets: ['realistic', 'anime', 'abstract'] },
  { id: 'replicate', label: 'Replicate', description: 'Replicate-hosted image generation models', presets: ['pixel-art', 'realistic', 'corporate'] }
]);

export const memoryProviders = $state([
  { id: 'openai', label: 'OpenAI Embeddings', description: 'OpenAI embedding models', models: ['text-embedding-3-small', 'text-embedding-3-large'] },
  { id: 'voyage', label: 'Voyage AI', description: 'Voyage embedding models', models: ['voyage-3', 'voyage-3-lite'] },
  { id: 'cohere', label: 'Cohere', description: 'Cohere embedding models', models: ['embed-english-v3.0', 'embed-multilingual-v3.0'] },
  { id: 'local', label: 'Local', description: 'Local embedding runtime', models: [] }
]);

export const customizationDraft = $state({
  persona: null,
  avatar: null,
  voice: null,
  memory: null
});

// Currently selected soul for detail view
export const selectedSoul = $state({ value: null });

// Active provisioning/lifecycle runs (reactive map)
export const provisioningRuns = new SvelteMap();
export const lifecycleRuns = provisioningRuns;

// Loading states
export const loading = $state({
  souls: false,
  templates: false,
  drafts: false,
  capabilities: false
});

// Error state
export const error = $state({ value: null });

export const readModelMeta = $state({
  souls: null,
  templates: null,
  drafts: null,
  capabilities: null,
  history: null
});

function rememberReadModelMeta(key, result) {
  readModelMeta[key] = result?.eose || {
    complete: result?.complete !== false,
    degraded: result?.degraded || null,
    relaySummary: Array.isArray(result?.relaySummary) ? result.relaySummary : []
  };
}


function attachHistoryMetadata(history, metadata) {
  Object.defineProperties(history, {
    complete: { value: metadata.complete, enumerable: false },
    degraded: { value: metadata.degraded, enumerable: false },
    relaySummary: { value: metadata.relaySummary, enumerable: false }
  });
  return history;
}

const soulEvents = new Map();
const templateEvents = new Map();
const draftEvents = new Map();
const capabilityEvents = new Map();

// --- Derived state helpers ---

// Souls by status
export function soulsByStatus() {
  const grouped = {
    active: [],
    provisioning: [],
    suspended: [],
    revoked: [],
    draft: []
  };

  for (const soul of souls) {
    const status = soul.status || 'active';
    if (grouped[status]) {
      grouped[status].push(soul);
    }
  }

  return grouped;
}

// Souls count
export function soulCounts() {
  const grouped = soulsByStatus();
  return {
    total: Object.values(grouped).flat().length,
    active: grouped.active.length,
    provisioning: grouped.provisioning.length,
    suspended: grouped.suspended.length,
    revoked: grouped.revoked.length
  };
}

// Templates by tier
export function templatesByTier() {
  const grouped = {
    lightweight: [],
    standard: [],
    heavy: []
  };

  for (const template of templates) {
    const tier = template.tier || 'standard';
    if (grouped[tier]) {
      grouped[tier].push(template);
    }
  }

  return grouped;
}

export function runtimeCapabilitiesByTarget() {
  return runtimeCapabilities.reduce((grouped, capability) => {
    const runtime = capability.runtime || 'unknown';
    grouped[runtime] = grouped[runtime] || [];
    grouped[runtime].push(capability);
    return grouped;
  }, {});
}

export function supportedRuntimeTargets({ method = SOUL_RUNTIME_METHODS.PROVISION, controllerPubkey = '' } = {}) {
  return Array.from(new Set(
    runtimeCapabilities
      .filter((capability) => capability.compatible)
      .filter((capability) => serverPolicyAllows(capability.runtime))
      .filter((capability) => !method || capability.methods.includes(method))
      .filter((capability) => !controllerPubkey || capability.controllerPubkeys.length === 0 || capability.controllerPubkeys.includes(controllerPubkey))
      .map((capability) => capability.runtime)
      .filter(Boolean)
  ));
}

function serverPolicyAllows(runtime) {
  return serverPolicy.known && serverAgentRuntimes.includes(runtime);
}

// Relay-canonical runtime policy is operator-only. Unknown fails closed.
let stopPolicyRefresh = null;
function applyServerAgentRuntimes() {
  const runtimes = soulRuntimePolicy();
  replaceStateArray(serverAgentRuntimes, runtimes || []);
  serverPolicy.known = Array.isArray(runtimes);
}
export async function refreshServerAgentRuntimes() {
  try {
    await boot();
    applyServerAgentRuntimes();
    if (!stopPolicyRefresh) stopPolicyRefresh = onStoreRefresh(applyServerAgentRuntimes);
  } catch {
    serverPolicy.known = false;
  }
}

/**
 * Methods advertised by the newest compatible live capability for a runtime
 * target. When runtimePubkey is known, pubkey-matching capabilities win over
 * target-level ones. Returns null when no compatible capability is observed so
 * callers can distinguish "not advertised" from "not discovered".
 */
export function supportedRuntimeMethods({ runtime = '', runtimePubkey = '' } = {}) {
  const candidates = runtimeCapabilities
    .filter((capability) => capability.compatible)
    .filter((capability) => serverPolicyAllows(capability.runtime))
    .filter((capability) => !runtime || capability.runtime === runtime)
    .sort(newestFirst);
  if (candidates.length === 0) return null;
  const selected = runtimePubkey
    ? candidates.filter((capability) => capability.pubkey === runtimePubkey)
    : [];
  const source = selected.length > 0 ? selected : (runtimePubkey ? [] : candidates);
  if (source.length === 0) return null;
  return Array.from(new Set(source.flatMap((capability) => capability.methods || [])));
}

// --- Replaceable event state helpers ---

function replaceStateArray(target, values) {
  target.length = 0;
  target.push(...values);
}

function parsedReplaceableValues(eventMap, parser, sorter) {
  const values = Array.from(eventMap.values())
    .filter((event) => !isReplaceableTombstone(event))
    .map(parser)
    .filter(Boolean);

  values.sort(sorter);
  return values;
}

function resetReplaceableState(eventMap, events, target, parser, sorter) {
  eventMap.clear();
  for (const event of events || []) {
    upsertReplaceableEvent(eventMap, event);
  }
  replaceStateArray(target, parsedReplaceableValues(eventMap, parser, sorter));
}

function applyReplaceableUpdate(eventMap, event, target, parser, sorter) {
  const result = upsertReplaceableEvent(eventMap, event);
  if (!result.accepted) return false;
  replaceStateArray(target, parsedReplaceableValues(eventMap, parser, sorter));
  return true;
}

const newestFirst = (a, b) => Number(b.createdAt || 0) - Number(a.createdAt || 0);
const templateSort = (a, b) => (a.name || a.identifier || '').localeCompare(b.name || b.identifier || '');
const capabilitySort = (a, b) => (a.runtime || '').localeCompare(b.runtime || '') || newestFirst(a, b);

/** @returns {SoulAvatarSpec} */
export function createDefaultAvatarSpec(overrides = {}) {
  return {
    generation: {
      prompt: '',
      style_preset: 'pixel-art',
      seed: '',
      width: 512,
      height: 512,
      provider: 'flux-comfyui',
      ...(overrides.generation || {})
    },
    uploaded_ref: '',
    generated_ref: '',
    current: 'generated',
    ...overrides
  };
}

/** @returns {SoulVoiceSpec} */
export function createDefaultVoiceSpec(overrides = {}) {
  return {
    provider: 'openai',
    persona_id: '',
    persona: {
      label: '',
      profile: '',
      style: 'articulate',
      accent: 'neutral american',
      pacing: 'measured',
      ...(overrides.persona || {})
    },
    auto_mode: 'tagged',
    sample_text: '',
    providers: {},
    ...overrides
  };
}

/** @returns {SoulMemorySpec} */
export function createDefaultMemorySpec(overrides = {}) {
  return {
    embedding_provider: 'openai',
    embedding_model: 'text-embedding-3-small',
    search: {
      top_k: 10,
      score_threshold: 0.7,
      rerank: false,
      rerank_model: '',
      ...(overrides.search || {})
    },
    strategy: 'session-aware',
    auto_index: true,
    retention_days: 90,
    ...overrides
  };
}

const SUPPORTED_RERANK_MODELS = new Set([
  'cohere-rerank-v3',
  'rerank-v3.5',
  'rerank-english-v3.0',
  'rerank-multilingual-v3.0'
]);

export function normalizeProvisioningMemorySpec(memory = {}) {
  const normalized = createDefaultMemorySpec(memory);
  if (!normalized.search.rerank) {
    delete normalized.search.rerank_model;
  } else if (!SUPPORTED_RERANK_MODELS.has(normalized.search.rerank_model)) {
    normalized.search.rerank_model = 'rerank-v3.5';
  }
  return normalized;
}

/** @returns {SoulPersonaSpec} */
export function createDefaultPersonaSpec(overrides = {}) {
  return {
    traits: [],
    style: 'conversational',
    tone: 'friendly professional',
    constraints: [],
    system_prompt_sections: {
      role: '',
      guidelines: '',
      red_lines: '',
      ...(overrides.system_prompt_sections || {})
    },
    ...overrides
  };
}

export function resetCustomizationDraft(specs = {}) {
  customizationDraft.persona = createDefaultPersonaSpec(specs.persona || {});
  customizationDraft.avatar = createDefaultAvatarSpec(specs.avatar || {});
  customizationDraft.voice = createDefaultVoiceSpec(specs.voice || {});
  customizationDraft.memory = createDefaultMemorySpec(specs.memory || {});
  return customizationDraft;
}

export function patchCustomizationSection(section, updates = {}) {
  if (!['persona', 'avatar', 'voice', 'memory'].includes(section)) {
    throw new Error(`Unknown customization section: ${section}`);
  }
  customizationDraft[section] = {
    ...(customizationDraft[section] || {}),
    ...updates
  };
  return customizationDraft[section];
}

export function patchNestedCustomizationSection(section, nestedKey, updates = {}) {
  const current = customizationDraft[section] || {};
  customizationDraft[section] = {
    ...current,
    [nestedKey]: {
      ...(current[nestedKey] || {}),
      ...updates
    }
  };
  return customizationDraft[section];
}

export function resolveAvatarRef(avatar = {}) {
  const current = avatar.current || 'generated';
  if (current === 'uploaded') return avatar.uploaded_ref || avatar.uploadedRef || avatar.generated_ref || avatar.generatedRef || '';
  return avatar.generated_ref || avatar.generatedRef || avatar.uploaded_ref || avatar.uploadedRef || '';
}

export function buildDraftCustomizationContent({
  identity = {},
  persona = customizationDraft.persona,
  avatar = customizationDraft.avatar,
  voice = customizationDraft.voice,
  memory = customizationDraft.memory,
  assets = {},
  ...rest
} = {}) {
  const normalizedAvatar = avatar || createDefaultAvatarSpec();
  const normalizedVoice = voice || createDefaultVoiceSpec();
  return normalizeSoulDraftContent({
    schema: SOUL_DRAFT_SCHEMA_V2,
    ...rest,
    identity,
    persona: persona || createDefaultPersonaSpec(),
    avatar: normalizedAvatar,
    voice: normalizedVoice,
    memory: memory || createDefaultMemorySpec(),
    assets: {
      ...assets,
      avatar_ref: assets.avatar_ref || assets.avatarRef || resolveAvatarRef(normalizedAvatar),
      voice_ref: assets.voice_ref || assets.voiceRef || ''
    }
  });
}

function isPlainObject(value) {
  return value && typeof value === 'object' && !Array.isArray(value);
}

/** @returns {SoulDraftDiffEntry[]} */
export function diffDraftContent(before = {}, after = {}, prefix = '') {
  const changes = [];
  const keys = new Set([...Object.keys(before || {}), ...Object.keys(after || {})]);

  for (const key of keys) {
    const path = prefix ? `${prefix}.${key}` : key;
    const beforeValue = before?.[key];
    const afterValue = after?.[key];

    if (stableStringify(beforeValue) === stableStringify(afterValue)) continue;

    if (isPlainObject(beforeValue) && isPlainObject(afterValue)) {
      changes.push(...diffDraftContent(beforeValue, afterValue, path));
      continue;
    }

    changes.push({
      path,
      before: beforeValue,
      after: afterValue,
      type: beforeValue === undefined ? 'added' : afterValue === undefined ? 'removed' : 'changed'
    });
  }

  return changes;
}

// --- Actions ---

const SOUL_HISTORY_KINDS = [KINDS.PROVISIONING_STATUS, KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT].filter(Boolean);
const HEX_PUBKEY = /^[0-9a-f]{64}$/;

function normalizedPubkeys(values = []) {
  return [...new Set(values.map((value) => String(value || '').trim().toLowerCase())
    .filter((value) => HEX_PUBKEY.test(value)))].sort();
}

/**
 * SoulFactory keys the Bahia service vouches for. The service-signed runtime
 * policy record (30900, t=soul-factory-runtime-policy) may name the
 * controller that signs Souls and the runtime keys pinned in
 * `soul_factory.runtime_pubkeys`. Only seeded service keys can author it.
 */
export function attestedSoulFactoryKeys(store = getEventStore(), serviceAuthors = getServicePubkeys()) {
  const authors = normalizedPubkeys(serviceAuthors);
  if (!store || authors.length === 0) return { controllers: [], runtimes: [] };
  const policy = store.query({ kinds: [CAS_CONTROL_STATE], authors, '#t': [CP_STATE_TOPICS.SOUL_RUNTIME_POLICY] })
    .filter((event) => !isReplaceableTombstone(event))
    .sort((a, b) => b.created_at - a.created_at || a.id.localeCompare(b.id))[0];
  if (!policy) return { controllers: [], runtimes: [] };
  let content = {};
  try { content = JSON.parse(policy.content || '{}') || {}; } catch { content = {}; }
  const pinned = content.runtime_pubkeys && typeof content.runtime_pubkeys === 'object' ? Object.values(content.runtime_pubkeys).flat() : [];
  return {
    controllers: normalizedPubkeys(Array.isArray(content.controller_pubkeys) ? content.controller_pubkeys : []),
    runtimes: normalizedPubkeys(pinned)
  };
}

/**
 * Trusted-author policy by kind. No SoulFactory kind is read without `authors`.
 * - factory: 31951 authoritative Souls and their 6950/7950/1951 lifecycle
 *   history are signed by the SoulFactory controller. Trusted controllers are
 *   the deployment-seeded service keys plus any controller the service attests
 *   in its signed runtime policy record.
 * - operator: 31952 drafts, 1950 actions and the 31953 fleet config are
 *   operator documents. The daemon accepts them from
 *   `soul_factory.authorized_pubkeys`, a list the browser cannot verify, so
 *   only the signed-in key is trusted. 31950 templates are operator input the
 *   controller may also publish: operator and factory keys are both trusted.
 * - runtime: 30317 capabilities are signed by runtime sidecars. Trusted
 *   runtime keys are those the service attests (its pinned
 *   `runtime_pubkeys`), those named by an already-trusted 31951 Soul, and the
 *   factory keys themselves, which could vouch for any runtime anyway. A
 *   self-signed capability can never introduce its own key.
 */
export function trustedSoulAuthors(store = getEventStore(), serviceAuthors = getServicePubkeys()) {
  const attested = attestedSoulFactoryKeys(store, serviceAuthors);
  const factory = normalizedPubkeys([...serviceAuthors, ...attested.controllers]);
  const operator = authState.status === 'authenticated' ? normalizedPubkeys([authState.pubkey]) : [];
  const soulRuntimes = store && factory.length
    ? store.query({ kinds: [KINDS.AGENT_SOUL], authors: factory }).map((event) => parseSoulEvent(event)?.runtime?.runtime_pubkey)
    : [];
  return { factory, operator, runtime: normalizedPubkeys([...factory, ...attested.runtimes, ...soulRuntimes]) };
}

/** The subscription units; each is one live REQ and one paged history walk. */
export function soulFilterUnits(authors = trustedSoulAuthors()) {
  const templateAuthors = normalizedPubkeys([...authors.factory, ...authors.operator]);
  return [
    { name: 'factory', filters: authors.factory.length ? [
      { kinds: [KINDS.AGENT_SOUL], authors: authors.factory },
      { kinds: SOUL_HISTORY_KINDS, authors: authors.factory }
    ] : [] },
    { name: 'templates', filters: templateAuthors.length ? [{ kinds: [KINDS.SOUL_TEMPLATE], authors: templateAuthors }] : [] },
    { name: 'operator', filters: authors.operator.length ? [
      { kinds: [KINDS.SOUL_DRAFT, KINDS.SOUL_FLEET_CONFIG], authors: authors.operator },
      { kinds: [KINDS.SOUL_ACTION], authors: authors.operator }
    ] : [] },
    { name: 'runtime', filters: authors.runtime.length ? [{ kinds: [KINDS.RUNTIME_CAPABILITY], authors: authors.runtime }] : [] }
  ];
}

export function soulStoreFilters(store = getEventStore(), serviceAuthors = getServicePubkeys()) {
  return soulFilterUnits(trustedSoulAuthors(store, serviceAuthors)).flatMap((unit) => unit.filters);
}

const SOUL_READ_MODELS = [
  { kind: KINDS.AGENT_SOUL, key: 'souls', events: soulEvents, target: souls, parse: parseSoulEvent, sort: newestFirst },
  { kind: KINDS.SOUL_TEMPLATE, key: 'templates', events: templateEvents, target: templates, parse: parseTemplateEvent, sort: templateSort },
  { kind: KINDS.SOUL_DRAFT, key: 'drafts', events: draftEvents, target: drafts, parse: parseSoulDraftEvent, sort: newestFirst },
  { kind: KINDS.RUNTIME_CAPABILITY, key: 'capabilities', events: capabilityEvents, target: runtimeCapabilities, parse: parseRuntimeCapabilityEvent, sort: capabilitySort }
];
const readModelSignatures = new Map();

// The read models are a projection of the verified store under the trusted
// filters. A model is replaced only when its event set changed, so unrelated
// store traffic does not re-render the gallery.
function rebuildSoulFactoryFromStore(store, filters) {
  for (const model of SOUL_READ_MODELS) {
    const events = filters.filter((filter) => filter.kinds.includes(model.kind))
      .flatMap((filter) => store.query({ ...filter, kinds: [model.kind] }));
    const signature = events.map((event) => event.id).sort().join(',');
    if (readModelSignatures.get(model.key) !== signature) {
      readModelSignatures.set(model.key, signature);
      resetReplaceableState(model.events, events, model.target, model.parse, model.sort);
    }
    // Cached rows render immediately; relay catch-up is metadata, not a gate.
    loading[model.key] = false;
  }
}

let soulBinding = null;

function projectSoulFactory() {
  if (!soulBinding) return;
  const authors = trustedSoulAuthors(soulBinding.store, soulBinding.serviceAuthors);
  const units = soulFilterUnits(authors);
  rebuildSoulFactoryFromStore(soulBinding.store, units.flatMap((unit) => unit.filters));
  // A newly trusted Soul can name a runtime key, which widens the runtime unit.
  if (soulBinding.relay) soulBinding.reader.sync(units);
}

function publishSoulCatchupMetadata(caught) {
  if (!soulBinding) return;
  const metadata = soulBinding.reader.metadata();
  for (const key of ['souls', 'templates', 'drafts', 'capabilities', 'history']) rememberReadModelMeta(key, metadata);
  if (caught) error.value = caught.message;
  else if (metadata.complete) error.value = null;
  for (const waiter of [...soulListeners]) waiter();
}

/**
 * Bind the SoulFactory read models to the shared store and project the cache
 * at once. With `relay` (the default) also start, or re-sync, the app-lifetime
 * relay reader; once started it stays on until teardown. The layout owns that
 * call: it starts the reader at boot, after its own core REQs are issued and
 * without waiting on any relay, and re-syncs it when the signed-in operator
 * changes. Pages pass `relay: false`.
 */
// Re-projects cached SoulFactory read models for the current trusted author
// set (the signed-in operator is a trusted author of drafts, actions and fleet
// config). Independent of the relay reader, so it also runs with every relay
// unreachable.
export function reprojectSoulFactoryForOperator() {
  if (!getEventStore()) return;
  projectSoulFactory();
  for (const listener of [...soulListeners]) listener();
}

export function initSoulFactoryStoreBinding({ relay = true } = {}) {
  const store = getEventStore();
  const pool = getPool();
  const serviceAuthors = getServicePubkeys();
  if (!store || !pool || serviceAuthors.length === 0) return;
  if (soulBinding?.store !== store) {
    teardownSoulFactoryStoreBinding();
    soulBinding = {
      store,
      serviceAuthors,
      relay: false,
      reader: createPagedReader({
        name: 'souls', pool, store,
        relays: [...new Set(getRelayUrls().map(toWebSocketUrl).filter(Boolean))],
        onChange: publishSoulCatchupMetadata
      }),
      offRefresh: onStoreRefresh(() => { projectSoulFactory(); for (const listener of [...soulListeners]) listener(); })
    };
    void refreshServerAgentRuntimes();
  }
  if (relay) soulBinding.relay = true;
  projectSoulFactory();
  publishSoulCatchupMetadata();
}

export function teardownSoulFactoryStoreBinding() {
  soulBinding?.offRefresh();
  soulBinding?.reader.stop();
  soulBinding = null;
  readModelSignatures.clear();
  stopPolicyRefresh?.();
  stopPolicyRefresh = null;
}

/**
 * Page entry point, kept for existing consumers: resolves once the cached read
 * models are projected. It does not wait for a relay and opens no REQ of its
 * own, so calling it on every navigation is free.
 */
export async function subscribeToSoulFactoryUpdates() {
  await boot();
  initSoulFactoryStoreBinding({ relay: false });
}

/** Kept for existing consumers. The reader belongs to the app lifecycle. */
export function unsubscribeFromSoulUpdates() {}

const soulListeners = new Set();

/**
 * Resolve a Soul by agent id: at once when it is cached, otherwise when it
 * arrives, or with null once every relay finished catch-up without it. Abort
 * the signal to stop waiting (for example when the page unmounts).
 */
export async function waitForSoul(agentId, { signal } = {}) {
  await subscribeToSoulFactoryUpdates();
  const find = () => souls.find((soul) => soul.agentId === agentId) || null;
  return new Promise((resolve) => {
    const settle = (value) => {
      soulListeners.delete(check);
      signal?.removeEventListener('abort', abort);
      resolve(value);
    };
    const abort = () => settle(null);
    const check = () => {
      const found = find();
      if (found) settle(found);
      else if (!soulBinding || soulBinding.reader.metadata().settled) settle(null);
    };
    if (signal?.aborted) { resolve(null); return; }
    soulListeners.add(check);
    signal?.addEventListener('abort', abort, { once: true });
    check();
  });
}

function getTag(event, name, fallback = '') {
  const tag = (event.tags || []).findLast?.((candidate) => candidate[0] === name) || [...(event.tags || [])].reverse().find((candidate) => candidate[0] === name);
  return tag?.[1] || fallback;
}

function parseJson(content, fallback = {}) {
  if (!content) return fallback;
  try {
    return JSON.parse(content);
  } catch {
    return fallback;
  }
}

function parseRunStatusEvent(event, defaultTotal) {
  let step = '';
  let progress = { current: 0, total: defaultTotal };
  const message = event.content || 'Request in progress';

  for (const tag of event.tags || []) {
    if (tag[0] === 'step') step = tag[1] || '';
    if (tag[0] === 'progress') {
      const current = Number.parseInt(tag[1], 10);
      const total = Number.parseInt(tag[2], 10);
      progress = {
        current: Number.isFinite(current) ? current : 0,
        total: Number.isFinite(total) ? total : defaultTotal
      };
    }
  }

  return {
    id: event.id,
    step,
    progress,
    message,
    action: getTag(event, 'action'),
    requestKind: getTag(event, 'request-kind'),
    soulRef: getTag(event, 'soul'),
    agentId: getTag(event, 'agent-id'),
    specHash: getTag(event, 'spec-hash'),
	requestId: getTag(event, 'e'),
	runId: getTag(event, 'run-id'),
    event
  };
}

function parseRunResultEvent(event) {
  const data = parseJson(event.content, null);
  const status = getTag(event, 'status', data?.status || '');
  const success = status === 'success' || data?.success === true;
  const errorMessage = success
    ? null
    : data?.error?.message || data?.error || event.content || status || 'Request failed';

  return {
    id: event.id,
    success,
    status,
    error: errorMessage,
    soulRef: getTag(event, 'soul', data?.soul_ref || data?.soulRef || ''),
    action: getTag(event, 'action', data?.action || ''),
    requestKind: getTag(event, 'request-kind', data?.request_kind || data?.requestKind || ''),
    agentId: getTag(event, 'agent-id', data?.agent_id || data?.agentId || ''),
    specHash: getTag(event, 'spec-hash', data?.spec_hash || data?.specHash || ''),
	requestId: getTag(event, 'e', data?.request_id || data?.requestId || ''),
	runId: getTag(event, 'run-id', data?.run_id || data?.runId || ''),
    data: data && typeof data === 'object' ? data : {},
    legacyKind: event.kind === KINDS.SOUL_ACTION_LEGACY_RESULT,
    event
  };
}

// Track a provisioning or lifecycle run. Terminal state comes only from explicit 7950
// (or legacy 1951 migration alias) result events, never from EOSE, CLOSED, or local time.
export function trackLifecycleRun(requestEventId, {
  type = 'provisioning', action = '', expectedAuthor = '', reconciliationTimeoutMs = 30000,
  onProgress, onComplete, onError
} = {}) {
  const defaultTotal = type === 'provisioning' ? 8 : 0;
  const run = $state({
    id: requestEventId,
    type,
    action,
    status: 'pending',
    step: '',
    progress: { current: 0, total: defaultTotal },
    message: type === 'provisioning' ? 'Waiting for provisioning events…' : 'Waiting for lifecycle events…',
    result: null,
    statusEvents: [],
	closedRelays: [],
	runId: '',
	startedAt: Date.now(),
	updatedAt: Date.now(),
	retryState: 'subscribed',
	reconciliation: null
  });

  provisioningRuns.set(requestEventId, run);

  const seenEventIds = new Set();
	let reconciliationTimer = null;
	let callbackTerminalId = '';
	let latestTerminal = null;
  const resultKinds = [KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT].filter(Boolean);
	const statusFilter = { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] };
	const resultFilter = { kinds: resultKinds, '#e': [requestEventId] };
	if (expectedAuthor) {
		statusFilter.authors = [expectedAuthor];
		resultFilter.authors = [expectedAuthor];
	}

	const clearReconciliationTimer = () => {
		if (reconciliationTimer !== null) clearTimeout(reconciliationTimer);
		reconciliationTimer = null;
	};
	const beginTerminalReconciliation = (currentRun) => {
		clearReconciliationTimer();
		currentRun.status = 'reconciling';
		currentRun.retryState = 'reconciling_terminal';
		currentRun.reconciliation = { startedAt: Date.now(), timeoutMs: reconciliationTimeoutMs };
		currentRun.message = 'All readiness checks reported complete. Reconciling the retained terminal result…';
		reconciliationTimer = setTimeout(() => {
			const active = provisioningRuns.get(requestEventId);
			if (!active || active.result) return;
			active.status = 'reconciliation_error';
			active.retryState = 'terminal_missing';
			active.updatedAt = Date.now();
			active.message = 'Readiness reached 100%, but no correlated terminal result arrived. Reconnect or retry reconciliation; do not assume the soul is running.';
		}, Math.max(1, reconciliationTimeoutMs));
	};

  const unsub = nostr.subscribe([
	statusFilter,
	resultFilter
  ], {
    onEvent: (event) => {
      if (event?.id && seenEventIds.has(event.id)) return;
      if (event?.id) seenEventIds.add(event.id);
		if (expectedAuthor && event?.pubkey !== expectedAuthor) return;
		if (getTag(event, 'e') !== requestEventId) return;

      if (event.kind === KINDS.PROVISIONING_STATUS) {
        const status = parseRunStatusEvent(event, defaultTotal);
        const currentRun = provisioningRuns.get(requestEventId);
		if (currentRun?.result) return;
		if (currentRun?.runId && status.runId && currentRun.runId !== status.runId) return;
		if (currentRun) {
		  if (status.runId) currentRun.runId = status.runId;
          currentRun.status = 'running';
          currentRun.step = status.step;
          currentRun.progress = status.progress;
          currentRun.message = status.message;
          currentRun.action = currentRun.action || status.action;
          currentRun.statusEvents.push(status);
		  currentRun.updatedAt = Date.now();
		  currentRun.retryState = 'live';
		  if (status.progress.total > 0 && status.progress.current >= status.progress.total) {
			beginTerminalReconciliation(currentRun);
		  } else {
			clearReconciliationTimer();
			currentRun.reconciliation = null;
		  }
        }

        if (onProgress) onProgress({ step: status.step, progress: status.progress, message: status.message });
        return;
      }

      if (resultKinds.includes(event.kind)) {
        const result = parseRunResultEvent(event);
		if (run.runId && result.runId && run.runId !== result.runId) return;
		const terminalOrder = [Number(event.created_at) || 0, event.id || ''];
		if (latestTerminal && (terminalOrder[0] < latestTerminal[0] || (terminalOrder[0] === latestTerminal[0] && terminalOrder[1] <= latestTerminal[1]))) return;
		latestTerminal = terminalOrder;
        const currentRun = provisioningRuns.get(requestEventId);
        if (currentRun) {
		  clearReconciliationTimer();
		  if (result.runId) currentRun.runId = result.runId;
          currentRun.status = result.success ? 'completed' : 'failed';
          currentRun.action = currentRun.action || result.action;
          currentRun.message = result.success
            ? (type === 'provisioning' ? 'Provisioning complete' : 'Lifecycle action complete')
            : result.error;
          currentRun.result = result;
		  currentRun.updatedAt = Date.now();
		  currentRun.terminalAt = Date.now();
		  currentRun.retryState = 'terminal';
		  currentRun.reconciliation = null;
        }

		if (callbackTerminalId === result.id) return;
		callbackTerminalId = result.id;
		if (result.success) {
          if (onComplete) onComplete(result.data);
        } else if (onError) {
          onError(result.error);
        }
      }
    },
    onEose: () => {
      const currentRun = provisioningRuns.get(requestEventId);
		if (currentRun && currentRun.status === 'pending' && !currentRun.result) {
		currentRun.retryState = 'live_after_snapshot';
        currentRun.message = type === 'provisioning'
          ? 'Request published. Waiting for live provisioning updates…'
          : 'Request published. Waiting for live lifecycle updates…';
      }
    },
    onClosed: (reason, relay) => {
      const currentRun = provisioningRuns.get(requestEventId);
		if (currentRun && !currentRun.result) {
        currentRun.closedRelays.push({ relay, reason });
		currentRun.retryState = 'reconnect_required';
		currentRun.updatedAt = Date.now();
        currentRun.message = reason
          ? `Relay closed this subscription: ${reason}. Waiting for an explicit ${type} result…`
          : `Relay closed this subscription. Waiting for an explicit ${type} result…`;
      }
    }
  });

  return () => {
	clearReconciliationTimer();
    unsub();
    provisioningRuns.delete(requestEventId);
  };
}

// Track a provisioning run
export function trackProvisioningRun(requestEventId, callbacks = {}) {
  return trackLifecycleRun(requestEventId, { ...callbacks, type: 'provisioning' });
}

// Select a soul for detail view
export function selectSoul(soul) {
  selectedSoul.value = soul;
}

// Clear selection
export function clearSelection() {
  selectedSoul.value = null;
}

export function buildSoulRef(soul) {
  if (!soul?.agentId || !soul?.pubkey) {
    throw new Error('Soul reference requires both agentId and pubkey');
  }
  return `${KINDS.AGENT_SOUL}:${soul.pubkey}:${soul.agentId}`;
}

function ensureRelayAcceptance(results = [], defaultErrorMessage) {
  if (results.some((result) => result.accepted === true)) {
    return;
  }

  if (results.length === 0) {
    throw new Error('No connected relays available for publishing');
  }

  const relayErrors = results
    .map((result) => {
      const relayName = result.relay || 'relay';
      const details = result.message || (result.sent ? 'event rejected' : 'send failed');
      return `${relayName}: ${details}`;
    })
    .join('; ');

  throw new Error(`${defaultErrorMessage}. ${relayErrors}`);
}

function stableStringify(value) {
  if (Array.isArray(value)) {
    return `[${value.map(stableStringify).join(',')}]`;
  }
  if (value && typeof value === 'object') {
    return `{${Object.keys(value).sort().map((key) => `${JSON.stringify(key)}:${stableStringify(value[key])}`).join(',')}}`;
  }
  return JSON.stringify(value);
}

async function computeSpecHash(content) {
  if (content?.spec_hash) return content.spec_hash;
  if (!globalThis.crypto?.subtle || typeof TextEncoder === 'undefined') return '';

  const bytes = new TextEncoder().encode(stableStringify(content));
  const digest = await globalThis.crypto.subtle.digest('SHA-256', bytes);
  const hex = Array.from(new Uint8Array(digest)).map((byte) => byte.toString(16).padStart(2, '0')).join('');
  return `sha256:${hex}`;
}

function maybePushTag(tags, name, value, ...extra) {
  if (value === undefined || value === null || value === '') return;
  tags.push([name, String(value), ...extra.filter((item) => item !== undefined && item !== null && item !== '')]);
}

async function ensureAuthenticated(message = 'Authentication required to manage souls') {
  if (authState.status !== 'authenticated') {
    await login();
  }

  if (authState.status !== 'authenticated' || !authState.pubkey) {
    throw new Error(message);
  }
}

export async function publishSoulDraft({ agentId, content = {}, templateRef = '', specHash = '', previousSpecHash = '' } = {}) {
  await ensureAuthenticated('Authentication required to save soul drafts');

  const draftContent = normalizeSoulDraftContent({ schema: SOUL_DRAFT_SCHEMA_V2, ...content });
  if (draftContent.memory) {
    draftContent.memory = normalizeProvisioningMemorySpec(draftContent.memory);
  }
  const id = agentId || draftContent.agent_id || draftContent.agentId || draftContent.identity?.name?.toLowerCase().replace(/[^a-z0-9-]+/g, '-');
  if (!id) throw new Error('Draft requires an agent id');

  const resolvedSpecHash = specHash || draftContent.spec_hash || await computeSpecHash(draftContent);
  if (resolvedSpecHash) draftContent.spec_hash = resolvedSpecHash;
  if (previousSpecHash) draftContent.previous_spec_hash = previousSpecHash;

  const tags = [['d', id]];
  maybePushTag(tags, 'name', draftContent.identity?.name);
  maybePushTag(tags, 'tier', draftContent.identity?.tier);
  maybePushTag(tags, 'template', templateRef || draftContent.template_ref || draftContent.templateRef);
  maybePushTag(tags, 'runtime', draftContent.runtime?.target);
  maybePushTag(tags, 'runtime-pubkey', draftContent.runtime?.runtime_pubkey);
  maybePushTag(tags, 'capability', draftContent.runtime?.capability_ref);
  maybePushTag(tags, 'spec-hash', resolvedSpecHash);
  maybePushTag(tags, 'previous-spec-hash', previousSpecHash || draftContent.previous_spec_hash);

  const unsignedEvent = {
    kind: KINDS.SOUL_DRAFT,
    created_at: Math.floor(Date.now() / 1000),
    pubkey: authState.pubkey,
    tags,
    content: JSON.stringify(draftContent)
  };

  const signedEvent = await signWithAuth(unsignedEvent);
  const publishResults = await nostr.publish(signedEvent);
  ensureRelayAcceptance(publishResults, 'Soul draft was not accepted by any relay');
  // The relay-accepted draft enters the shared store like any other event, so
  // the projection (and a reload) shows it without waiting for a relay echo.
  if (soulBinding?.store.ingest) {
    soulBinding.store.ingest(signedEvent);
    projectSoulFactory();
  } else {
    applyReplaceableUpdate(draftEvents, signedEvent, drafts, parseSoulDraftEvent, newestFirst);
  }

  return { event: signedEvent, publishResults, specHash: resolvedSpecHash };
}

export async function publishSoulAction({ soul, action, reason = '', content = '', extraTags = [], beforePublish = null }) {
  if (!soul) throw new Error('Soul is required');
  if (!action) throw new Error('Action is required');

  await ensureAuthenticated();

  const tags = [
    ['soul', buildSoulRef(soul)],
    ['action', action],
    ['agent-id', soul.agentId]
  ];

  if (reason?.trim()) {
    tags.push(['reason', reason.trim()]);
  }

  for (const tag of extraTags) {
    if (Array.isArray(tag) && tag[0] && tag[1] !== undefined && tag[1] !== null && tag[1] !== '') {
      tags.push(tag.map(String));
    }
  }

  const unsignedEvent = {
    kind: KINDS.SOUL_ACTION,
    created_at: Math.floor(Date.now() / 1000),
    pubkey: authState.pubkey,
    tags,
    content: typeof content === 'string' ? content : JSON.stringify(content)
  };

  const signedEvent = await signWithAuth(unsignedEvent);
  if (beforePublish) beforePublish(signedEvent);
  const publishResults = await nostr.publish(signedEvent);

  ensureRelayAcceptance(publishResults, `Soul action "${action}" was not accepted by any relay`);

  return { event: signedEvent, publishResults };
}

export async function publishSoulUpdateAction({
  soul,
  draft = null,
  draftRef = '',
  draftEventId = '',
  patch = null,
  resolvedSpec = null,
  previousSpecHash = '',
  newSpecHash = '',
  updateMode = 'merge',
  reason = 'Soul update requested'
} = {}) {
  if (!soul) throw new Error('Soul is required');

  const previousHash = previousSpecHash || soul.specHash || soul.previousSpecHash || '';
  const nextHash = newSpecHash || draft?.specHash || draft?.content?.spec_hash || '';
  const resolvedDraftRef = draftRef || draft?.coordinate || draft?.draftRef || soul.draftRef || '';
  const resolvedDraftEventId = draftEventId || draft?.event?.id || draft?.id || '';
  const payload = {
    schema: 'soulfactory-action/v1',
    action: SOUL_LIFECYCLE_ACTIONS.UPDATE,
    method: SOUL_RUNTIME_METHODS.UPDATE,
    draft: resolvedDraftRef,
    draft_ref: resolvedDraftRef,
    draft_event_id: resolvedDraftEventId,
    spec_hash: nextHash,
    previous_spec_hash: previousHash,
    requested_at: Math.floor(Date.now() / 1000),
    params: {
      update_mode: updateMode,
      previous_spec_hash: previousHash,
      new_spec_hash: nextHash
    }
  };

  if (patch) payload.params.patch = patch;
  if (resolvedSpec) payload.params.resolved_spec = resolvedSpec;

  const extraTags = [
    ['method', SOUL_RUNTIME_METHODS.UPDATE],
    ['request-kind', String(KINDS.SOUL_ACTION)]
  ];
  maybePushTag(extraTags, 'draft', resolvedDraftRef);
  maybePushTag(extraTags, 'draft-event', resolvedDraftEventId);
  maybePushTag(extraTags, 'e', resolvedDraftEventId, '', 'draft');
  maybePushTag(extraTags, 'previous-spec-hash', previousHash);
  maybePushTag(extraTags, 'spec-hash', nextHash);

  return publishSoulAction({
    soul,
    action: SOUL_LIFECYCLE_ACTIONS.UPDATE,
    reason,
    content: payload,
    extraTags
  });
}

export async function publishProvisioningRequest({
  agentId,
  name = '',
  tier = 'standard',
  brief = '',
  draftRef = '',
  draftEvent = null,
  draftEventId = '',
  draftContent = {},
  templateRef = '',
  specHash = '',
  beforePublish = null
} = {}) {
  await ensureAuthenticated('Authentication required to provision a soul');

  const id = agentId || draftContent?.agent_id || draftContent?.agentId;
  if (!id) throw new Error('Provisioning request requires an agent id');

  const resolvedName = name || draftContent?.identity?.name || id;
  const resolvedTier = tier || draftContent?.identity?.tier || 'standard';
  const resolvedDraftEventId = draftEventId || draftEvent?.id || '';
  const resolvedDraftRef = draftRef || (draftEvent?.pubkey ? `${KINDS.SOUL_DRAFT}:${draftEvent.pubkey}:${id}` : '');
  const resolvedSpecHash = specHash || draftContent?.spec_hash || '';
  const resolvedTemplateRef = templateRef || draftContent?.template_ref || draftContent?.templateRef || '';
  const runtime = draftContent?.runtime || {};

  const tags = [
    ['agent-id', id],
    ['name', resolvedName],
    ['tier', resolvedTier],
    ['output', 'application/json'],
    ['method', SOUL_RUNTIME_METHODS.PROVISION],
    ['request-kind', String(KINDS.PROVISIONING_REQUEST)]
  ];

  maybePushTag(tags, 'template', resolvedTemplateRef);
  maybePushTag(tags, 'draft', resolvedDraftRef);
  maybePushTag(tags, 'draft-event', resolvedDraftEventId);
  maybePushTag(tags, 'e', resolvedDraftEventId, '', 'draft');
  maybePushTag(tags, 'spec-hash', resolvedSpecHash);
  maybePushTag(tags, 'runtime', runtime.target);
  maybePushTag(tags, 'runtime-pubkey', runtime.runtime_pubkey);
  maybePushTag(tags, 'capability', runtime.capability_ref);

  const content = {
    schema: 'soulfactory-provisioning/v1',
    method: SOUL_RUNTIME_METHODS.PROVISION,
    agent_id: id,
    name: resolvedName,
    tier: resolvedTier,
    template_ref: resolvedTemplateRef,
    draft_ref: resolvedDraftRef,
    draft_event_id: resolvedDraftEventId,
    spec_hash: resolvedSpecHash,
    brief: brief || draftContent?.brief || draftContent?.identity?.purpose || '',
    requested_at: Math.floor(Date.now() / 1000)
  };

  const unsignedEvent = {
    kind: KINDS.PROVISIONING_REQUEST,
    created_at: Math.floor(Date.now() / 1000),
    pubkey: authState.pubkey,
    tags,
    content: JSON.stringify(content)
  };

  const signedEvent = await signWithAuth(unsignedEvent);
  if (beforePublish) beforePublish(signedEvent);
  const publishResults = await nostr.publish(signedEvent);

  ensureRelayAcceptance(publishResults, 'Provisioning request was not accepted by any relay');

  return { event: signedEvent, publishResults };
}

export async function updateSoulDetails(soul, updates = {}) {
  const patch = {
    identity: {
      name: updates.name || soul?.name || '',
      purpose: updates.purpose || updates.brief || soul?.purpose || '',
      tier: updates.tier || soul?.tier || 'standard'
    }
  };

  return publishSoulUpdateAction({
    soul,
    patch,
    previousSpecHash: updates.previousSpecHash || soul?.specHash || '',
    newSpecHash: updates.newSpecHash || '',
    updateMode: 'merge',
    reason: updates.reason || 'Soul details updated'
  });
}

function summarizeHistoryEvent(event, soulRef) {
  if (event.kind === KINDS.SOUL_ACTION) {
    const action = (event.tags || []).find((tag) => tag[0] === 'action')?.[1] || 'unknown';
    const reason = (event.tags || []).find((tag) => tag[0] === 'reason')?.[1] || '';
    return {
      id: event.id,
      kind: event.kind,
      type: 'action',
      action,
      summary: reason ? `${action}: ${reason}` : action,
      createdAt: event.created_at,
      pubkey: event.pubkey,
      event
    };
  }

  if (event.kind === KINDS.PROVISIONING_STATUS) {
    const step = (event.tags || []).find((tag) => tag[0] === 'step')?.[1] || 'progress';
    return {
      id: event.id,
      kind: event.kind,
      type: 'progress',
      action: step,
      summary: event.content || `Progress: ${step}`,
      createdAt: event.created_at,
      pubkey: event.pubkey,
      soulRef,
      event
    };
  }

  if (event.kind === KINDS.PROVISIONING_RESULT || event.kind === KINDS.SOUL_ACTION_LEGACY_RESULT) {
    const result = parseRunResultEvent(event);
    return {
      id: event.id,
      kind: event.kind,
      type: 'result',
      action: result.action || result.status || 'result',
      summary: result.success ? 'Request completed' : `Request failed: ${result.error}`,
      createdAt: event.created_at,
      pubkey: event.pubkey,
      soulRef,
      event
    };
  }

  const status = (event.tags || []).find((tag) => tag[0] === 'status')?.[1] || 'unknown';
  return {
    id: event.id,
    kind: event.kind,
    type: 'soul_update',
    action: status,
    summary: `Soul updated (${status})`,
    createdAt: event.created_at,
    pubkey: event.pubkey,
    soulRef,
    event
  };
}

const CATCHING_UP = { complete: false, degraded: { incomplete: true, reason: 'catching-up' }, relaySummary: [] };

// History is a projection of the verified local store. The app-lifetime reader
// pages the factory's 31951/6950/7950/1951 events and the operator's 1950
// actions with a cursor, so nothing here opens a REQ.
function soulHistoryEvents(store, soul) {
  const soulRef = buildSoulRef(soul);
  const authors = trustedSoulAuthors(store);
  if (!authors.factory.includes(soul.pubkey)) return [];
  const filters = [
    { kinds: [KINDS.AGENT_SOUL], authors: [soul.pubkey], '#d': [soul.agentId] },
    // The SoulFactory correlation tag is the multi-letter `soul`, which NIP-01
    // filters cannot index, so it is matched here.
    { kinds: [KINDS.SOUL_ACTION], authors: authors.operator },
    { kinds: SOUL_HISTORY_KINDS, authors: authors.factory }
  ].filter((filter) => filter.authors.length && filter.kinds.length);
  const events = new Map();
  for (const filter of filters) for (const event of store.query(filter)) {
    if (event.kind !== KINDS.AGENT_SOUL && getTag(event, 'soul') !== soulRef) continue;
    events.set(event.id, event);
  }
  return [...events.values()];
}

function soulHistoryRows(events, soul, limit) {
  const soulRef = buildSoulRef(soul);
  const rows = events.map((event) => summarizeHistoryEvent(event, soulRef))
    .sort((a, b) => b.createdAt - a.createdAt).slice(0, limit);
  return attachHistoryMetadata(rows, soulBinding?.reader.metadata() || CATCHING_UP);
}

/** Read a Soul's activity from the local store. Catch-up state rides on the result. */
export async function fetchSoulHistory(soul, { limit = 50 } = {}) {
  if (!soul?.agentId) return [];
  await subscribeToSoulFactoryUpdates();
  const store = getEventStore();
  if (!store) return [];
  const history = soulHistoryRows(soulHistoryEvents(store, soul), soul, limit);
  rememberReadModelMeta('history', history);
  return history;
}

/**
 * Follow a Soul's activity: delivers the cached history at once, then again
 * whenever its events or the relay catch-up state change.
 */
export function subscribeToSoulHistory(soul, { limit = 50, onUpdate } = {}) {
  if (!soul?.agentId) return () => {};
  let signature = null;
  const publish = () => {
    const store = getEventStore();
    if (!store) return;
    const events = soulHistoryEvents(store, soul);
    const history = soulHistoryRows(events, soul, limit);
    const next = `${history.complete}|${events.map((event) => event.id).sort().join(',')}`;
    if (next === signature) return;
    signature = next;
    rememberReadModelMeta('history', history);
    onUpdate?.(history);
  };
  publish();
  soulListeners.add(publish);
  const offRefresh = onStoreRefresh(publish);
  return () => { offRefresh(); soulListeners.delete(publish); };
}
