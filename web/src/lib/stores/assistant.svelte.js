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
  computeAssistantPlanHash
} from '../nostr/client.js';

const SIDEBAR_STORAGE_KEY = 'bahia_assistant_sidebar';
const TRANSCRIPT_STORAGE_SCHEMA = 'bahia_assistant_transcript_v1';
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
  return assistantSessions.find((session) => session.sessionId === assistantUi.activeSessionId) || assistantSessions[0] || null;
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

function assistantTranscriptStorageKey(operatorPubkey = assistantConnection.operatorPubkey, servicePubkey = assistantConnection.servicePubkey) {
  const operator = String(operatorPubkey || '').trim();
  const service = String(servicePubkey || '').trim();
  if (!browser || !operator) return '';
  return `${TRANSCRIPT_STORAGE_PREFIX}:${TRANSCRIPT_STORAGE_SCHEMA}:${operator}:${service || 'unknown-service'}`;
}

function cacheableTranscriptItems() {
  return sortTranscript(Array.from(eventMap.values()).filter((item) => !item?.pending)).slice(-TRANSCRIPT_LIMIT);
}

function serializableSession(session) {
  const copy = { ...(session || {}) };
  delete copy.transcript;
  return copy;
}

function rememberSeenEventIds(item) {
  if (item?.id) seenEventIds.add(item.id);
  if (item?.event?.id) seenEventIds.add(item.event.id);
  if (item?.sessionEvent?.id) seenEventIds.add(item.sessionEvent.id);
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
    const cached = JSON.parse(localStorage.getItem(key) || 'null');
    if (!cached || cached.schema !== TRANSCRIPT_STORAGE_SCHEMA) return false;
    if (cached.operatorPubkey && cached.operatorPubkey !== operatorPubkey) return false;
    if (cached.servicePubkey && servicePubkey && cached.servicePubkey !== servicePubkey) return false;

    let restored = false;
    for (const cachedSession of Array.isArray(cached.sessions) ? cached.sessions : []) {
      const sessionId = String(cachedSession?.sessionId || '').trim();
      if (!sessionId) continue;
      const session = ensureSession(sessionId);
      Object.assign(session, { ...session, ...cachedSession, transcript: [] });
      rememberSeenEventIds(session);
      restored = true;
    }

    for (const item of Array.isArray(cached.transcript) ? cached.transcript : []) {
      const sessionId = String(item?.sessionId || '').trim();
      const itemId = String(item?.id || '').trim();
      if (!sessionId || !itemId || item?.pending) continue;
      const session = ensureSession(sessionId);
      eventMap.set(itemId, item);
      session.updatedAt = Math.max(session.updatedAt || 0, item.createdAt || item.event?.created_at || 0);
      rememberSeenEventIds(item);
      restored = true;
    }

    if (typeof cached.activeSessionId === 'string') assistantUi.activeSessionId = cached.activeSessionId;
    if (restored) refreshSessions();
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

function normalizePendingAction(action = {}) {
  const actionId = String(action.action_id || action.actionId || action.ActionID || '').trim();
  if (!actionId) return null;
  return {
    actionId,
    sessionId: action.session_id || action.sessionId || action.SessionID || '',
    runId: action.run_id || action.runId || action.RunID || '',
    turnId: action.turn_id || action.turnId || action.TurnID || '',
    toolCallId: action.tool_call_id || action.toolCallId || action.ToolCallID || '',
    toolName: action.tool_name || action.toolName || action.ToolName || '',
    argsPreview: action.args_preview || action.argsPreview || action.tool_args || action.toolArgs || action.ToolArgs || null,
    approvalPrompt: action.approval_prompt || action.approvalPrompt || action.ApprovalPrompt || '',
    permission: action.permission || action.Permission || null,
    createdAt: action.created_at || action.createdAt || action.CreatedAt || ''
  };
}

function pendingActionsFromMetadata(metadata = {}) {
  const raw = metadata?.deferred_actions || metadata?.deferredActions || {};
  const values = Array.isArray(raw) ? raw : Object.values(raw || {});
  return values.map(normalizePendingAction).filter(Boolean);
}

function pendingActionFromStatus(item) {
  if (item?.type !== 'status' || item.phase !== 'approval_required' || !item.actionId) return null;
  return normalizePendingAction({
    action_id: item.actionId,
    session_id: item.sessionId,
    tool_call_id: item.toolCallId,
    tool_name: item.toolName,
    args_preview: item.argsPreview,
    approval_prompt: item.approvalPrompt || item.message,
    permission: item.permission,
    created_at: item.createdAt
  });
}

function upsertPendingAction(session, action) {
  const normalized = normalizePendingAction(action);
  if (!session || !normalized) return;
  const existing = Array.isArray(session.pendingActions) ? session.pendingActions : [];
  session.pendingActions = [normalized, ...existing.filter((item) => item.actionId !== normalized.actionId)];
}

function resolvePendingAction(session, actionId) {
  if (!session || !actionId || !Array.isArray(session.pendingActions)) return;
  session.pendingActions = session.pendingActions.filter((item) => item.actionId !== actionId);
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
  Object.assign(session, {
    ...session,
    state: parsed.state,
    operatorPubkey: parsed.operatorPubkey,
    participants: Array.isArray(parsed.participants) ? parsed.participants : [],
    assistantId: parsed.assistantId,
    assistantPubkey: parsed.assistantPubkey,
    currentTurnId: parsed.currentTurnId,
    currentRequestId: parsed.currentRequestId,
    lastPlanHash: parsed.lastPlanHash,
    currentPlan: parsed.currentPlan,
    pendingSteps: parsed.pendingSteps,
    transcriptSummary: parsed.transcriptSummary,
    metadata: parsed.content?.metadata || {},
    pendingActions: pendingActionsFromMetadata(parsed.content?.metadata || {}),
    lastResultId: parsed.lastResultId,
    updatedAt: Math.max(session.updatedAt || 0, parsed.createdAt || 0),
    sessionEvent: parsed
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

function recordAssistantItem(item) {
  if (!item?.sessionId || !item.id) return false;
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
  if ((item.type === 'status' || item.type === 'result' || item.type === 'approval') && item.planHash) session.lastPlanHash = item.planHash;
  if ((item.type === 'status' || item.type === 'result') && item.plan && !session.currentPlan) session.currentPlan = item.plan;
  if (item.type === 'status' && item.phase === 'approval_required') upsertPendingAction(session, pendingActionFromStatus(item));
  if (item.actionId && (
    item.type === 'approval'
    || (item.type === 'result' && item.status !== 'awaiting_approval')
    || (item.type === 'status' && item.phase && item.phase !== 'approval_required')
  )) {
    resolvePendingAction(session, item.actionId);
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

function assistantFailureItem(error, { sessionId, turnId }) {
  const detail = error?.message || String(error || 'Assistant request failed');
  return {
    type: 'result',
    id: `assistant-failed:${sessionId}:${turnId}:${Date.now()}`,
    kind: ASSISTANT_KINDS.CONTEXTVM_RESULT,
    pubkey: assistantConnection.servicePubkey,
    createdAt: nowSeconds(),
    sessionId,
    turnId,
    status: 'failed',
    failed: true,
    summary: 'assistant planning failed',
    error: detail,
    content: { status: 'failed', summary: 'assistant planning failed', error: detail },
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
    { kinds: [ASSISTANT_KINDS.SESSION], authors: [servicePubkey], '#p': [operatorPubkey], '#schema': ['bahia.assistant-session.v1'], limit: SESSION_LIMIT },
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
  liveUnsubscribe = nostr.subscribe(subscriptionFilters(operatorPubkey, servicePubkey), {
    onEvent: (event) => applyAssistantEvent(event, { allowStreaming: historicalCatchupComplete }),
    onEose: () => {
      historicalCatchupComplete = true;
      assistantConnection.ready = true;
      assistantConnection.status = 'live';
      assistantConnection.lastEoseAt = new Date().toISOString();
      refreshSessions();
    },
    onClosed: (reason, relay) => {
      assistantConnection.lastError = reason || `assistant subscription closed by ${relay}`;
      if (assistantConnection.status === 'live') assistantConnection.status = 'disconnected';
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

function assistantResultItem(response, fallbackSessionId = '') {
  const payload = response?.result || {};
  const sessionId = payload.session_id || payload.sessionId || fallbackSessionId;
  const status = payload.status || '';
  return {
    type: 'result',
    id: response?.resultEvent?.id || response?.requestEventId || `assistant-result:${sessionId}:${Date.now()}`,
    kind: ASSISTANT_KINDS.CONTEXTVM_RESULT,
    pubkey: response?.resultEvent?.pubkey || assistantConnection.servicePubkey,
    createdAt: response?.resultEvent?.created_at || nowSeconds(),
    sessionId,
    status,
    requestEventId: payload.request_event_id || response?.requestEventId || '',
    turnId: payload.turn_id || payload.turnId || '',
    runId: payload.run_id || payload.runId || '',
    iteration: Number(payload.iteration || 0),
    planHash: payload.plan_hash || payload.planHash || '',
    downstreamRequestId: payload.downstream_request_id || payload.downstreamRequestId || payload.downstream_request || payload.downstreamRequest || '',
    actionId: payload.action_id || payload.actionId || '',
    toolCallId: payload.tool_call_id || payload.toolCallId || '',
    toolName: payload.tool_name || payload.toolName || '',
    phase: payload.phase || '',
    observationStatus: payload.observation_status || payload.observationStatus || '',
    subagent: payload.subagent || payload.subagent_name || payload.subagentName || '',
    success: status === 'completed' || status === 'planned',
    blocked: status === 'blocked',
    failed: status === 'failed',
    rejected: status === 'rejected',
    cancelled: status === 'cancelled',
    needsClarification: status === 'needs_clarification',
    summary: payload.summary || payload.message || '',
    error: payload.error || '',
    plan: payload.plan || null,
    content: payload,
    event: response?.resultEvent || null
  };
}

export async function publishAssistantPrompt({ prompt, sessionId, routeContext = null, selectedRefs = [], signal } = {}) {
  const cleanPrompt = String(prompt || '').trim();
  if (!cleanPrompt) throw new Error('Prompt is required');
  const resolvedSessionId = sessionId || activeAssistantSession()?.sessionId || createAssistantSessionId();
  const turnId = globalThis.crypto?.randomUUID?.() || `${Date.now()}`;
  const content = {
    prompt: cleanPrompt,
    session_id: resolvedSessionId,
    turn_id: turnId,
    route_context: routeContext || null,
    selected_refs: Array.isArray(selectedRefs) ? selectedRefs : []
  };

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

    pendingMap.delete(pendingItem.id);
    syncPendingRequests();
    removeLocalAssistantItem(pendingItem.id);
    applyLocalAssistantItem(assistantResultItem(response, resolvedSessionId));
    return response;
  } catch (err) {
    pendingMap.delete(pendingItem.id);
    syncPendingRequests();
    removeLocalAssistantItem(pendingItem.id);
    applyLocalAssistantItem(assistantFailureItem(err, { sessionId: resolvedSessionId, turnId }));
    throw err;
  }
}

export async function publishAssistantApproval({ sessionId, planHash = '', actionId = '', decision, message = '', reason = '', modifiedPlan = null, signal } = {}) {
  if (!sessionId) throw new Error('sessionId is required');
  if (!planHash && !actionId) throw new Error('planHash or actionId is required');
  if (!['approve', 'reject', 'cancel'].includes(decision)) throw new Error('decision must be approve, reject, or cancel');
  if (actionId && !['approve', 'reject'].includes(decision)) throw new Error('action decision must be approve or reject');
  if (actionId && modifiedPlan) throw new Error('modifiedPlan is only valid for plan-hash approvals');
  const effectivePlanHash = !actionId && modifiedPlan ? await computeAssistantPlanHash(modifiedPlan, sessionId) : planHash;
  const content = { session_id: sessionId, decision };
  if (actionId) {
    content.action_id = actionId;
    if (reason || message) content.reason = reason || message;
  } else {
    content.plan_hash = effectivePlanHash;
    content.message = message;
    if (modifiedPlan) content.modified_plan = modifiedPlan;
  }

  applyLocalAssistantItem({
    type: 'approval',
    id: `assistant-approval:${sessionId}:${actionId || effectivePlanHash}:${decision}:${Date.now()}`,
    kind: ASSISTANT_KINDS.CONTEXTVM_RESULT,
    pubkey: assistantConnection.operatorPubkey,
    createdAt: nowSeconds(),
    sessionId,
    planHash: effectivePlanHash,
    actionId,
    decision,
    message: reason || message
  });

  const tags = actionId
    ? [['session', sessionId], ['action', actionId], ['decision', decision]]
    : [['session', sessionId], ['plan-hash', effectivePlanHash], ['decision', decision]];
  const response = await requestEncryptedResult({
    operation: 'assistant/approval',
    payload: content,
    tags,
    signal,
    timeoutMs: ASSISTANT_APPROVAL_TIMEOUT_MS
  });

  applyLocalAssistantItem(assistantResultItem(response, sessionId));
  return response;
}

export async function publishAssistantActionDecision({ sessionId, actionId, decision, reason = '', signal } = {}) {
  if (!actionId) throw new Error('actionId is required');
  if (!['approve', 'reject'].includes(decision)) throw new Error('decision must be approve or reject');
  return publishAssistantApproval({ sessionId, actionId, decision, reason, signal });
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
