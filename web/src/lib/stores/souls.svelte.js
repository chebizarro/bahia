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
import { ensureRelayConnection } from '$lib/nostr/client.js';
import api from '$lib/api/client.js';
import { createReadModelMetadataTracker } from '$lib/nostr/pool-utils.js';

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
// by the Bahia API (non-secret). Empty means unknown (older server or
// SoulFactory disabled), in which case discovery falls back to capability-only.
export const serverAgentRuntimes = $state([]);

// serverPolicyKnown becomes true only after a successful policy fetch. Unknown
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

function rememberSoulFactoryReadModelMeta(metadata) {
  for (const key of ['souls', 'templates', 'drafts', 'capabilities']) {
    rememberReadModelMeta(key, metadata);
  }
}

function knownPoolRelays() {
  if (typeof nostr.getConnectedRelays === 'function') return nostr.getConnectedRelays();
  if (typeof nostr.getRelays === 'function') return nostr.getRelays();
  return [];
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

// Fetches the administratively enabled agent runtime list from the Bahia API.
// On failure the policy stays unknown and target discovery fails closed rather
// than exposing runtimes the server has not enabled.
export async function refreshServerAgentRuntimes() {
  if (!api) return;
  try {
    const data = await api.fetch('/soulfactory/runtimes');
    replaceStateArray(serverAgentRuntimes, Array.isArray(data?.agent_runtimes) ? data.agent_runtimes : []);
    serverPolicy.known = true;
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

let soulFactorySubscription = null;

function applySoulFactoryEvent(event) {
  if (event.kind === KINDS.AGENT_SOUL) {
    applyReplaceableUpdate(soulEvents, event, souls, parseSoulEvent, newestFirst);
  } else if (event.kind === KINDS.SOUL_TEMPLATE) {
    applyReplaceableUpdate(templateEvents, event, templates, parseTemplateEvent, templateSort);
  } else if (event.kind === KINDS.SOUL_DRAFT) {
    applyReplaceableUpdate(draftEvents, event, drafts, parseSoulDraftEvent, newestFirst);
  } else if (event.kind === KINDS.RUNTIME_CAPABILITY) {
    applyReplaceableUpdate(capabilityEvents, event, runtimeCapabilities, parseRuntimeCapabilityEvent, capabilitySort);
  }
}

// Single persistent subscription for all soul factory read models.
// Receives stored events, marks ready on EOSE, keeps listening for live updates.
export async function subscribeToSoulFactoryUpdates(options = null) {
  if (soulFactorySubscription) return;

  // Accept either a bare author pubkey string or an options object. Callers such as
  // loadRuntimeCapabilities({ method }) pass an options object; only a real pubkey
  // string may ever reach the relay `authors` filter, which must be an array of
  // strings (a non-string here triggers a relay "parse error ... of authors").
  const authorPubkey = typeof options === 'string'
    ? options
    : (options && typeof options === 'object' ? (options.authorPubkey || options.author || null) : null);

  loading.souls = true;
  loading.templates = true;
  loading.drafts = true;
  loading.capabilities = true;
  error.value = null;

  // Server policy is advisory context, not a subscription gate.
  refreshServerAgentRuntimes();

  try {
    await ensureRelayConnection();
  } catch (err) {
    console.error('[souls] Failed to connect:', err);
    error.value = err.message;
    loading.souls = false;
    loading.templates = false;
    loading.drafts = false;
    loading.capabilities = false;
    return;
  }

  const soulFilter = { kinds: [KINDS.AGENT_SOUL, KINDS.SOUL_TEMPLATE, KINDS.SOUL_DRAFT] };
  if (typeof authorPubkey === 'string' && authorPubkey) soulFilter.authors = [authorPubkey];

  const tracker = createReadModelMetadataTracker({ relays: knownPoolRelays() });
  const finishHistoricalCatchup = () => {
    const metadata = tracker.metadata();
    rememberSoulFactoryReadModelMeta(metadata);
    loading.souls = false;
    loading.templates = false;
    loading.drafts = false;
    loading.capabilities = false;
  };

  soulFactorySubscription = nostr.subscribe([
    soulFilter,
    { kinds: [KINDS.RUNTIME_CAPABILITY] }
  ], {
    onEvent: (event, relay) => {
      tracker.markEvent(event, relay);
      applySoulFactoryEvent(event);
    },
    onEose: (relay) => {
      tracker.markEose(relay);
      if (tracker.isComplete()) finishHistoricalCatchup();
    },
    onClosed: (reason, relay, meta) => {
      tracker.markClosed(reason, relay, meta);
      console.warn(`[souls] Subscription closed by ${relay}: ${reason}`);
      if (tracker.isTerminal()) finishHistoricalCatchup();
    },
    onAuth: (challenge, relay) => {
      tracker.markAuth(challenge, relay);
    }
  });
}

// Backward-compatible aliases
export const loadSouls = subscribeToSoulFactoryUpdates;
export const loadTemplates = subscribeToSoulFactoryUpdates;
export const loadDrafts = subscribeToSoulFactoryUpdates;
export const loadRuntimeCapabilities = subscribeToSoulFactoryUpdates;
export const loadAll = subscribeToSoulFactoryUpdates;
export const subscribeToSoulUpdates = subscribeToSoulFactoryUpdates;

export function unsubscribeFromSoulUpdates() {
  if (soulFactorySubscription) {
    soulFactorySubscription();
    soulFactorySubscription = null;
  }
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
    data: data && typeof data === 'object' ? data : {},
    legacyKind: event.kind === KINDS.SOUL_ACTION_LEGACY_RESULT,
    event
  };
}

// Track a provisioning or lifecycle run. Terminal state comes only from explicit 7950
// (or legacy 1951 migration alias) result events, never from EOSE, CLOSED, or local time.
export function trackLifecycleRun(requestEventId, { type = 'provisioning', action = '', onProgress, onComplete, onError } = {}) {
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
    closedRelays: []
  });

  provisioningRuns.set(requestEventId, run);

  const seenEventIds = new Set();
  let finished = false;
  const resultKinds = [KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT].filter(Boolean);

  const unsub = nostr.subscribe([
    { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
    { kinds: resultKinds, '#e': [requestEventId] }
  ], {
    onEvent: (event) => {
      if (finished) return;
      if (event?.id && seenEventIds.has(event.id)) return;
      if (event?.id) seenEventIds.add(event.id);

      if (event.kind === KINDS.PROVISIONING_STATUS) {
        const status = parseRunStatusEvent(event, defaultTotal);
        const currentRun = provisioningRuns.get(requestEventId);
        if (currentRun) {
          currentRun.status = 'running';
          currentRun.step = status.step;
          currentRun.progress = status.progress;
          currentRun.message = status.message;
          currentRun.action = currentRun.action || status.action;
          currentRun.statusEvents.push(status);
        }

        if (onProgress) onProgress({ step: status.step, progress: status.progress, message: status.message });
        return;
      }

      if (resultKinds.includes(event.kind)) {
        const result = parseRunResultEvent(event);
        const currentRun = provisioningRuns.get(requestEventId);
        if (currentRun) {
          currentRun.status = result.success ? 'completed' : 'failed';
          currentRun.action = currentRun.action || result.action;
          currentRun.message = result.success
            ? (type === 'provisioning' ? 'Provisioning complete' : 'Lifecycle action complete')
            : result.error;
          currentRun.result = result;
        }

        finished = true;
        unsub();

        if (result.success) {
          if (onComplete) onComplete(result.data);
        } else if (onError) {
          onError(result.error);
        }
      }
    },
    onEose: () => {
      if (finished) return;
      const currentRun = provisioningRuns.get(requestEventId);
      if (currentRun && currentRun.status === 'pending') {
        currentRun.message = type === 'provisioning'
          ? 'Request published. Waiting for live provisioning updates…'
          : 'Request published. Waiting for live lifecycle updates…';
      }
    },
    onClosed: (reason, relay) => {
      if (finished) return;
      const currentRun = provisioningRuns.get(requestEventId);
      if (currentRun) {
        currentRun.closedRelays.push({ relay, reason });
        currentRun.message = reason
          ? `Relay closed this subscription: ${reason}. Waiting for an explicit ${type} result…`
          : `Relay closed this subscription. Waiting for an explicit ${type} result…`;
      }
    }
  });

  return () => {
    finished = true;
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
  applyReplaceableUpdate(draftEvents, signedEvent, drafts, parseSoulDraftEvent, newestFirst);

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

export async function fetchSoulHistory(soul, { limit = 50 } = {}) {
  if (!soul?.agentId) return [];

  const soulRef = buildSoulRef(soul);
  const lifecycleKinds = [KINDS.PROVISIONING_STATUS, KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT].filter(Boolean);
  const filters = [
    { kinds: [KINDS.AGENT_SOUL], '#d': [soul.agentId], limit },
    // NIP-01 relay filters only support single-letter tag indexes. The SoulFactory
    // compatibility tags use `soul`, so query bounded recent kind sets and apply
    // the multi-letter tag match locally instead of sending an invalid `#soul` filter.
    { kinds: [KINDS.SOUL_ACTION], limit },
    { kinds: lifecycleKinds, limit }
  ];

  return new Promise((resolve) => {
    const events = [];
    const tracker = createReadModelMetadataTracker({
      relays: knownPoolRelays(),
      partialEventCount: () => events.length
    });
    let settled = false;
    let unsubscribe = null;
    let unsubscribeAfterAssign = false;

    const buildHistory = () => {
      const deduped = new Map();
      for (const event of events) {
        if (deduped.has(event.id)) continue;
        if (event.kind !== KINDS.AGENT_SOUL && getTag(event, 'soul') !== soulRef) continue;
        deduped.set(event.id, summarizeHistoryEvent(event, soulRef));
      }
      return Array.from(deduped.values()).sort((a, b) => b.createdAt - a.createdAt);
    };

    const settle = () => {
      if (settled) return;
      settled = true;
      const metadata = tracker.metadata();
      const history = attachHistoryMetadata(buildHistory(), metadata);
      rememberReadModelMeta('history', history);
      resolve(history);
      if (unsubscribe) unsubscribe();
      else unsubscribeAfterAssign = true;
    };

    unsubscribe = nostr.subscribe(filters, {
      onEvent: (event, relay) => {
        tracker.markEvent(event, relay);
        events.push(event);
      },
      onEose: (relay) => {
        tracker.markEose(relay);
        if (tracker.isComplete()) settle();
      },
      onClosed: (reason, relay, meta) => {
        tracker.markClosed(reason, relay, meta);
        if (tracker.isTerminal()) settle();
      },
      onAuth: (challenge, relay) => {
        tracker.markAuth(challenge, relay);
      }
    });
    if (unsubscribeAfterAssign) unsubscribe?.();
  });
}
