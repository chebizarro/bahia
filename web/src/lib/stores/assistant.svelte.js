import { browser } from '$app/environment';
import { authState } from './auth.js';
import { controlplaneConnection, bootstrapControlplane } from './controlplane.svelte.js';
import { requestEncryptedResult } from '../nostr/encrypted-controlplane.js';
import {
  nostr,
  ASSISTANT_KINDS,
  getTagValue,
  getTagValues,
  parseJsonContent,
  parseAssistantSessionEvent,
  parseAssistantStatusEvent,
  parseAssistantTranscriptEvent,
  computeAssistantBatchApprovalHash,
  normalizeAssistantExecutablePlan,
  shouldAcceptReplaceableEvent,
  assistantRequestError,
  assertAssistantRequestAccepted,
  classifyAssistantRequestError,
  ASSISTANT_REQUEST_ERROR_KINDS,
  ASSISTANT_EXECUTION_TERMINAL_PHASES as TERMINAL_PHASES,
  ASSISTANT_EXECUTION_CANCELLABLE_PHASES as CANCELLABLE_PHASES
} from '../nostr/client.js';

const { STALE, INVALID, REJECTED } = ASSISTANT_REQUEST_ERROR_KINDS;

const SIDEBAR_STORAGE_KEY = 'bahia_assistant_sidebar';
const TRANSCRIPT_STORAGE_SCHEMA = 'bahia_assistant_transcript_v2';
const LEGACY_TRANSCRIPT_STORAGE_SCHEMA = 'bahia_assistant_transcript_v1';
const TRANSCRIPT_STORAGE_PREFIX = 'bahia_assistant_transcript';
const RECENT_TRANSCRIPT_SECONDS = 14 * 24 * 60 * 60;
const TRANSCRIPT_LIMIT = 300;
const SESSION_LIMIT = 100;
const ASSISTANT_PROMPT_TIMEOUT_MS = 120000;
const ASSISTANT_APPROVAL_TIMEOUT_MS = 180000;

export const assistantConnection = $state({
  status: 'idle', // idle | waiting_auth | bootstrapping | live | disconnected | error
  ready: false,
  operatorPubkey: '',
  servicePubkey: '',
  lastError: null,
  lastEoseAt: null,
  resubscribeAttempts: 0,
  lastClosedReason: null,
  lastEventAt: null
});

export const assistantUi = $state({
  panelOpen: false,
  activeSessionId: '',
  hasUnread: false,
  lastDismissedAt: 0
});

export const assistantSessions = $state([]);
export const pendingAssistantRequests = $state({});

export function activeAssistantSession() {
  return assistantUi.activeSessionId ? assistantSessions.find((session) => session.sessionId === assistantUi.activeSessionId) || null : assistantSessions[0] || null;
}

const sessionMap = new Map();
const eventMap = new Map();
const pendingMap = new Map();
const seenEventIds = new Set();

let bootstrapPromise = null;
let liveUnsubscribe = null;
let connectedUnsubscribe = null;
let lastConnected = false;
let restoredTranscriptCacheKey = '';

function nowSeconds() {
  return Math.floor(Date.now() / 1000);
}

function replaceArray(target, values) {
  target.length = 0;
  target.push(...values);
}

function syncPendingRequests() {
  for (const key of Object.keys(pendingAssistantRequests)) delete pendingAssistantRequests[key];
  for (const [key, value] of pendingMap.entries()) pendingAssistantRequests[key] = value;
}

function assistantTranscriptStorageKey(operatorPubkey = assistantConnection.operatorPubkey, servicePubkey = assistantConnection.servicePubkey, schema = TRANSCRIPT_STORAGE_SCHEMA) {
  const operator = String(operatorPubkey || '').trim();
  const service = String(servicePubkey || '').trim();
  if (!browser || !operator) return '';
  return `${TRANSCRIPT_STORAGE_PREFIX}:${schema}:${operator}:${service || 'unknown-service'}`;
}

function cacheableTranscriptItems() {
  return sortTranscript(Array.from(eventMap.values()).filter((item) => !item?.pending)).slice(-TRANSCRIPT_LIMIT)
    .map(({ event: _event, content: _content, ...displayItem }) => displayItem);
}

function serializableSession(session) {
  const { sessionId, state, operatorPubkey, participants, assistantId, assistantPubkey,
    currentTurnId, currentRequestId, transcriptSummary, lastResultId, updatedAt,
    executionVersion, workflow, currentRunId, executionRevision, phase, scope,
    proposal, pendingApprovals, submittedEffects, uncertainEffects, checkpointEventId,
    sessionEvent } = session;
  return { sessionId, state, operatorPubkey, participants, assistantId, assistantPubkey,
    currentTurnId, currentRequestId, transcriptSummary, lastResultId, updatedAt,
    executionVersion, workflow, currentRunId, executionRevision, phase, scope,
    proposal, pendingApprovals, submittedEffects, uncertainEffects, checkpointEventId,
    sessionEvent: sessionEvent ? { id: sessionEvent.id, createdAt: sessionEvent.createdAt,
      event: { id: sessionEvent.id, created_at: sessionEvent.createdAt } } : null };
}

// Session projections are deliberately not remembered here: a cached projection
// is display-only, and the relay re-delivering it is what restores authority.
function rememberSeenTranscriptIds(item) {
  if (item?.id) seenEventIds.add(item.id);
  if (item?.event?.id) seenEventIds.add(item.event.id);
}

function persistAssistantTranscriptCache() {
  const key = assistantTranscriptStorageKey();
  if (!key) return false;
  try {
    const payload = {
      schema: TRANSCRIPT_STORAGE_SCHEMA,
      cachedAt: Date.now(),
      operatorPubkey: assistantConnection.operatorPubkey,
      servicePubkey: assistantConnection.servicePubkey,
      activeSessionId: assistantUi.activeSessionId || '',
      sessions: Array.from(sessionMap.values()).map(serializableSession),
      transcript: cacheableTranscriptItems()
    };
    localStorage.setItem(key, JSON.stringify(payload));
    return true;
  } catch (err) {
    console.warn('Unable to persist assistant transcript cache:', err);
    return false;
  }
}

function restoreAssistantTranscriptCache(operatorPubkey, servicePubkey) {
  const key = assistantTranscriptStorageKey(operatorPubkey, servicePubkey);
  if (!key || restoredTranscriptCacheKey === key) return false;
  restoredTranscriptCacheKey = key;

  try {
    const current = JSON.parse(localStorage.getItem(key) || 'null');
    const legacyKey = assistantTranscriptStorageKey(operatorPubkey, servicePubkey, LEGACY_TRANSCRIPT_STORAGE_SCHEMA);
    const cached = current || JSON.parse(localStorage.getItem(legacyKey) || 'null');
    if (!cached || ![TRANSCRIPT_STORAGE_SCHEMA, LEGACY_TRANSCRIPT_STORAGE_SCHEMA].includes(cached.schema)) return false;
    const fromLegacy = cached.schema === LEGACY_TRANSCRIPT_STORAGE_SCHEMA;
    if (cached.operatorPubkey && cached.operatorPubkey !== operatorPubkey) return false;
    if (cached.servicePubkey && servicePubkey && cached.servicePubkey !== servicePubkey) return false;

    let restored = false;
    for (const cachedSession of Array.isArray(cached.sessions) ? cached.sessions : []) {
      const sessionId = String(cachedSession?.sessionId || '').trim();
      if (!sessionId) continue;
      const session = ensureSession(sessionId);
      Object.assign(session, { ...session, ...serializableSession(cachedSession), authoritative: false, transcript: [], pendingActions: [] });
      if (fromLegacy) {
        Object.assign(session, { executionVersion: 1, workflow: '', currentRunId: '', proposal: null, pendingApprovals: [], scope: null });
      }
      restored = true;
    }

    for (const item of Array.isArray(cached.transcript) ? cached.transcript : []) {
      const sessionId = String(item?.sessionId || '').trim();
      const itemId = String(item?.id || '').trim();
      if (!sessionId || !itemId || item?.pending) continue;
      const session = ensureSession(sessionId);
      const { event: _rawEvent, content: _rawContent, ...displayItem } = item;
      eventMap.set(itemId, withoutPrivateCommandScope(displayItem));
      session.updatedAt = Math.max(session.updatedAt || 0, item.createdAt || item.event?.created_at || 0);
      rememberSeenTranscriptIds(item);
      restored = true;
    }

    if (typeof cached.activeSessionId === 'string') assistantUi.activeSessionId = cached.activeSessionId;
    if (restored) refreshSessions();
    // Migration: once the history is re-written under the v2 key, drop the v1
    // entry so it can never be re-imported with different semantics.
    if (fromLegacy && persistAssistantTranscriptCache()) localStorage.removeItem(legacyKey);
    return restored;
  } catch (err) {
    console.warn('Unable to restore assistant transcript cache:', err);
    return false;
  }
}

function loadAssistantUiState() {
  if (!browser) return;
  try {
    const stored = JSON.parse(localStorage.getItem(SIDEBAR_STORAGE_KEY) || '{}');
    if ('open' in stored && !('panelOpen' in stored)) {
      stored.panelOpen = Boolean(stored.open) && !Boolean(stored.collapsed);
      delete stored.open;
      delete stored.collapsed;
    }
    if (typeof stored.panelOpen === 'boolean') assistantUi.panelOpen = stored.panelOpen;
    if (typeof stored.activeSessionId === 'string') assistantUi.activeSessionId = stored.activeSessionId;
    if (typeof stored.hasUnread === 'boolean') assistantUi.hasUnread = stored.hasUnread;
    if (Number.isFinite(stored.lastDismissedAt)) assistantUi.lastDismissedAt = stored.lastDismissedAt;
  } catch (err) {
    console.warn('Unable to load assistant UI state:', err);
  }
}

function persistAssistantUiState() {
  if (!browser) return;
  try {
    localStorage.setItem(
      SIDEBAR_STORAGE_KEY,
      JSON.stringify({
        panelOpen: assistantUi.panelOpen,
        activeSessionId: assistantUi.activeSessionId || '',
        hasUnread: assistantUi.hasUnread,
        lastDismissedAt: assistantUi.lastDismissedAt || 0
      })
    );
  } catch (err) {
    console.warn('Unable to persist assistant UI state:', err);
  }
}

function emptySession(sessionId) {
  return {
    sessionId,
    state: 'idle',
    operatorPubkey: assistantConnection.operatorPubkey,
    participants: assistantConnection.operatorPubkey ? [assistantConnection.operatorPubkey] : [],
    assistantId: '',
    assistantPubkey: '',
    currentTurnId: '',
    currentRequestId: '',
    lastPlanHash: '',
    executionVersion: 0,
    workflow: '',
    currentRunId: '',
    executionRevision: 0,
    phase: '',
    scope: null,
    proposal: null,
    pendingApprovals: [],
    submittedEffects: 0,
    uncertainEffects: 0,
    authoritative: false,
    currentPlan: null,
    pendingSteps: [],
    transcriptSummary: '',
    metadata: {},
    pendingActions: [],
    lastResultId: '',
    updatedAt: 0,
    sessionEvent: null,
    transcript: []
  };
}

function ensureSession(sessionId) {
  if (!sessionId) return null;
  if (!sessionMap.has(sessionId)) sessionMap.set(sessionId, emptySession(sessionId));
  return sessionMap.get(sessionId);
}

function eventSessionId(event, content = null) {
  return getTagValue(event, 'session', content?.session_id || content?.sessionId || '');
}

// Status events only *describe* an action (tool, arguments preview, prompt).
// Whether it is actionable is decided solely by the current v2 projection.
function actionDetailsFromStatus(item) {
  return {
    turnId: item.turnId || '',
    toolCallId: item.toolCallId || '',
    toolName: item.toolName || '',
    argsPreview: item.argsPreview || null,
    approvalPrompt: item.approvalPrompt || item.message || '',
    permission: item.permission || null,
    createdAt: item.createdAt || ''
  };
}

function currentActionDetails(session, actionId) {
  const transcript = Array.isArray(session.transcript) ? session.transcript : [];
  for (let index = transcript.length - 1; index >= 0; index--) {
    const item = transcript[index];
    if (item?.type === 'status' && item.phase === 'approval_required' && item.actionId === actionId &&
        item.runId && item.runId === session.currentRunId) return actionDetailsFromStatus(item);
  }
  return {};
}

function currentPendingActions(session) {
  if (!session.authoritative || session.executionVersion !== 2 || session.phase !== 'awaiting_approval' ||
      session.workflow !== 'iterative' || !session.currentRunId) return [];
  return (session.pendingApprovals || []).map((actionId) => ({
    ...currentActionDetails(session, actionId),
    actionId, runId: session.currentRunId, sessionId: session.sessionId
  }));
}

function eventTimestamp(item) {
  return item.createdAt || item.event?.created_at || 0;
}

function sortTranscript(items) {
  return [...items].sort((a, b) => eventTimestamp(a) - eventTimestamp(b) || String(a.id).localeCompare(String(b.id)));
}

function refreshSessions() {
  for (const session of sessionMap.values()) {
    session.transcript = sortTranscript(Array.from(eventMap.values()).filter((item) => item.sessionId === session.sessionId));
    session.pendingActions = currentPendingActions(session);
  }

  const values = Array.from(sessionMap.values()).sort((a, b) => (b.updatedAt || 0) - (a.updatedAt || 0));
  replaceArray(assistantSessions, values);

  if (!assistantUi.activeSessionId && values[0]?.sessionId) assistantUi.activeSessionId = values[0].sessionId;
  persistAssistantUiState();
  persistAssistantTranscriptCache();
}


function applySessionEvent(event) {
  const parsed = parseAssistantSessionEvent(event);
  if (!parsed?.sessionId) return false;
  if (assistantConnection.servicePubkey && event.pubkey !== assistantConnection.servicePubkey) return false;
  if (assistantConnection.operatorPubkey) {
    const participants = Array.isArray(parsed.participants) ? parsed.participants : [];
    if (participants.length > 0 && !participants.includes(assistantConnection.operatorPubkey)) return false;
    if (participants.length === 0 && parsed.operatorPubkey && parsed.operatorPubkey !== assistantConnection.operatorPubkey) return false;
  }
  const session = ensureSession(parsed.sessionId);
  // v1 and v2 use different coordinates; v2 always outranks v1 history.
  if (session.executionVersion === 2 && parsed.executionVersion !== 2) return false;
  // Within one coordinate, plain NIP-01 replaceable ordering applies to every
  // known projection, cached or live. Payload execution_revision never
  // overrides it. The same event re-delivered by a relay confirms a cached view.
  const known = session.executionVersion === parsed.executionVersion ? session.sessionEvent?.event : null;
  if (known) {
    const confirmsCachedView = known.id === event.id && !session.authoritative;
    if (!confirmsCachedView && !shouldAcceptReplaceableEvent(known, event)) return false;
  }
  Object.assign(session, {
    state: parsed.state, operatorPubkey: parsed.operatorPubkey, participants: parsed.participants,
    assistantId: parsed.assistantId, assistantPubkey: parsed.assistantPubkey,
    currentTurnId: parsed.currentTurnId, currentRequestId: parsed.currentRequestId,
    lastPlanHash: parsed.executionVersion === 2 ? '' : parsed.lastPlanHash,
    currentPlan: parsed.executionVersion === 2 ? null : parsed.currentPlan,
    pendingSteps: parsed.executionVersion === 2 ? [] : parsed.pendingSteps,
    transcriptSummary: parsed.transcriptSummary, lastResultId: parsed.lastResultId,
    executionVersion: parsed.executionVersion, workflow: parsed.workflow,
    currentRunId: parsed.currentRunId, executionRevision: parsed.executionRevision,
    phase: parsed.phase, scope: parsed.scope, proposal: parsed.proposal,
    pendingApprovals: parsed.pendingApprovals, submittedEffects: parsed.submittedEffects,
    uncertainEffects: parsed.uncertainEffects, checkpointEventId: parsed.checkpointEventId,
    metadata: {}, authoritative: parsed.executionVersion === 2,
    updatedAt: Math.max(session.updatedAt || 0, parsed.createdAt || 0),
    sessionEvent: { id: parsed.id, createdAt: parsed.createdAt,
      event: { id: event.id, created_at: event.created_at, kind: event.kind, pubkey: event.pubkey, tags: event.tags } }
  });
  return true;
}

function normalizeAssistantItem(event) {
  if (event.kind === ASSISTANT_KINDS.STATUS) {
    const parsed = parseAssistantStatusEvent(event);
    return parsed
      ? {
          ...parsed,
          type: 'status',
          streaming: getTagValue(event, 'streaming', '') === 'true' || parsed.content?.streaming === true,
          chunk: parsed.content?.chunk || ''
        }
      : null;
  }
  if (event.kind === ASSISTANT_KINDS.TRANSCRIPT) {
    const parsed = parseAssistantTranscriptEvent(event);
    return parsed
      ? {
          ...parsed,
          type: 'transcript'
        }
      : null;
  }
  return null;
}

function authorAllowed(event) {
  if (event.kind === ASSISTANT_KINDS.STATUS || event.kind === ASSISTANT_KINDS.SESSION || event.kind === ASSISTANT_KINDS.TRANSCRIPT) {
    return !assistantConnection.servicePubkey || event.pubkey === assistantConnection.servicePubkey;
  }
  return false;
}

function withoutPrivateCommandScope(value) {
  if (Array.isArray(value)) return value.map(withoutPrivateCommandScope);
  if (!value || typeof value !== 'object') return value;
  const clean = {};
  for (const [key, child] of Object.entries(value)) {
    if (key === 'command_scope') continue;
    if (key === 'scope' && child && typeof child === 'object') {
      const { arguments: _privateArguments, ...publicScope } = child;
      clean[key] = withoutPrivateCommandScope(publicScope);
    } else clean[key] = withoutPrivateCommandScope(child);
  }
  return clean;
}

function recordAssistantItem(item) {
  if (!item?.sessionId || !item.id) return false;
  item = withoutPrivateCommandScope({ ...item, event: item.event ? { ...item.event, content: '' } : null });
  ensureSession(item.sessionId);

  if (item.type === 'status' && item.streaming) {
    const streamKey = `stream:${item.requestEventId || item.id}`;
    const existing = eventMap.get(streamKey);
    eventMap.set(streamKey, {
      ...(existing || item),
      id: streamKey,
      type: 'status',
      status: 'planning',
      message: existing?.message || 'Planning…',
      streaming: true,
      streamingContent: `${existing?.streamingContent || ''}${item.chunk || ''}`,
      createdAt: existing?.createdAt || item.createdAt,
      event: item.event
    });
  } else {
    if (item.type === 'result' && item.requestEventId) eventMap.delete(`stream:${item.requestEventId}`);
    eventMap.set(item.id, item);
  }

  const session = sessionMap.get(item.sessionId);
  session.updatedAt = Math.max(session.updatedAt || 0, item.createdAt || 0);
  // v1 plan fields are kept only as read-only history; they never feed controls.
  if (session.executionVersion !== 2) {
    if ((item.type === 'status' || item.type === 'result') && item.planHash) session.lastPlanHash = item.planHash;
    if ((item.type === 'status' || item.type === 'result') && item.plan && !session.currentPlan) session.currentPlan = item.plan;
  }
  if (item.type === 'result' && item.id) session.lastResultId = item.id;

  if (item.requestEventId && pendingMap.has(item.requestEventId) && item.type === 'result') {
    pendingMap.delete(item.requestEventId);
    syncPendingRequests();
  }
  return true;
}

function applyTranscriptEvent(event, { allowStreaming = true } = {}) {
  if (!authorAllowed(event)) return false;
  const item = normalizeAssistantItem(event);
  if (!item?.sessionId) return false;
  if (item.streaming && !allowStreaming) return false;
  return recordAssistantItem(item);
}

function applyLocalAssistantItem(item) {
  if (!recordAssistantItem(item)) return false;
  assistantConnection.lastEventAt = new Date().toISOString();
  refreshSessions();
  return true;
}

function removeLocalAssistantItem(itemId) {
  if (!itemId || !eventMap.delete(itemId)) return false;
  refreshSessions();
  return true;
}

function assistantPendingItem({ sessionId, turnId, prompt }) {
  return {
    type: 'status',
    id: `assistant-pending:${sessionId}:${turnId}`,
    kind: ASSISTANT_KINDS.STATUS,
    pubkey: assistantConnection.servicePubkey,
    createdAt: nowSeconds(),
    sessionId,
    turnId,
    status: 'planning',
    pending: true,
    message: '',
    prompt
  };
}

// A failed prompt RPC never becomes an execution failure: either the service
// definitively rejected the request, or the outcome is unknown.
function assistantRequestOutcomeItem(error, { sessionId, turnId }) {
  const { kind, detail } = classifyAssistantRequestError(error);
  const rejected = kind === REJECTED || kind === STALE;
  const status = rejected ? 'request_rejected' : 'outcome_unknown';
  const summary = rejected ? 'Assistant service rejected the request' : 'Request outcome unknown / reconnecting';
  return {
    type: 'result',
    id: `assistant-${rejected ? 'rejected' : 'unknown'}:${sessionId}:${turnId}:${Date.now()}`,
    kind: ASSISTANT_KINDS.CONTEXTVM_RESULT,
    pubkey: assistantConnection.servicePubkey,
    createdAt: nowSeconds(),
    sessionId,
    turnId,
    status,
    summary,
    error: detail,
    content: { status, summary, error: detail },
    event: null
  };
}

function applyAssistantEvent(event, options = {}) {
  if (!event?.id || seenEventIds.has(event.id)) return false;
  seenEventIds.add(event.id);

  let changed = false;
  if (event.kind === ASSISTANT_KINDS.SESSION) changed = applySessionEvent(event);
  else changed = applyTranscriptEvent(event, options);

  if (changed) {
    if (
      !assistantUi.panelOpen &&
      event.kind === ASSISTANT_KINDS.STATUS &&
      event.created_at > assistantUi.lastDismissedAt
    ) {
      assistantUi.hasUnread = true;
    }
    assistantConnection.lastEventAt = new Date().toISOString();
    refreshSessions();
  }
  return changed;
}

function subscriptionFilters(operatorPubkey, servicePubkey) {
  const since = nowSeconds() - RECENT_TRANSCRIPT_SECONDS;
  return [
    { kinds: [ASSISTANT_KINDS.SESSION], authors: [servicePubkey], '#p': [operatorPubkey], '#schema': ['bahia.assistant-session.v1', 'bahia.assistant-session.v2'], limit: SESSION_LIMIT },
    { kinds: [ASSISTANT_KINDS.STATUS], authors: [servicePubkey], '#schema': ['bahia.assistant-status.v1'], since, limit: TRANSCRIPT_LIMIT },
    { kinds: [ASSISTANT_KINDS.TRANSCRIPT], authors: [servicePubkey], '#p': [operatorPubkey], '#schema': ['bahia.assistant-transcript.v1'], '#domain': ['assistant'], since, limit: TRANSCRIPT_LIMIT }
  ];
}

function subscribeToConnectionState() {
  if (connectedUnsubscribe) return;
  connectedUnsubscribe = nostr.connected.subscribe((connected) => {
    if (lastConnected && !connected && assistantConnection.status === 'live') assistantConnection.status = 'disconnected';
    if (!lastConnected && connected && assistantConnection.ready) assistantConnection.status = 'live';
    lastConnected = connected;
  });
}

function startSubscription(operatorPubkey, servicePubkey) {
  if (liveUnsubscribe) liveUnsubscribe();
  let historicalCatchupComplete = false;
  liveUnsubscribe = nostr.subscribeWithRecovery(subscriptionFilters(operatorPubkey, servicePubkey), {
    onEvent: (event) => applyAssistantEvent(event, { allowStreaming: historicalCatchupComplete }),
    onEose: () => {
      historicalCatchupComplete = true;
      assistantConnection.ready = true;
      assistantConnection.status = 'live';
      refreshSessions();
    },
    onHealth: (health) => Object.assign(assistantConnection, health),
    onClosed: (reason, relay, meta = {}) => {
      assistantConnection.lastError = reason || `assistant subscription closed by ${relay}`;
      if (['live', 'reconnecting'].includes(assistantConnection.status)) {
        assistantConnection.status = meta.disconnected ? 'disconnected' : 'reconnecting';
      }
    }
  });
}

export function resetAssistantStore() {
  if (liveUnsubscribe) liveUnsubscribe();
  if (connectedUnsubscribe) connectedUnsubscribe();
  liveUnsubscribe = null;
  connectedUnsubscribe = null;
  bootstrapPromise = null;
  lastConnected = false;
  restoredTranscriptCacheKey = '';
  sessionMap.clear();
  eventMap.clear();
  pendingMap.clear();
  seenEventIds.clear();
  assistantSessions.length = 0;
  syncPendingRequests();
  assistantConnection.status = 'idle';
  assistantConnection.ready = false;
  assistantConnection.operatorPubkey = '';
  assistantConnection.servicePubkey = '';
  assistantConnection.lastError = null;
  assistantConnection.lastEoseAt = null;
  assistantConnection.resubscribeAttempts = 0;
  assistantConnection.lastClosedReason = null;
  assistantConnection.lastEventAt = null;
  assistantUi.panelOpen = false;
  assistantUi.activeSessionId = '';
  assistantUi.hasUnread = false;
  assistantUi.lastDismissedAt = 0;
}

export async function bootstrapAssistant({ force = false } = {}) {
  if (!browser) return { ok: false, reason: 'not_browser' };
  if (bootstrapPromise && !force) return bootstrapPromise;
  if (assistantConnection.ready && !force) return { ok: true };

  loadAssistantUiState();
  bootstrapPromise = (async () => {
    assistantConnection.status = 'waiting_auth';
    assistantConnection.lastError = null;

    try {
      if (authState.status !== 'authenticated' || !authState.pubkey) {
        return { ok: false, reason: 'waiting_for_auth' };
      }

      const controlplane = await bootstrapControlplane({ force });
      if (!controlplane.ok) throw new Error(controlplane.reason || 'controlplane bootstrap failed');

      const operatorPubkey = authState.pubkey;
      const servicePubkey = controlplaneConnection.servicePubkey;
      if (!servicePubkey) throw new Error('Assistant bootstrap requires service pubkey from controlplane discovery');

      assistantConnection.status = 'bootstrapping';
      assistantConnection.operatorPubkey = operatorPubkey;
      assistantConnection.servicePubkey = servicePubkey;
      subscribeToConnectionState();
      restoreAssistantTranscriptCache(operatorPubkey, servicePubkey);

      startSubscription(operatorPubkey, servicePubkey);
      return { ok: true };
    } catch (err) {
      assistantConnection.status = 'error';
      assistantConnection.ready = false;
      assistantConnection.lastError = err?.message || String(err);
      return { ok: false, reason: assistantConnection.lastError };
    } finally {
      bootstrapPromise = null;
    }
  })();

  return bootstrapPromise;
}

export function disconnectAssistant() {
  if (liveUnsubscribe) liveUnsubscribe();
  liveUnsubscribe = null;
  assistantConnection.status = assistantConnection.ready ? 'disconnected' : 'idle';
}

export function toggleAssistantPanel() {
  assistantUi.panelOpen = !assistantUi.panelOpen;
  if (assistantUi.panelOpen) {
    assistantUi.hasUnread = false;
    assistantUi.lastDismissedAt = nowSeconds();
  } else {
    assistantUi.lastDismissedAt = nowSeconds();
  }
  persistAssistantUiState();
}

export function openAssistantPanel() {
  assistantUi.panelOpen = true;
  assistantUi.hasUnread = false;
  assistantUi.lastDismissedAt = nowSeconds();
  persistAssistantUiState();
}

export function closeAssistantPanel() {
  assistantUi.panelOpen = false;
  assistantUi.lastDismissedAt = nowSeconds();
  persistAssistantUiState();
}

export function setActiveAssistantSession(sessionId) {
  assistantUi.activeSessionId = sessionId || '';
  persistAssistantUiState();
  persistAssistantTranscriptCache();
}

export function createAssistantSessionId() {
  const random = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return `assistant-${random}`;
}

function assistantAcknowledgementItem(response, fallbackSessionId = '') {
  const payload = response?.result || {};
  const sessionId = fallbackSessionId || payload.session_id || payload.sessionId || '';
  return {
    type: 'status', id: response?.resultEvent?.id || response?.requestEventId || `assistant-ack:${sessionId}:${Date.now()}`,
    kind: ASSISTANT_KINDS.CONTEXTVM_RESULT, pubkey: assistantConnection.servicePubkey,
    createdAt: response?.resultEvent?.created_at || nowSeconds(), sessionId,
    status: 'request_acknowledged', requestEventId: response?.requestEventId || '',
    runId: payload.run_id || '', message: 'Assistant request acknowledged; awaiting canonical execution state.',
    event: null
  };
}

export async function publishAssistantPrompt({ prompt, sessionId, workflow = '', routeContext = null, selectedRefs = [], signal } = {}) {
  const cleanPrompt = String(prompt || '').trim();
  if (!cleanPrompt) throw assistantRequestError(INVALID, 'Prompt is required');
  const resolvedSessionId = sessionId || assistantUi.activeSessionId || activeAssistantSession()?.sessionId || createAssistantSessionId();
  const existing = sessionMap.get(resolvedSessionId);
  if (existing?.executionVersion === 1) throw assistantRequestError(INVALID, 'Historical v1 sessions are read-only; start a new session');
  if (existing?.executionVersion === 2 && !TERMINAL_PHASES.includes(existing.phase)) throw assistantRequestError(INVALID, 'An assistant run is already active');
  if (Array.from(pendingMap.values()).some((value) => value.sessionId === resolvedSessionId)) throw assistantRequestError(INVALID, 'An assistant request is already pending');
  if (workflow && !['batch', 'iterative'].includes(workflow)) throw assistantRequestError(INVALID, 'Invalid assistant workflow');
  if (existing?.workflow && workflow && workflow !== existing.workflow &&
      (!TERMINAL_PHASES.includes(existing.phase) || existing.uncertainEffects)) throw assistantRequestError(INVALID, 'Workflow change requires a finished run with no unresolved effects');
  const selectedWorkflow = workflow || existing?.workflow || '';
  const turnId = globalThis.crypto?.randomUUID?.() || `${Date.now()}`;
  const content = {
    contract_version: 2,
    prompt: cleanPrompt,
    session_id: resolvedSessionId,
    turn_id: turnId,
    route_context: routeContext || null,
    selected_refs: Array.isArray(selectedRefs) ? selectedRefs : []
  };
  if (selectedWorkflow) content.workflow = selectedWorkflow;

  applyLocalAssistantItem({
    type: 'prompt',
    id: `assistant-prompt:${resolvedSessionId}:${turnId}`,
    kind: ASSISTANT_KINDS.CONTEXTVM_RESULT,
    pubkey: assistantConnection.operatorPubkey,
    createdAt: nowSeconds(),
    sessionId: resolvedSessionId,
    turnId,
    prompt: cleanPrompt,
    routeContext: content.route_context,
    selectedRefs: content.selected_refs
  });
  const pendingItem = assistantPendingItem({ sessionId: resolvedSessionId, turnId, prompt: cleanPrompt });
  applyLocalAssistantItem(pendingItem);
  pendingMap.set(pendingItem.id, {
    sessionId: resolvedSessionId,
    turnId,
    status: 'planning',
    startedAt: new Date().toISOString()
  });
  syncPendingRequests();
  setActiveAssistantSession(resolvedSessionId);

  try {
    const response = await requestEncryptedResult({
      operation: 'assistant/prompt',
      payload: content,
      tags: [['session', resolvedSessionId], ['turn', turnId]],
      signal,
      timeoutMs: ASSISTANT_PROMPT_TIMEOUT_MS
    });
    assertAssistantRequestAccepted(response);

    pendingMap.delete(pendingItem.id);
    syncPendingRequests();
    removeLocalAssistantItem(pendingItem.id);
    applyLocalAssistantItem(assistantAcknowledgementItem(response, resolvedSessionId));
    return response;
  } catch (err) {
    pendingMap.delete(pendingItem.id);
    syncPendingRequests();
    removeLocalAssistantItem(pendingItem.id);
    applyLocalAssistantItem(assistantRequestOutcomeItem(err, { sessionId: resolvedSessionId, turnId }));
    throw err;
  }
}

function currentV2Session(sessionId, runId) {
  const session = sessionMap.get(sessionId);
  if (!session?.authoritative || session.executionVersion !== 2 || !session.currentRunId || session.currentRunId !== runId) {
    throw assistantRequestError(STALE, 'Current v2 run required; reload the session', 'run_not_current');
  }
  return session;
}

function requestId() {
  return globalThis.crypto?.randomUUID?.() || createAssistantSessionId();
}

async function batchApprovalHash(input) {
  try {
    return (await computeAssistantBatchApprovalHash(input)).hash;
  } catch (err) {
    throw assistantRequestError(INVALID, `Plan cannot be approved: ${err?.message || String(err)}`);
  }
}

export async function publishAssistantApproval({ sessionId, runId, proposalId, baseRevision, basePlanHash,
  decision, message = '', modifiedPlan = null, signal } = {}) {
  if (!['approve', 'reject'].includes(decision)) throw assistantRequestError(INVALID, 'Decision must be approve or reject');
  const session = currentV2Session(sessionId, runId);
  const proposal = session.proposal;
  if (session.workflow !== 'batch' || session.phase !== 'awaiting_approval' || !proposal ||
    proposal.proposal_id !== proposalId || proposal.revision !== baseRevision || proposal.hash !== basePlanHash ||
    !session.pendingApprovals.includes(proposalId)) throw assistantRequestError(STALE, 'Proposal changed; reload and review the current proposal', 'proposal_changed');
  // Recompute the base hash from the public projection: a mismatch means this
  // browser is not looking at what the service will verify, so nothing is sent.
  const baseHash = await batchApprovalHash({ sessionId, runId, proposalId, revision: baseRevision, scope: session.scope, plan: proposal.plan });
  if (baseHash !== basePlanHash) throw assistantRequestError(STALE, 'Proposal hash does not match the public scope commitment; reload the current proposal', 'proposal_hash_mismatch');
  // Only an approval can carry edits; a rejection closes the base revision.
  const edited = decision === 'approve' && Boolean(modifiedPlan);
  const approvedRevision = edited ? baseRevision + 1 : baseRevision;
  const approvedHash = edited
    ? await batchApprovalHash({ sessionId, runId, proposalId, revision: approvedRevision, scope: session.scope, plan: modifiedPlan })
    : baseHash;
  const content = { contract_version: 2, request_id: requestId(), session_id: sessionId,
    run_id: runId, workflow: 'batch', proposal_id: proposalId, base_revision: baseRevision,
    base_plan_hash: basePlanHash, approved_revision: approvedRevision, approved_plan_hash: approvedHash, decision };
  if (message) content.message = message;
  if (edited) content.modified_plan = normalizeAssistantExecutablePlan(modifiedPlan);
  const response = await requestEncryptedResult({ operation: 'assistant/approval', payload: content,
    tags: [['session', sessionId], ['run', runId], ['decision', decision]], signal,
    timeoutMs: ASSISTANT_APPROVAL_TIMEOUT_MS });
  assertAssistantRequestAccepted(response);
  applyLocalAssistantItem(assistantAcknowledgementItem(response, sessionId));
  return response;
}

export async function publishAssistantActionDecision({ sessionId, runId, actionId, decision, reason = '', signal } = {}) {
  if (!['approve', 'reject'].includes(decision)) throw assistantRequestError(INVALID, 'Action decision must be approve or reject');
  const session = currentV2Session(sessionId, runId);
  if (session.workflow !== 'iterative' || session.phase !== 'awaiting_approval' || !session.pendingApprovals.includes(actionId)) {
    throw assistantRequestError(STALE, 'Action changed; reload the current run', 'action_not_pending');
  }
  const content = { contract_version: 2, request_id: requestId(), session_id: sessionId,
    run_id: runId, workflow: 'iterative', action_id: actionId, decision };
  if (reason) content.reason = reason;
  const response = await requestEncryptedResult({ operation: 'assistant/approval', payload: content,
    tags: [['session', sessionId], ['run', runId], ['action', actionId], ['decision', decision]],
    signal, timeoutMs: ASSISTANT_APPROVAL_TIMEOUT_MS });
  assertAssistantRequestAccepted(response);
  applyLocalAssistantItem(assistantAcknowledgementItem(response, sessionId));
  return response;
}

export async function publishAssistantCancellation({ sessionId, runId, scope = 'run', reason = '', signal } = {}) {
  if (!['run', 'session'].includes(scope)) throw assistantRequestError(INVALID, 'Cancellation scope must be run or session');
  const session = currentV2Session(sessionId, runId);
  if (!CANCELLABLE_PHASES.includes(session.phase)) throw assistantRequestError(STALE, 'Run is no longer cancellable', 'run_not_cancellable');
  const content = { contract_version: 2, session_id: sessionId, run_id: runId, scope };
  if (reason) content.reason = reason;
  return assertAssistantRequestAccepted(await requestEncryptedResult({ operation: 'assistant/cancel', payload: content,
    tags: [['session', sessionId], ['run', runId]], signal, timeoutMs: ASSISTANT_APPROVAL_TIMEOUT_MS }));
}

export async function publishAssistantReconciliation({ sessionId, runId, workId, requestEventId, signal } = {}) {
  if (!workId?.trim() || !/^[0-9a-f]{64}$/.test(requestEventId || '')) throw assistantRequestError(INVALID, 'Exact work ID and 64-character request-event ID required');
  const session = currentV2Session(sessionId, runId);
  if (!session.uncertainEffects) throw assistantRequestError(STALE, 'No uncertain work in this run', 'no_uncertain_work');
  return assertAssistantRequestAccepted(await requestEncryptedResult({ operation: 'assistant/reconcile',
    payload: { contract_version: 2, session_id: sessionId, run_id: runId,
      work_id: workId.trim(), request_event_id: requestEventId },
    tags: [['session', sessionId], ['run', runId]], signal, timeoutMs: ASSISTANT_APPROVAL_TIMEOUT_MS }));
}

export function downstreamRequestsForTurn(item) {
  const ids = new Set();
  if (item?.downstreamRequestId) ids.add(item.downstreamRequestId);
  for (const value of getTagValues(item?.event, 'downstream-request')) ids.add(value);
  const receiptId = item?.receipt?.request_event_id || item?.receipt?.requestEventId || item?.receipt?.RequestEventID;
  if (receiptId) ids.add(receiptId);
  if (Array.isArray(item?.downstreamResults)) {
    for (const result of item.downstreamResults) {
      if (result?.request_event_id) ids.add(result.request_event_id);
      if (result?.requestEventId) ids.add(result.requestEventId);
    }
  }
  return Array.from(ids);
}

if (browser) loadAssistantUiState();
