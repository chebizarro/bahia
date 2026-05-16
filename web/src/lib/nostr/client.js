// Nostr client for Soul Factory web UI
// Uses nostr-tools for WebSocket relay connections

import { writable, derived, get } from 'svelte/store';

const HEX_64 = /^[0-9a-f]{64}$/;
const HEX_128 = /^[0-9a-f]{128}$/;
const MAX_FUTURE_SKEW_SECONDS = 10 * 60;
const MAX_PAST_SKEW_SECONDS = 365 * 24 * 60 * 60;

function currentUnixTime() {
  return Math.floor(Date.now() / 1000);
}

function openReadyState() {
  return typeof WebSocket !== 'undefined' ? WebSocket.OPEN : 1;
}

function isStringTagValue(value) {
  return typeof value === 'string';
}

function relaySummaryFromStates(relayStates) {
  return Array.from(relayStates.entries()).map(([relay, state]) => ({ relay, ...state }));
}

export class NostrIncompleteEOSEError extends Error {
  constructor(reason, { partialEvents = [], relaySummary = [], message = '' } = {}) {
    super(message || `Nostr query did not receive complete EOSE history: ${reason}`);
    this.name = 'NostrIncompleteEOSEError';
    this.reason = reason;
    this.partialEvents = partialEvents;
    this.relaySummary = relaySummary;
  }
}

async function sha256Hex(input) {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle || typeof subtle.digest !== 'function') {
    throw new Error('crypto.subtle is unavailable for Nostr event hash validation');
  }
  const digest = await subtle.digest('SHA-256', new TextEncoder().encode(input));
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('');
}

export async function validateInboundNostrEvent(event, { now = currentUnixTime() } = {}) {
  if (!event || typeof event !== 'object' || Array.isArray(event)) {
    throw new Error('event must be an object');
  }
  if (typeof event.id !== 'string' || !HEX_64.test(event.id)) {
    throw new Error('event id must be 64 lowercase hex characters');
  }
  if (typeof event.pubkey !== 'string' || !HEX_64.test(event.pubkey)) {
    throw new Error('event pubkey must be 64 lowercase hex characters');
  }
  if (typeof event.sig !== 'string' || !HEX_128.test(event.sig)) {
    throw new Error('event signature must be 128 lowercase hex characters');
  }
  if (!Number.isInteger(event.kind) || event.kind < 0) {
    throw new Error('event kind must be an integer');
  }
  if (!Number.isInteger(event.created_at)) {
    throw new Error('event created_at must be an integer');
  }
  if (event.created_at > now + MAX_FUTURE_SKEW_SECONDS) {
    throw new Error('event created_at is too far in the future');
  }
  if (event.created_at < now - MAX_PAST_SKEW_SECONDS) {
    throw new Error('event created_at is too far in the past');
  }
  if (typeof event.content !== 'string') {
    throw new Error('event content must be a string');
  }
  if (!Array.isArray(event.tags)) {
    throw new Error('event tags must be an array');
  }
  for (const tag of event.tags) {
    if (!Array.isArray(tag) || tag.some((value) => !isStringTagValue(value))) {
      throw new Error('event tags must be arrays of strings');
    }
  }

  const serialized = JSON.stringify([
    0,
    event.pubkey,
    event.created_at,
    event.kind,
    event.tags,
    event.content
  ]);
  const computedId = await sha256Hex(serialized);
  if (computedId !== event.id) {
    throw new Error('event id does not match NIP-01 hash');
  }

  // Browser WebCrypto does not expose Schnorr verification. The browser trust boundary
  // therefore fails closed on malformed signatures and verifies deterministic event IDs;
  // backend validation performs full signature checks before persistence/dispatch.
  return true;
}

// Soul Factory event kinds
export const KINDS = {
  SOUL_TEMPLATE: 31950,
  AGENT_SOUL: 31951,
  SOUL_DRAFT: 31952,
  PROVISIONING_REQUEST: 5950,
  PROVISIONING_STATUS: 6950,
  PROVISIONING_RESULT: 7950,
  SOUL_ACTION: 1950,
  SOUL_ACTION_LEGACY_RESULT: 1951,
  RUNTIME_CAPABILITY: 30317,
  RUNTIME_CONTROL_REQUEST: 38384,
  RUNTIME_CONTROL_RESULT: 38386,
  REPOSITORY: 30617,

  // Bahia canonical control-plane/read-model kinds
  BAHIA_REQUEST_DEPLOY: 5961,
  BAHIA_REQUEST_ROLLBACK: 5962,
  BAHIA_REQUEST_SERVICE_ACTION: 5963,
  BAHIA_REQUEST_SERVICE_CREATE: 5964,
  BAHIA_REQUEST_ENVIRONMENT_CREATE: 5965,
  BAHIA_REQUEST_DEPLOYMENT_APPROVAL: 5966,
  BAHIA_REQUEST_OBSERVATION_SUBMIT: 5967,
  BAHIA_REQUEST_DRIFT_REMEDIATE: 5968,
  BAHIA_REQUEST_LLM_ROUTE_CREATE: 5971,
  BAHIA_REQUEST_LLM_RELEASE_REGISTER: 5972,
  BAHIA_REQUEST_LLM_DEPLOY: 5973,
  BAHIA_REQUEST_LLM_DEPLOYMENT_APPROVAL: 5974,
  BAHIA_REQUEST_LLM_ROLLBACK: 5975,
  BAHIA_REQUEST_SERVICE_UPDATE: 5981,
  BAHIA_REQUEST_SERVICE_DELETE: 5982,
  BAHIA_REQUEST_ENVIRONMENT_UPDATE: 5983,
  BAHIA_REQUEST_ENVIRONMENT_DELETE: 5984,
  BAHIA_REQUEST_ARTIFACT_REGISTER: 5985,
  BAHIA_REQUEST_POLICY_CREATE: 5986,
  BAHIA_REQUEST_POLICY_UPDATE: 5987,
  BAHIA_REQUEST_POLICY_DELETE: 5988,
  BAHIA_REQUEST_POLICY_EVALUATE: 5989,
  BAHIA_DEPLOYMENT_STATUS: 6961,
  BAHIA_SERVICE_STATUS: 6962,
  BAHIA_LLM_DEPLOYMENT_STATUS: 6973,
  BAHIA_DEPLOYMENT_RESULT: 7961,
  BAHIA_ACTION_RESULT: 7962,
  BAHIA_SERVICE_CREATE_RESULT: 7963,
  BAHIA_ENVIRONMENT_CREATE_RESULT: 7964,
  BAHIA_OBSERVATION_RESULT: 7965,
  BAHIA_REMEDIATION_RESULT: 7966,
  BAHIA_LLM_ROUTE_CREATE_RESULT: 7971,
  BAHIA_LLM_RELEASE_REGISTER_RESULT: 7972,
  BAHIA_LLM_DEPLOYMENT_RESULT: 7973,
  BAHIA_SERVICE_STATE: 31961,
  BAHIA_SERVICE_REGISTRY: 31962,
  BAHIA_ENVIRONMENT_REGISTRY: 31963,
  BAHIA_LLM_ROUTE_REGISTRY: 31964,
  BAHIA_LLM_ROUTE_STATE: 31965,
  BAHIA_ARTIFACT_REGISTRY: 31966,
  BAHIA_DEPLOYMENT_INTENT_REGISTRY: 31967,
  BAHIA_DEPLOYMENT_RUN_REGISTRY: 31968,
  BAHIA_BUILD_REGISTRY: 31969,
  BAHIA_POLICY_REGISTRY: 31970,
  BAHIA_SYSTEM_DISCOVERY: 31974,
  NIP51_RELAY_SET: 30002,
  LOOM_WORKER_AD: 10100,

  // AI/ML Fabric read-model kinds (31980-31989)
  BAHIA_ML_MODEL_REGISTRY: 31980,
  BAHIA_ML_MODEL_VERSION_REGISTRY: 31981,
  BAHIA_ML_DATASET_REGISTRY: 31982,
  BAHIA_ML_RECIPE_REGISTRY: 31983,
  BAHIA_ML_RECIPE_RUN_STATE: 31984,
  BAHIA_ML_ENDPOINT_REGISTRY: 31985,
  BAHIA_ML_ENDPOINT_STATE: 31986,
  BAHIA_ML_EVALUATION_STATE: 31987,
  BAHIA_ML_ARTIFACT_PROVENANCE: 31988,
  BAHIA_ML_RUNTIME_CAPABILITY: 31989,

  // Operator assistant event kinds
  ASSISTANT_SESSION: 31990,
  ASSISTANT_PROMPT_REQUEST: 38420,
  ASSISTANT_APPROVAL: 38421,
  ASSISTANT_STATUS: 38422,
  ASSISTANT_RESULT: 38423,
  
  // SBOM Attestation/Index kinds (NIP-51 style parameterized lists)
  SBOM_ATTESTATION: 30078,
  SBOM_INDEX: 30079
};

export const SOUL_FACTORY_RUNTIME_CONTROL_SCHEMA = 'soulfactory-runtime-control/v1';
export const SOUL_FACTORY_RUNTIME_CAPABILITY_SCHEMA = 'soulfactory-runtime-capability/v1';

export const SOUL_RUNTIME_TARGETS = {
  OPENCLAW: 'openclaw',
  METIQ: 'metiq'
};

export const SOUL_LIFECYCLE_ACTIONS = {
  SUSPEND: 'suspend',
  RESUME: 'resume',
  REVOKE: 'revoke',
  REGENERATE: 'regenerate',
  REDEPLOY: 'redeploy',
  UPDATE: 'update'
};

export const SOUL_RUNTIME_METHODS = {
  PROVISION: 'soulfactory.provision',
  UPDATE: 'soulfactory.update',
  SUSPEND: 'soulfactory.suspend',
  RESUME: 'soulfactory.resume',
  REDEPLOY: 'soulfactory.redeploy',
  REVOKE: 'soulfactory.revoke'
};

export function isLifecycleResultKind(kind) {
  return kind === KINDS.PROVISIONING_RESULT || kind === KINDS.SOUL_ACTION_LEGACY_RESULT;
}

export function canonicalLifecycleResultKind(kind) {
  return kind === KINDS.SOUL_ACTION_LEGACY_RESULT ? KINDS.PROVISIONING_RESULT : kind;
}

export const BAHIA_KINDS = {
  DEPLOY_REQUEST: KINDS.BAHIA_REQUEST_DEPLOY,
  ROLLBACK_REQUEST: KINDS.BAHIA_REQUEST_ROLLBACK,
  SERVICE_ACTION: KINDS.BAHIA_REQUEST_SERVICE_ACTION,
  SERVICE_CREATE: KINDS.BAHIA_REQUEST_SERVICE_CREATE,
  ENVIRONMENT_CREATE: KINDS.BAHIA_REQUEST_ENVIRONMENT_CREATE,
  DEPLOYMENT_APPROVAL: KINDS.BAHIA_REQUEST_DEPLOYMENT_APPROVAL,
  OBSERVATION_SUBMIT: KINDS.BAHIA_REQUEST_OBSERVATION_SUBMIT,
  DRIFT_REMEDIATE: KINDS.BAHIA_REQUEST_DRIFT_REMEDIATE,
  LLM_ROUTE_CREATE: KINDS.BAHIA_REQUEST_LLM_ROUTE_CREATE,
  LLM_RELEASE_REGISTER: KINDS.BAHIA_REQUEST_LLM_RELEASE_REGISTER,
  LLM_DEPLOY_REQUEST: KINDS.BAHIA_REQUEST_LLM_DEPLOY,
  LLM_DEPLOYMENT_APPROVAL: KINDS.BAHIA_REQUEST_LLM_DEPLOYMENT_APPROVAL,
  LLM_ROLLBACK_REQUEST: KINDS.BAHIA_REQUEST_LLM_ROLLBACK,
  SERVICE_UPDATE: KINDS.BAHIA_REQUEST_SERVICE_UPDATE,
  SERVICE_DELETE: KINDS.BAHIA_REQUEST_SERVICE_DELETE,
  ENVIRONMENT_UPDATE: KINDS.BAHIA_REQUEST_ENVIRONMENT_UPDATE,
  ENVIRONMENT_DELETE: KINDS.BAHIA_REQUEST_ENVIRONMENT_DELETE,
  ARTIFACT_REGISTER: KINDS.BAHIA_REQUEST_ARTIFACT_REGISTER,
  POLICY_CREATE: KINDS.BAHIA_REQUEST_POLICY_CREATE,
  POLICY_UPDATE: KINDS.BAHIA_REQUEST_POLICY_UPDATE,
  POLICY_DELETE: KINDS.BAHIA_REQUEST_POLICY_DELETE,
  POLICY_EVALUATE: KINDS.BAHIA_REQUEST_POLICY_EVALUATE,
  DEPLOYMENT_STATUS: KINDS.BAHIA_DEPLOYMENT_STATUS,
  SERVICE_STATUS: KINDS.BAHIA_SERVICE_STATUS,
  LLM_DEPLOYMENT_STATUS: KINDS.BAHIA_LLM_DEPLOYMENT_STATUS,
  DEPLOYMENT_RESULT: KINDS.BAHIA_DEPLOYMENT_RESULT,
  ACTION_RESULT: KINDS.BAHIA_ACTION_RESULT,
  SERVICE_CREATE_RESULT: KINDS.BAHIA_SERVICE_CREATE_RESULT,
  ENVIRONMENT_CREATE_RESULT: KINDS.BAHIA_ENVIRONMENT_CREATE_RESULT,
  OBSERVATION_RESULT: KINDS.BAHIA_OBSERVATION_RESULT,
  REMEDIATION_RESULT: KINDS.BAHIA_REMEDIATION_RESULT,
  LLM_ROUTE_CREATE_RESULT: KINDS.BAHIA_LLM_ROUTE_CREATE_RESULT,
  LLM_RELEASE_REGISTER_RESULT: KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT,
  LLM_DEPLOYMENT_RESULT: KINDS.BAHIA_LLM_DEPLOYMENT_RESULT,
  SERVICE_STATE: KINDS.BAHIA_SERVICE_STATE,
  SERVICE_REGISTRY: KINDS.BAHIA_SERVICE_REGISTRY,
  ENVIRONMENT_REGISTRY: KINDS.BAHIA_ENVIRONMENT_REGISTRY,
  LLM_ROUTE_REGISTRY: KINDS.BAHIA_LLM_ROUTE_REGISTRY,
  LLM_ROUTE_STATE: KINDS.BAHIA_LLM_ROUTE_STATE,
  ARTIFACT_REGISTRY: KINDS.BAHIA_ARTIFACT_REGISTRY,
  DEPLOYMENT_INTENT_REGISTRY: KINDS.BAHIA_DEPLOYMENT_INTENT_REGISTRY,
  DEPLOYMENT_RUN_REGISTRY: KINDS.BAHIA_DEPLOYMENT_RUN_REGISTRY,
  BUILD_REGISTRY: KINDS.BAHIA_BUILD_REGISTRY,
  POLICY_REGISTRY: KINDS.BAHIA_POLICY_REGISTRY,
  SYSTEM_DISCOVERY: KINDS.BAHIA_SYSTEM_DISCOVERY,
  RELAY_SET: KINDS.NIP51_RELAY_SET,
  WORKER_ADVERTISEMENT: KINDS.LOOM_WORKER_AD,
  AUDIT_MIN: 31000,
  AUDIT_MAX: 31099
};

export const BAHIA_READ_MODEL_KINDS = [
  KINDS.BAHIA_SERVICE_REGISTRY,
  KINDS.BAHIA_ENVIRONMENT_REGISTRY,
  KINDS.BAHIA_SERVICE_STATE,
  KINDS.BAHIA_LLM_ROUTE_REGISTRY,
  KINDS.BAHIA_LLM_ROUTE_STATE,
  KINDS.BAHIA_ARTIFACT_REGISTRY,
  KINDS.BAHIA_DEPLOYMENT_INTENT_REGISTRY,
  KINDS.BAHIA_DEPLOYMENT_RUN_REGISTRY,
  KINDS.BAHIA_BUILD_REGISTRY,
  KINDS.BAHIA_POLICY_REGISTRY,
  KINDS.LOOM_WORKER_AD,
  // AI/ML Fabric
  KINDS.BAHIA_ML_MODEL_REGISTRY,
  KINDS.BAHIA_ML_MODEL_VERSION_REGISTRY,
  KINDS.BAHIA_ML_ENDPOINT_REGISTRY,
  KINDS.BAHIA_ML_ENDPOINT_STATE,
  KINDS.BAHIA_ML_RUNTIME_CAPABILITY
];

export const BAHIA_STATUS_KINDS = [
  KINDS.BAHIA_DEPLOYMENT_STATUS,
  KINDS.BAHIA_SERVICE_STATUS,
  KINDS.BAHIA_LLM_DEPLOYMENT_STATUS,
  KINDS.BAHIA_DEPLOYMENT_RESULT,
  KINDS.BAHIA_ACTION_RESULT,
  KINDS.BAHIA_SERVICE_CREATE_RESULT,
  KINDS.BAHIA_ENVIRONMENT_CREATE_RESULT,
  KINDS.BAHIA_OBSERVATION_RESULT,
  KINDS.BAHIA_REMEDIATION_RESULT,
  KINDS.BAHIA_LLM_ROUTE_CREATE_RESULT,
  KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT,
  KINDS.BAHIA_LLM_DEPLOYMENT_RESULT
];

export const BAHIA_SBOM_KINDS = [
  KINDS.SBOM_ATTESTATION,
  KINDS.SBOM_INDEX
];

export const BAHIA_AUDIT_KINDS = Array.from({ length: 100 }, (_, i) => 31000 + i);

export const BAHIA_CONTROLPLANE_KINDS = [
  ...BAHIA_READ_MODEL_KINDS,
  ...BAHIA_STATUS_KINDS,
  ...BAHIA_AUDIT_KINDS,
  ...BAHIA_SBOM_KINDS
];

// Operator assistant kinds are intentionally kept separate from the canonical
// Bahia control-plane arrays. Assistant subscriptions compose these explicitly.
export const ASSISTANT_KINDS = {
  SESSION: KINDS.ASSISTANT_SESSION,
  PROMPT_REQUEST: KINDS.ASSISTANT_PROMPT_REQUEST,
  APPROVAL: KINDS.ASSISTANT_APPROVAL,
  STATUS: KINDS.ASSISTANT_STATUS,
  RESULT: KINDS.ASSISTANT_RESULT
};

export const ASSISTANT_EVENT_KINDS = Object.values(ASSISTANT_KINDS);

export const ASSISTANT_SESSION_STATES = {
  IDLE: 'idle',
  PLANNING: 'planning',
  AWAITING_APPROVAL: 'awaiting_approval',
  EXECUTING: 'executing',
  BLOCKED: 'blocked',
  COMPLETED: 'completed',
  FAILED: 'failed'
};

export const ASSISTANT_RESULT_STATUSES = {
  COMPLETED: 'completed',
  BLOCKED: 'blocked',
  FAILED: 'failed',
  REJECTED: 'rejected',
  CANCELLED: 'cancelled',
  NEEDS_CLARIFICATION: 'needs_clarification'
};

// Default relays - can be overridden via localStorage or connect() parameter
const DEFAULT_RELAYS = [
  'wss://relay.sharegap.net',
  'wss://relay.primal.net',
  'wss://nos.lol'
];

// Storage key for user-configured relays
const RELAY_CONFIG_KEY = 'bahia_nostr_relays';

// Reconnection configuration
const MAX_RECONNECT_ATTEMPTS = 5;
const INITIAL_BACKOFF_MS = 1000;
const MAX_BACKOFF_MS = 30000;

/**
 * Get configured relays from localStorage or return defaults
 */
function getConfiguredRelays() {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.getItem !== 'function') return DEFAULT_RELAYS;
  
  try {
    const stored = localStorage.getItem(RELAY_CONFIG_KEY);
    if (stored) {
      const relays = JSON.parse(stored);
      if (Array.isArray(relays) && relays.length > 0) {
        return relays;
      }
    }
  } catch (e) {
    console.error('[nostr] Failed to load relay config:', e);
  }
  
  return DEFAULT_RELAYS;
}

/**
 * Save relay configuration to localStorage
 */
export function saveRelayConfig(relays) {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.setItem !== 'function') return;
  
  try {
    if (Array.isArray(relays) && relays.length > 0) {
      localStorage.setItem(RELAY_CONFIG_KEY, JSON.stringify(relays));
    } else {
      localStorage.removeItem(RELAY_CONFIG_KEY);
    }
  } catch (e) {
    console.error('[nostr] Failed to save relay config:', e);
  }
}

/**
 * Get the default relay list
 */
export function getDefaultRelays() {
  return [...DEFAULT_RELAYS];
}

export function getTagValues(event, name) {
  if (!event || !Array.isArray(event.tags)) return [];
  return event.tags
    .filter(tag => Array.isArray(tag) && tag[0] === name && tag[1])
    .map(tag => tag[1]);
}

export function getTagValue(event, name, fallback = '') {
  const values = getTagValues(event, name);
  return values.length > 0 ? values[values.length - 1] : fallback;
}

export function getDTag(event) {
  return getTagValue(event, 'd', '');
}

export function replaceableKey(event) {
  if (!event || !event.kind || !event.pubkey) return '';
  const d = getDTag(event);
  return d ? `${event.kind}:${event.pubkey}:${d}` : `${event.kind}:${event.pubkey}`;
}

export function parseJsonContent(event, fallback = {}) {
  if (!event || !event.content) return fallback;
  try {
    return JSON.parse(event.content);
  } catch {
    return fallback;
  }
}

function getTaggedEventRef(event, marker = 'reply') {
  const tag = (event?.tags || []).find((candidate) =>
    Array.isArray(candidate) && candidate[0] === 'e' && candidate[1] && (!marker || candidate[3] === marker)
  );
  return tag?.[1] || '';
}

function getTaggedPubkeyRef(event, role = '') {
  const tag = (event?.tags || []).find((candidate) =>
    Array.isArray(candidate) && candidate[0] === 'p' && candidate[1] && (!role || candidate[3] === role)
  );
  return tag?.[1] || '';
}

export function parseAssistantSessionEvent(event) {
  if (!event || event.kind !== KINDS.ASSISTANT_SESSION) return null;
  const content = parseJsonContent(event, {});
  const sessionId = getTagValue(event, 'session', content.session_id || getDTag(event));
  const state = getTagValue(event, 'status', content.state || ASSISTANT_SESSION_STATES.IDLE);

  return {
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    sessionId,
    state,
    operatorPubkey: getTaggedPubkeyRef(event, 'operator') || content.operator_pubkey || '',
    assistantId: getTagValue(event, 'agent', content.assistant_id || ''),
    assistantPubkey: content.assistant_pubkey || '',
    currentTurnId: content.current_turn_id || '',
    currentRequestId: content.current_request_id || '',
    lastPlanHash: content.last_plan_hash || '',
    currentPlan: content.current_plan || null,
    pendingSteps: Array.isArray(content.pending_steps) ? content.pending_steps : [],
    transcriptSummary: content.transcript_summary || '',
    lastResultId: content.last_result_id || '',
    content,
    event
  };
}

export function parseAssistantStatusEvent(event) {
  if (!event || event.kind !== KINDS.ASSISTANT_STATUS) return null;
  const content = parseJsonContent(event, {});
  const status = getTagValue(event, 'status', content.status || '');

  return {
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    sessionId: getTagValue(event, 'session', content.session_id || ''),
    assistantId: getTagValue(event, 'agent', content.assistant_id || ''),
    status,
    requestEventId: getTaggedEventRef(event, 'reply') || content.request_event_id || '',
    planHash: getTagValue(event, 'plan-hash', content.plan_hash || ''),
    stepId: getTagValue(event, 'step', content.step_id || ''),
    downstreamRequestId: getTagValue(event, 'downstream-request', content.downstream_request_id || ''),
    message: content.message || event.content || '',
    plan: content.plan || null,
    receipt: content.receipt || null,
    content,
    event
  };
}

export function parseAssistantResultEvent(event) {
  if (!event || event.kind !== KINDS.ASSISTANT_RESULT) return null;
  const content = parseJsonContent(event, {});
  const status = getTagValue(event, 'status', content.status || '');

  return {
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    sessionId: getTagValue(event, 'session', content.session_id || ''),
    assistantId: getTagValue(event, 'agent', content.assistant_id || ''),
    status,
    requestEventId: getTaggedEventRef(event, 'reply') || content.request_event_id || '',
    planHash: getTagValue(event, 'plan-hash', content.plan_hash || ''),
    downstreamRequestId: getTagValue(event, 'downstream-request', content.downstream_request_id || ''),
    success: status === ASSISTANT_RESULT_STATUSES.COMPLETED || content.success === true,
    blocked: status === ASSISTANT_RESULT_STATUSES.BLOCKED,
    failed: status === ASSISTANT_RESULT_STATUSES.FAILED || content.success === false,
    rejected: status === ASSISTANT_RESULT_STATUSES.REJECTED,
    cancelled: status === ASSISTANT_RESULT_STATUSES.CANCELLED,
    needsClarification: status === ASSISTANT_RESULT_STATUSES.NEEDS_CLARIFICATION,
    summary: content.summary || content.message || event.content || '',
    error: content.error || '',
    downstreamResults: Array.isArray(content.downstream_results) ? content.downstream_results : [],
    usage: content.usage || null,
    content,
    event
  };
}

export function normalizeSoulDraftContent(content = {}) {
  const identity = content.identity || {};
  const permissions = content.permissions || {};
  const runtime = content.runtime || {};
  const relayPolicy = content.relay_policy || content.relayPolicy || {};
  const workspace = content.workspace || {};
  const assets = content.assets || {};
  const avatar = content.avatar || content.avatar_spec || content.avatarSpec || null;
  const voice = content.voice || content.voice_spec || content.voiceSpec || null;
  const memory = content.memory || content.memory_spec || content.memorySpec || null;
  const persona = content.persona || content.persona_spec || content.personaSpec || null;
  const avatarRef = assets.avatar_ref || assets.avatarRef || content.avatar_ref || (
    avatar?.current === 'uploaded'
      ? avatar?.uploaded_ref || avatar?.uploadedRef || avatar?.generated_ref || avatar?.generatedRef
      : avatar?.generated_ref || avatar?.generatedRef || avatar?.uploaded_ref || avatar?.uploadedRef
  ) || '';

  return {
    ...content,
    ...(avatar ? { avatar } : {}),
    ...(voice ? { voice } : {}),
    ...(memory ? { memory } : {}),
    ...(persona ? { persona } : {}),
    identity: {
      ...identity,
      name: identity.name || content.name || '',
      purpose: identity.purpose || content.purpose || content.brief || '',
      tier: identity.tier || content.tier || 'standard',
      nip05: identity.nip05 || content.nip05 || ''
    },
    runtime: {
      ...runtime,
      target: runtime.target || runtime.runtime || '',
      runtime_pubkey: runtime.runtime_pubkey || runtime.runtimePubkey || '',
      capability_ref: runtime.capability_ref || runtime.capabilityRef || '',
      runtime_binding: runtime.runtime_binding || runtime.runtimeBinding || '',
      state: runtime.state || ''
    },
    permissions: {
      ...permissions,
      allowed_kinds: permissions.allowed_kinds || permissions.allowedKinds || content.allowed_kinds || [],
      tool_grants: permissions.tool_grants || permissions.toolGrants || content.tool_grants || [],
      approval_policy: permissions.approval_policy || permissions.approvalPolicy || ''
    },
    relay_policy: {
      read: relayPolicy.read || [],
      write: relayPolicy.write || [],
      control: relayPolicy.control || [],
      nip65_discovery: relayPolicy.nip65_discovery ?? relayPolicy.nip65Discovery ?? false
    },
    workspace: {
      ...workspace,
      repo: workspace.repo || workspace.repository || '',
      branch: workspace.branch || '',
      environment: workspace.environment || ''
    },
    assets: {
      ...assets,
      avatar_ref: avatarRef,
      voice_ref: assets.voice_ref || assets.voiceRef || content.voice_ref || ''
    },
    spec_hash: content.spec_hash || content.specHash || '',
    previous_spec_hash: content.previous_spec_hash || content.previousSpecHash || ''
  };
}

export function isReplaceableTombstone(event) {
  const content = parseJsonContent(event, {});
  if (content?.deleted === true) return true;
  return getTagValue(event, 'deleted') === 'true';
}

export function shouldAcceptReplaceableEvent(existing, incoming) {
  if (!incoming?.id) return false;
  if (!existing) return true;
  if (existing.id === incoming.id) return false;
  const incomingCreated = Number(incoming.created_at || 0);
  const existingCreated = Number(existing.created_at || 0);
  if (incomingCreated > existingCreated) return true;
  if (incomingCreated < existingCreated) return false;
  return String(incoming.id) > String(existing.id);
}

export function upsertReplaceableEvent(map, event) {
  const key = replaceableKey(event);
  if (!key) return { accepted: false, key: '', deleted: false };
  const existing = map.get(key);
  if (!shouldAcceptReplaceableEvent(existing, event)) {
    return { accepted: false, key, deleted: false };
  }
  if (isReplaceableTombstone(event)) {
    map.set(key, event);
    return { accepted: true, key, deleted: true };
  }
  map.set(key, event);
  return { accepted: true, key, deleted: false };
}

export function dedupeReplaceableEvents(events = []) {
  const map = new Map();
  for (const event of events || []) {
    upsertReplaceableEvent(map, event);
  }
  return Array.from(map.values())
    .filter((event) => !isReplaceableTombstone(event))
    .sort((a, b) => Number(b.created_at || 0) - Number(a.created_at || 0));
}

export function eventCoordinate(event) {
  const d = getDTag(event);
  if (!event?.kind || !event?.pubkey || !d) return '';
  return `${event.kind}:${event.pubkey}:${d}`;
}

function arrayFrom(value) {
  if (!value) return [];
  return Array.isArray(value) ? value.filter((item) => item !== undefined && item !== null && item !== '') : [value];
}

function unique(values = []) {
  return Array.from(new Set(values.filter(Boolean)));
}

function normalizeRelayHints(input = {}) {
  return {
    read: arrayFrom(input.read),
    write: arrayFrom(input.write),
    control: arrayFrom(input.control)
  };
}

export function parseRuntimeCapabilityEvent(event) {
  if (!event) return null;

  const content = parseJsonContent(event, {});
  const methods = [...arrayFrom(content.methods)];
  const controllerPubkeys = [
    ...arrayFrom(content.controller_pubkeys || content.controllerPubkeys),
    ...getTagValues(event, 'controller')
  ];
  const relayHints = normalizeRelayHints(content.relay_hints || content.relayHints || {});

  for (const tag of event.tags || []) {
    if (!Array.isArray(tag) || tag.length < 2) continue;
    switch (tag[0]) {
      case 'method':
        methods.push(tag[1]);
        break;
      case 'relay': {
        const scope = tag[2] || 'control';
        if (relayHints[scope]) relayHints[scope].push(tag[1]);
        break;
      }
      case 'read-relay':
        relayHints.read.push(tag[1]);
        break;
      case 'write-relay':
        relayHints.write.push(tag[1]);
        break;
      case 'control-relay':
        relayHints.control.push(tag[1]);
        break;
    }
  }

  const runtime = getTagValue(event, 'runtime', content.runtime || getDTag(event));
  const schema = getTagValue(event, 'schema', content.schema || '');
  const controlSchema = getTagValue(event, 'control-schema', content.control_schema || content.controlSchema || '');

  return {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    identifier: getDTag(event),
    coordinate: eventCoordinate(event),
    runtime,
    schema,
    controlSchema,
    methods: unique(methods),
    controllerPubkeys: unique(controllerPubkeys),
    relayHints: {
      read: unique(relayHints.read),
      write: unique(relayHints.write),
      control: unique(relayHints.control)
    },
    content,
    event,
    compatible:
      schema === SOUL_FACTORY_RUNTIME_CAPABILITY_SCHEMA &&
      controlSchema === SOUL_FACTORY_RUNTIME_CONTROL_SCHEMA
  };
}

export function runtimeCapabilitySupports(capability, { runtime = '', method = '', controllerPubkey = '' } = {}) {
  if (!capability?.compatible) return false;
  if (runtime && capability.runtime !== runtime) return false;
  if (method && !capability.methods.includes(method)) return false;
  if (controllerPubkey && capability.controllerPubkeys.length > 0 && !capability.controllerPubkeys.includes(controllerPubkey)) {
    return false;
  }
  return true;
}

function relayConnectionStateForSocket(socket) {
  if (!socket) return 'disconnected';
  if (socket.readyState === openReadyState()) return 'connected';
  if (socket.readyState === WebSocket.CONNECTING) return 'connecting';
  return 'disconnected';
}

function summarizeRelayConnections(relays, statusMap = {}) {
  const relayStatuses = relays.map((url) => ({
    url,
    status: statusMap[url] || 'unknown'
  }));

  const connected = relayStatuses.filter((relay) => relay.status === 'connected').length;
  const failed = relayStatuses.filter((relay) => ['error', 'failed', 'disconnected'].includes(relay.status)).length;
  const connecting = relayStatuses.filter((relay) => relay.status === 'connecting').length;

  return {
    total: relays.length,
    connected,
    failed,
    connecting,
    relays: relayStatuses
  };
}

export class NostrClient {
  constructor({ relays = getConfiguredRelays() } = {}) {
    this.relays = relays;
    this.sockets = new Map();
    this.subscriptions = new Map();
    this.subIdCounter = 0;
    this.connected = writable(false);
    this.connectionStatus = writable({});
    this.reconnectAttempts = new Map();
    this.reconnectTimers = new Map();
    this.manuallyDisconnected = false;
    this.pendingPublishes = new Map();
    this.messageQueues = new Map();
  }

  // Calculate exponential backoff delay
  getBackoffDelay(attempts) {
    const delay = Math.min(INITIAL_BACKOFF_MS * Math.pow(2, attempts), MAX_BACKOFF_MS);
    const jitter = delay * 0.2 * (Math.random() - 0.5);
    return Math.round(delay + jitter);
  }

  // Get current relay list
  getRelays() {
    return [...this.relays];
  }

  // Set relay list (and optionally persist)
  setRelays(relays, persist = true) {
    this.relays = relays;
    if (persist) {
      saveRelayConfig(relays);
    }
  }

  // Connect to all relays.
  // force=true: full reset (user-initiated reconnect) — clears failed/mid-retry state.
  // force=false (default): automatic/background call — skips relays already in a retry
  //   sequence so we don't interrupt backoff or restart exhausted relays.
  async connect(relays = this.relays, { force = false } = {}) {
    this.manuallyDisconnected = false;
    this.relays = Array.isArray(relays) ? [...relays] : [];
    console.log(`[nostr] Connecting to ${this.relays.length} relay(s):`, this.relays);

    const configuredRelays = new Set(this.relays);
    Array.from(this.reconnectTimers.entries()).forEach(([url, timer]) => {
      if (!configuredRelays.has(url)) {
        clearTimeout(timer);
        this.reconnectTimers.delete(url);
      }
    });

    Array.from(this.sockets.entries()).forEach(([url, socket]) => {
      if (configuredRelays.has(url)) return;
      this.rejectPendingPublishesForRelay(url, 'relay removed from configuration');
      this.notifyRelayClosed(url, 'relay removed from configuration');
      socket.onclose = null;
      socket.onerror = null;
      socket.onmessage = null;
      try {
        socket.close();
      } catch {}
      this.sockets.delete(url);
      this.reconnectAttempts.delete(url);
    });

    // Update connection status, preserving in-progress retry state on automatic calls.
    this.connectionStatus.update((prevStatus) =>
      Object.fromEntries(
        this.relays.map((url) => {
          if (!force) {
            // Keep 'failed' relays failed — they've exhausted all attempts.
            if (prevStatus[url] === 'failed') return [url, 'failed'];
            // Keep mid-retry relays as-is — don't interrupt the backoff sequence.
            if ((this.reconnectAttempts.get(url) || 0) > 0) return [url, prevStatus[url] || 'connecting'];
          }
          const currentState = relayConnectionStateForSocket(this.sockets.get(url));
          return [url, currentState === 'connected' ? 'connected' : 'connecting'];
        })
      )
    );

    // Reset attempt counters: force=true resets everything; otherwise only fresh relays.
    const currentStatus = get(this.connectionStatus);
    this.relays.forEach(url => {
      const attempts = this.reconnectAttempts.get(url) || 0;
      if (force || (attempts === 0 && currentStatus[url] !== 'failed')) {
        this.reconnectAttempts.set(url, 0);
      }
    });
    const promises = this.relays.map(url => this.connectRelay(url, { force }));
    await Promise.allSettled(promises);
    
    const summary = summarizeRelayConnections(this.relays, get(this.connectionStatus));
    console.log(`[nostr] Connected to ${summary.connected}/${summary.total} relays`);
    
    this.updateConnectedStatus();
    return summary;
  }

  // Connect to a single relay.
  // force=true: bypass failed/mid-retry guards (used by user-initiated reconnects).
  connectRelay(url, { force = false } = {}) {
    return new Promise((resolve) => {
      const status = get(this.connectionStatus)[url];
      const attempts = this.reconnectAttempts.get(url) || 0;

      if (!force) {
        // Skip relays that have permanently failed.
        if (status === 'failed') { resolve(); return; }
        // Skip relays mid-retry — scheduleReconnect will call us when the backoff fires.
        if (attempts > 0) { resolve(); return; }
      } else if (status === 'failed') {
        // Force reconnect on a previously-failed relay: clear its failed state.
        this.reconnectAttempts.set(url, 0);
        this.connectionStatus.update(s => ({ ...s, [url]: 'connecting' }));
      }

      // Clear any pending reconnect timer
      if (this.reconnectTimers.has(url)) {
        clearTimeout(this.reconnectTimers.get(url));
        this.reconnectTimers.delete(url);
      }

      if (this.sockets.has(url) && this.sockets.get(url).readyState === openReadyState()) {
        this.connectionStatus.update(s => ({ ...s, [url]: 'connected' }));
        resolve();
        return;
      }

      let ws;
      try {
        ws = new WebSocket(url);
      } catch (err) {
        console.error(`[nostr] Failed to create WebSocket for ${url}:`, err);
        this.connectionStatus.update(s => ({ ...s, [url]: 'error' }));
        resolve();
        return;
      }

      this.connectionStatus.update(s => ({ ...s, [url]: 'connecting' }));
      
      ws.onopen = () => {
        console.log(`[nostr] ✓ Connected to ${url}`);
        this.sockets.set(url, ws);
        this.connectionStatus.update(s => ({ ...s, [url]: 'connected' }));
        this.reconnectAttempts.set(url, 0);
        this.updateConnectedStatus();
        this.reissueSubscriptions(url, ws);
        resolve();
      };

      ws.onclose = () => {
        console.log(`[nostr] Disconnected from ${url}`);
        this.sockets.delete(url);
        this.connectionStatus.update(s => ({ ...s, [url]: 'disconnected' }));
        this.updateConnectedStatus();
        this.rejectPendingPublishesForRelay(url, 'relay connection closed');
        this.notifyRelayClosed(url, 'relay connection closed');
        if (!this.manuallyDisconnected) {
          this.scheduleReconnect(url);
        }
      };

      ws.onerror = (err) => {
        // Don't log full error object, just note it happened
        console.warn(`[nostr] ✗ Connection failed: ${url}`);
        this.connectionStatus.update(s => ({ ...s, [url]: 'error' }));
      };

      ws.onmessage = (e) => {
        this.handleMessage(url, e.data);
      };

      // Timeout for initial connection
      setTimeout(() => {
        if (ws.readyState === WebSocket.CONNECTING) {
          console.log(`[nostr] Connection timeout: ${url}`);
          ws.close();
          resolve();
        }
      }, 10000);
    });
  }

  // Schedule reconnection with exponential backoff
  scheduleReconnect(url) {
    const attempts = this.reconnectAttempts.get(url) || 0;
    
    if (attempts >= MAX_RECONNECT_ATTEMPTS) {
      console.log(`[nostr] Giving up on ${url} after ${MAX_RECONNECT_ATTEMPTS} attempts`);
      this.connectionStatus.update(s => ({ ...s, [url]: 'failed' }));
      return;
    }

    const delay = this.getBackoffDelay(attempts);
    console.log(`[nostr] Will retry ${url} in ${Math.round(delay/1000)}s (attempt ${attempts + 1}/${MAX_RECONNECT_ATTEMPTS})`);
    
    this.reconnectAttempts.set(url, attempts + 1);
    
    const timer = setTimeout(() => {
      this.reconnectTimers.delete(url);
      this.connectRelay(url);
    }, delay);
    
    this.reconnectTimers.set(url, timer);
  }

  // Reset and retry connection to a specific relay (always a force reconnect).
  retryRelay(url) {
    this.reconnectAttempts.set(url, 0);
    if (this.reconnectTimers.has(url)) {
      clearTimeout(this.reconnectTimers.get(url));
      this.reconnectTimers.delete(url);
    }
    return this.connectRelay(url, { force: true });
  }

  // Update connected store
  updateConnectedStatus() {
    const anyConnected = Array.from(this.sockets.values())
      .some(ws => ws.readyState === openReadyState());
    this.connected.set(anyConnected);
  }

  // Handle incoming messages in relay order. EVENT validation is async, so EOSE/CLOSED
  // for the same relay must wait behind earlier EVENT frames to preserve stream order.
  handleMessage(relay, data) {
    const previous = this.messageQueues.get(relay) || Promise.resolve();
    const next = previous.catch(() => {}).then(() => this.processRelayMessage(relay, data));
    this.messageQueues.set(relay, next);
    next.finally(() => {
      if (this.messageQueues.get(relay) === next) {
        this.messageQueues.delete(relay);
      }
    });
    return next;
  }

  async processRelayMessage(relay, data) {
    try {
      const msg = JSON.parse(data);
      const [type] = msg;

      switch (type) {
        case 'EVENT': {
          const [, subId, event] = msg;
          const sub = this.subscriptions.get(subId);
          if (sub && sub.onEvent) {
            try {
              await validateInboundNostrEvent(event);
            } catch (validationError) {
              console.warn(`[nostr] Dropping invalid EVENT from ${relay}:`, validationError?.message || validationError);
              break;
            }
            sub.onEvent(event, relay);
          }
          break;
        }

        case 'EOSE': {
          const [, subId] = msg;
          const subEose = this.subscriptions.get(subId);
          if (subEose && subEose.onEose) {
            subEose.onEose(relay);
          }
          break;
        }

        case 'OK': {
          const [, eventId, accepted, message] = msg;
          this.handleOk(relay, eventId, accepted, message);
          break;
        }

        case 'CLOSED': {
          const [, subId, reason = ''] = msg;
          const subClosed = this.subscriptions.get(subId);
          if (subClosed && subClosed.onClosed) {
            subClosed.onClosed(reason, relay);
          }
          break;
        }

        case 'NOTICE':
          console.log(`[nostr] Notice from ${relay}:`, msg[1]);
          break;
      }
    } catch (err) {
      console.error('[nostr] Failed to parse message:', err);
    }
  }

  handleOk(relay, eventId, accepted, message = '') {
    const pendingByRelay = this.pendingPublishes.get(eventId);
    if (!pendingByRelay) return;

    const pending = pendingByRelay.get(relay);
    if (!pending) return;

    pendingByRelay.delete(relay);
    pending.resolve({
      relay,
      sent: true,
      accepted: accepted === true,
      message: typeof message === 'string' ? message : ''
    });

    if (pendingByRelay.size === 0) {
      this.pendingPublishes.delete(eventId);
    }
  }

  // Subscribe to events
  subscribe(filters, { onEvent, onEose, onClosed } = {}) {
    const subId = `sub_${++this.subIdCounter}`;
    
    this.subscriptions.set(subId, { filters, onEvent, onEose, onClosed, events: [] });

    this.sendSubscription(subId);

    return () => {
      this.subscriptions.delete(subId);
      const close = JSON.stringify(['CLOSE', subId]);
      this.sockets.forEach((ws) => {
        if (ws.readyState === openReadyState()) {
          ws.send(close);
        }
      });
    };
  }

  sendSubscription(subId, relayUrl = null) {
    const sub = this.subscriptions.get(subId);
    if (!sub) return;

    const req = JSON.stringify(['REQ', subId, ...sub.filters]);
    if (relayUrl) {
      const ws = this.sockets.get(relayUrl);
      if (ws && ws.readyState === openReadyState()) {
        ws.send(req);
      }
      return;
    }

    this.sockets.forEach((ws) => {
      if (ws.readyState === WebSocket.OPEN) {
        ws.send(req);
      }
    });
  }

  reissueSubscriptions(relayUrl, ws) {
    if (!ws || ws.readyState !== openReadyState()) return;
    this.subscriptions.forEach((_sub, subId) => {
      this.sendSubscription(subId, relayUrl);
    });
  }

  notifyRelayClosed(relayUrl, reason) {
    this.subscriptions.forEach((sub) => {
      if (sub.onClosed) {
        sub.onClosed(reason, relayUrl);
      }
    });
  }

  // One-shot query that resolves only when all currently connected relays send EOSE.
  async queryUntilEose(filters, options = {}) {
    const queryOptions = typeof options === 'number' ? { timeoutMs: options } : (options || {});
    const { timeoutMs = null, signal = null } = queryOptions;

    return new Promise((resolve, reject) => {
      const events = [];
      const relayStates = new Map(
        Array.from(this.sockets.entries())
          .filter(([, ws]) => ws.readyState === openReadyState())
          .map(([url]) => [url, { status: 'pending', reason: '' }])
      );
      let unsub = null;
      let timer = null;
      let settled = false;

      const cleanup = () => {
        if (timer) clearTimeout(timer);
        timer = null;
        if (unsub) unsub();
        unsub = null;
        signal?.removeEventListener?.('abort', onAbort);
      };

      const incomplete = (reason, message = '') => new NostrIncompleteEOSEError(reason, {
        partialEvents: [...events],
        relaySummary: relaySummaryFromStates(relayStates),
        message
      });

      const settle = (fn, value) => {
        if (settled) return;
        settled = true;
        cleanup();
        fn(value);
      };

      const onAbort = () => {
        settle(reject, incomplete('aborted', signal?.reason?.message || 'Nostr query aborted before EOSE completion'));
      };

      const evaluateCompletion = () => {
        if (settled) return;
        const states = Array.from(relayStates.values());
        if (states.length === 0) return;
        if (states.every((state) => state.status === 'eose')) {
          settle(resolve, events);
          return;
        }
        if (states.every((state) => state.status !== 'pending')) {
          settle(reject, incomplete('all_relays_closed', 'Nostr query relays closed before all EOSE messages were received'));
        }
      };

      if (relayStates.size === 0) {
        settle(reject, incomplete('all_relays_closed', 'No connected Nostr relays available for EOSE query'));
        return;
      }

      if (signal?.aborted) {
        onAbort();
        return;
      }
      signal?.addEventListener?.('abort', onAbort, { once: true });

      if (Number.isFinite(timeoutMs) && timeoutMs > 0) {
        timer = setTimeout(() => {
          settle(reject, incomplete('timeout', `Timed out waiting for Nostr EOSE after ${timeoutMs}ms`));
        }, timeoutMs);
      }

      unsub = this.subscribe(filters, {
        onEvent: (event) => {
          if (!events.find(e => e.id === event.id)) {
            events.push(event);
          }
        },
        onEose: (relay) => {
          if (relayStates.has(relay)) {
            relayStates.set(relay, { status: 'eose', reason: '' });
          }
          evaluateCompletion();
        },
        onClosed: (reason = '', relay) => {
          if (relayStates.has(relay)) {
            const current = relayStates.get(relay);
            if (current?.status !== 'eose') {
              relayStates.set(relay, { status: 'closed', reason: String(reason || '') });
            }
          }
          evaluateCompletion();
        }
      });
    });
  }

  // One-shot query. Completion is EOSE-authoritative; timeout is an incomplete error.
  async query(filters, timeout = 5000) {
    const options = typeof timeout === 'number' ? { timeoutMs: timeout } : (timeout || {});
    return this.queryUntilEose(filters, options);
  }

  rejectPendingPublishesForRelay(relay, message) {
    this.pendingPublishes.forEach((pendingByRelay, eventId) => {
      const relaysToReject = relay ? [relay] : Array.from(pendingByRelay.keys());

      relaysToReject.forEach((relayUrl) => {
        const pending = pendingByRelay.get(relayUrl);
        if (!pending) return;

        pendingByRelay.delete(relayUrl);
        pending.resolve({
          relay: relayUrl,
          sent: false,
          accepted: false,
          message: message || 'relay connection closed'
        });
      });

      if (pendingByRelay.size === 0) {
        this.pendingPublishes.delete(eventId);
      }
    });
  }

  // Publish an event and wait for per-relay OK/CLOSED outcomes.
  async publish(event) {
    if (!event?.id) {
      throw new Error('Cannot publish event without id');
    }

    const msg = JSON.stringify(['EVENT', event]);
    const pendingByRelay = new Map();
    const promises = [];

    this.sockets.forEach((ws, url) => {
      if (ws.readyState !== openReadyState()) return;

      let resolvePending;
      const relayPromise = new Promise((resolve) => {
        resolvePending = resolve;
      });

      pendingByRelay.set(url, { resolve: resolvePending });
      promises.push(relayPromise);

      try {
        ws.send(msg);
      } catch (error) {
        pendingByRelay.delete(url);
        resolvePending({
          relay: url,
          sent: false,
          accepted: false,
          message: error?.message || 'failed to send event'
        });
      }
    });

    if (pendingByRelay.size === 0) {
      return [];
    }

    this.pendingPublishes.set(event.id, pendingByRelay);
    return Promise.all(promises);
  }

  // Close all connections
  disconnect() {
    this.manuallyDisconnected = true;
    this.subscriptions.clear();
    this.messageQueues.clear();
    this.rejectPendingPublishesForRelay('', 'client disconnected');
    
    this.reconnectTimers.forEach(timer => clearTimeout(timer));
    this.reconnectTimers.clear();
    this.reconnectAttempts.clear();
    
    this.sockets.forEach((ws, url) => {
      ws.close();
    });
    this.sockets.clear();
    this.connected.set(false);
    this.connectionStatus.set({});
  }
}

// Singleton instance
export const nostr = new NostrClient();

// --- Soul Factory specific helpers ---

export async function fetchTemplates(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_TEMPLATE] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return dedupeReplaceableEvents(await nostr.queryUntilEose([filter]));
}

export async function fetchSouls(authorPubkey = null) {
  const filter = { kinds: [KINDS.AGENT_SOUL] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return dedupeReplaceableEvents(await nostr.queryUntilEose([filter]));
}

export async function fetchSoulDrafts(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_DRAFT] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return dedupeReplaceableEvents(await nostr.queryUntilEose([filter]));
}

export async function fetchSoulDraft(agentId, authorPubkey) {
  const events = await nostr.queryUntilEose([{
    kinds: [KINDS.SOUL_DRAFT],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }]);
  return dedupeReplaceableEvents(events)[0] || null;
}

export async function fetchRuntimeCapabilities({ runtime = null, controllerPubkey = null, method = null, limit = 200 } = {}) {
  const events = dedupeReplaceableEvents(await nostr.queryUntilEose([{ kinds: [KINDS.RUNTIME_CAPABILITY], limit }]));
  return events
    .map(parseRuntimeCapabilityEvent)
    .filter(Boolean)
    .filter((capability) => runtimeCapabilitySupports(capability, { runtime, controllerPubkey, method }));
}

export async function fetchSoul(agentId, authorPubkey) {
  const events = await nostr.queryUntilEose([{
    kinds: [KINDS.AGENT_SOUL],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }]);
  return dedupeReplaceableEvents(events)[0] || null;
}

export function subscribeToProvisioningProgress(requestEventId, onStatus, onResult) {
  return nostr.subscribe([
    { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
    { kinds: [KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT], '#e': [requestEventId] }
  ], {
    onEvent: (event) => {
      if (event.kind === KINDS.PROVISIONING_STATUS) {
        onStatus(parseProvisioningStatus(event));
      } else if (isLifecycleResultKind(event.kind)) {
        onResult(parseProvisioningResult(event));
      }
    }
  });
}

export function parseSoulEvent(event) {
  const soul = {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    content: event.content,
    agentId: '',
    name: '',
    purpose: '',
    tier: 'standard',
    status: 'active',
    deployStatus: '',
    npub: '',
    agentPubkey: '',
    avatarUrl: '',
    nip05: '',
    workspace: '',
    qdrant: '',
    bahiaServiceId: '',
    allowedKinds: [],
    tools: [],
    draftRef: '',
    specHash: '',
    previousSpecHash: '',
    runtime: {},
    relayPolicy: {},
    permissions: {},
    workspaceSpec: {},
    assets: {},
    capabilityRef: '',
    lastResultRef: ''
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'd': soul.agentId = tag[1]; break;
      case 'name': soul.name = tag[1]; break;
      case 'purpose': soul.purpose = tag[1]; break;
      case 'tier': soul.tier = tag[1]; break;
      case 'status': soul.status = tag[1]; break;
      case 'deploy-status': soul.deployStatus = tag[1]; break;
      case 'npub': soul.npub = tag[1]; break;
      case 'p': if (tag[2] === 'agent') soul.agentPubkey = tag[1]; break;
      case 'avatar': soul.avatarUrl = tag[1]; break;
      case 'nip05': soul.nip05 = tag[1]; break;
      case 'workspace': soul.workspace = tag[1]; break;
      case 'qdrant': soul.qdrant = tag[1]; break;
      case 'service': soul.bahiaServiceId = tag[1]; break;
      case 'allowed-kind': soul.allowedKinds.push(parseInt(tag[1])); break;
      case 'tool': soul.tools.push({ server: tag[1], scopes: tag.slice(2) }); break;
      case 'draft': soul.draftRef = tag[1]; break;
      case 'spec-hash': soul.specHash = tag[1]; break;
      case 'previous-spec-hash': soul.previousSpecHash = tag[1]; break;
      case 'runtime': soul.runtime.target = tag[1]; break;
      case 'runtime-pubkey': soul.runtime.runtime_pubkey = tag[1]; break;
      case 'runtime-binding': soul.runtime.runtime_binding = tag[1]; break;
      case 'runtime-state': soul.runtime.state = tag[1]; break;
      case 'capability': soul.capabilityRef = tag[1]; soul.runtime.capability_ref = tag[1]; break;
      case 'last-result': soul.lastResultRef = tag[1]; break;
    }
  }

  const content = parseJsonContent(event, null);
  if (content && typeof content === 'object') {
    soul.runtime = { ...soul.runtime, ...(content.runtime || {}) };
    soul.relayPolicy = content.relay_policy || content.relayPolicy || soul.relayPolicy;
    soul.permissions = content.permissions || soul.permissions;
    soul.workspaceSpec = content.workspace || soul.workspaceSpec;
    soul.assets = content.assets || soul.assets;
    if (soul.allowedKinds.length === 0 && Array.isArray(soul.permissions?.allowed_kinds)) {
      soul.allowedKinds = soul.permissions.allowed_kinds;
    }
    if (soul.tools.length === 0 && Array.isArray(soul.permissions?.tool_grants)) {
      soul.tools = soul.permissions.tool_grants.map((grant) => {
        if (typeof grant === 'string') return { server: grant, scopes: [] };
        return {
          server: grant?.mcp_server || grant?.server || grant?.name || '',
          scopes: Array.isArray(grant?.scopes) ? grant.scopes : []
        };
      }).filter((grant) => grant.server);
    }
    soul.avatarUrl = soul.avatarUrl || content.avatar_url || content.avatarUrl || (String(soul.assets?.avatar_ref || '').startsWith('http') ? soul.assets.avatar_ref : '');
    soul.specHash = soul.specHash || content.spec_hash || content.specHash || '';
    soul.previousSpecHash = soul.previousSpecHash || content.previous_spec_hash || content.previousSpecHash || '';
    soul.draftRef = soul.draftRef || content.draft_ref || content.draftRef || '';
    soul.capabilityRef = soul.capabilityRef || content.capability_ref || content.capabilityRef || soul.runtime.capability_ref || '';
    soul.lastResultRef = soul.lastResultRef || content.last_result_ref || content.lastResultRef || '';
  }

  return soul;
}

export function parseSoulDraftEvent(event) {
  if (!event) return null;
  const content = normalizeSoulDraftContent(parseJsonContent(event, {}));
  return {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    agentId: getDTag(event),
    name: getTagValue(event, 'name', content.identity?.name || ''),
    tier: getTagValue(event, 'tier', content.identity?.tier || 'standard'),
    templateRef: getTagValue(event, 'template', ''),
    specHash: getTagValue(event, 'spec-hash', content.spec_hash || ''),
    previousSpecHash: getTagValue(event, 'previous-spec-hash', content.previous_spec_hash || ''),
    content
  };
}

export function parseTemplateEvent(event) {
  const content = parseJsonContent(event, null);
  const customization = content && typeof content === 'object'
    ? normalizeSoulDraftContent({
      schema: 'soulfactory-draft/v2',
      ...(content.customization || {}),
      persona: content.persona || content.customization?.persona,
      avatar: content.avatar || content.customization?.avatar,
      voice: content.voice || content.customization?.voice,
      memory: content.memory || content.customization?.memory
    })
    : null;
  const template = {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    identifier: '',
    name: content?.name || '',
    description: content?.description || '',
    tier: content?.tier || 'standard',
    basePrompt: content?.brief || content?.basePrompt || content?.prompt || event.content,
    defaultCustomization: customization,
    defaultKinds: [],
    defaultTools: [],
    tags: []
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'd': template.identifier = tag[1]; break;
      case 'name': template.name = tag[1]; break;
      case 'description': template.description = tag[1]; break;
      case 'tier': template.tier = tag[1]; break;
      case 't': template.tags.push(tag[1]); break;
      case 'default-kind': template.defaultKinds.push(parseInt(tag[1])); break;
    }
  }

  return template;
}

export function parseRepositoryEvent(event) {
  if (!event || !event.id || !event.pubkey || !Array.isArray(event.tags)) {
    return null;
  }

  const repo = {
    id: event.id,
    pubkey: event.pubkey,
    created_at: event.created_at,
    identifier: '',
    name: '',
    description: '',
    webUrls: [],
    cloneUrls: [],
    relayUrls: [],
    earliestUniqueCommitId: '',
    maintainers: []
  };

  for (const tag of event.tags) {
    if (!Array.isArray(tag) || tag.length < 2) continue;

    switch (tag[0]) {
      case 'd':
        repo.identifier = tag[1] || '';
        break;
      case 'name':
        repo.name = tag[1] || '';
        break;
      case 'description':
        repo.description = tag[1] || '';
        break;
      case 'web':
        repo.webUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'clone':
        repo.cloneUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'relays':
        repo.relayUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'r':
        repo.earliestUniqueCommitId = tag[1] || '';
        break;
      case 'maintainers':
        repo.maintainers.push(...tag.slice(1).filter(Boolean));
        break;
    }
  }

  if (!repo.identifier) {
    return null;
  }

  repo.repoCoordinate = `${KINDS.REPOSITORY}:${repo.pubkey}:${repo.identifier}`;
  repo.primaryUrl = repo.cloneUrls[0] || repo.webUrls[0] || '';
  repo.displayName = repo.name || repo.identifier || repo.primaryUrl;
  repo.searchText = [
    repo.identifier,
    repo.name,
    repo.description,
    repo.primaryUrl,
    repo.repoCoordinate,
    ...repo.cloneUrls,
    ...repo.webUrls,
    ...repo.relayUrls,
    ...repo.maintainers
  ].join(' ').toLowerCase();

  return repo;
}

export async function fetchRepositories({ authors = null, limit = 200, since = null } = {}) {
  const filter = {
    kinds: [KINDS.REPOSITORY],
    limit
  };

  if (Array.isArray(authors) && authors.length > 0) {
    filter.authors = authors;
  }

  if (typeof since === 'number') {
    filter.since = since;
  }

  const events = await nostr.query([filter]);
  const deduped = new Map();

  for (const event of events) {
    const parsed = parseRepositoryEvent(event);
    if (!parsed) continue;

    const existing = deduped.get(parsed.repoCoordinate);
    if (!existing || parsed.created_at >= existing.created_at) {
      deduped.set(parsed.repoCoordinate, parsed);
    }
  }

  return Array.from(deduped.values());
}

function parseProvisioningStatus(event) {
  const status = {
    id: event.id,
    step: '',
    progress: { current: 0, total: 0 },
    message: event.content
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'step': status.step = tag[1]; break;
      case 'progress':
        status.progress = { current: parseInt(tag[1]), total: parseInt(tag[2]) };
        break;
    }
  }

  return status;
}

function parseProvisioningResult(event) {
  const result = {
    id: event.id,
    success: false,
    error: '',
    soulRef: '',
    action: '',
    requestKind: '',
    specHash: '',
    data: {},
    legacyKind: event.kind === KINDS.SOUL_ACTION_LEGACY_RESULT
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'status':
        result.success = tag[1] === 'success';
        if (tag[1] === 'error') result.error = event.content;
        break;
      case 'soul': result.soulRef = tag[1]; break;
      case 'action': result.action = tag[1]; break;
      case 'request-kind': result.requestKind = tag[1]; break;
      case 'spec-hash': result.specHash = tag[1]; break;
    }
  }

  if (event.content) {
    try {
      result.data = JSON.parse(event.content);
    } catch (e) {
      // Content might not be JSON
    }
  }

  return result;
}

export default nostr;
