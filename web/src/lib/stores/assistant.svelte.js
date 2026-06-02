import { browser } from '$app/environment';
import { authState } from './auth.js';
import { controlplaneConnection, bootstrapControlplane } from './controlplane.svelte.js';
import { publishRequest } from '../nostr/controlplane-requests.js';
import {
  nostr,
  ASSISTANT_KINDS,
  getTagValue,
  getTagValues,
  parseJsonContent,
  parseAssistantSessionEvent,
  parseAssistantStatusEvent,
  parseAssistantResultEvent,
  computeAssistantPlanHash
} from '../nostr/client.js';

const SIDEBAR_STORAGE_KEY = 'bahia_assistant_sidebar';
const RECENT_TRANSCRIPT_SECONDS = 14 * 24 * 60 * 60;
const TRANSCRIPT_LIMIT = 300;
const SESSION_LIMIT = 100;

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
    lastResultId: parsed.lastResultId,
    updatedAt: Math.max(session.updatedAt || 0, parsed.createdAt || 0),
    sessionEvent: parsed
  });
  return true;
}

function parsePromptEvent(event) {
  const content = parseJsonContent(event, {});
  const sessionId = eventSessionId(event, content) || content.session_id || content.sessionId;
  if (!sessionId) return null;
  return {
    type: 'prompt',
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    sessionId,
    turnId: getTagValue(event, 'turn', content.turn_id || content.turnId || getDTag(event)),
    prompt: content.prompt || content.message || event.content || '',
    routeContext: content.route_context || content.routeContext || null,
    selectedRefs: Array.isArray(content.selected_refs) ? content.selected_refs : [],
    event
  };
}

function parseApprovalEvent(event) {
  const content = parseJsonContent(event, {});
  const sessionId = eventSessionId(event, content) || content.session_id || content.sessionId;
  if (!sessionId) return null;
  return {
    type: 'approval',
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    sessionId,
    planHash: getTagValue(event, 'plan-hash', content.plan_hash || content.planHash || ''),
    decision: getTagValue(event, 'decision', content.decision || ''),
    message: content.message || '',
    event
  };
}

function normalizeAssistantItem(event) {
  if (event.kind === ASSISTANT_KINDS.PROMPT_REQUEST) return parsePromptEvent(event);
  if (event.kind === ASSISTANT_KINDS.APPROVAL) return parseApprovalEvent(event);
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
  if (event.kind === ASSISTANT_KINDS.RESULT) {
    const parsed = parseAssistantResultEvent(event);
    return parsed ? { ...parsed, type: 'result' } : null;
  }
  return null;
}

function authorAllowed(event) {
  if (event.kind === ASSISTANT_KINDS.PROMPT_REQUEST || event.kind === ASSISTANT_KINDS.APPROVAL) {
    if (!assistantConnection.operatorPubkey || event.pubkey === assistantConnection.operatorPubkey) return true;
    const sessionId = eventSessionId(event, parseJsonContent(event, {}));
    const session = sessionMap.get(sessionId);
    return Array.isArray(session?.participants) && session.participants.includes(event.pubkey);
  }
  if (event.kind === ASSISTANT_KINDS.STATUS || event.kind === ASSISTANT_KINDS.RESULT || event.kind === ASSISTANT_KINDS.SESSION) {
    return !assistantConnection.servicePubkey || event.pubkey === assistantConnection.servicePubkey;
  }
  return false;
}

function applyTranscriptEvent(event) {
  if (!authorAllowed(event)) return false;
  const item = normalizeAssistantItem(event);
  if (!item?.sessionId) return false;

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
    eventMap.set(event.id, item);
  }

  const session = sessionMap.get(item.sessionId);
  session.updatedAt = Math.max(session.updatedAt || 0, item.createdAt || 0);
  if ((item.type === 'status' || item.type === 'result') && item.planHash) session.lastPlanHash = item.planHash;
  if (item.type === 'status' && item.plan && !session.currentPlan) session.currentPlan = item.plan;
  if (item.type === 'result' && item.id) session.lastResultId = item.id;

  if (item.requestEventId && pendingMap.has(item.requestEventId) && item.type === 'result') {
    pendingMap.delete(item.requestEventId);
    syncPendingRequests();
  }
  return true;
}

function applyAssistantEvent(event) {
  if (!event?.id || seenEventIds.has(event.id)) return false;
  seenEventIds.add(event.id);

  let changed = false;
  if (event.kind === ASSISTANT_KINDS.SESSION) changed = applySessionEvent(event);
  else changed = applyTranscriptEvent(event);

  if (changed) {
    if (
      !assistantUi.panelOpen &&
      (event.kind === ASSISTANT_KINDS.STATUS || event.kind === ASSISTANT_KINDS.RESULT) &&
      event.created_at > assistantUi.lastDismissedAt
    ) {
      assistantUi.hasUnread = true;
    }
    assistantConnection.lastEventAt = new Date().toISOString();
    refreshSessions();
  }
  return changed;
}

function bootstrapFilters(operatorPubkey, servicePubkey) {
  const since = nowSeconds() - RECENT_TRANSCRIPT_SECONDS;
  return [
    { kinds: [ASSISTANT_KINDS.SESSION], authors: [servicePubkey], '#p': [operatorPubkey], limit: SESSION_LIMIT },
    { kinds: [ASSISTANT_KINDS.PROMPT_REQUEST, ASSISTANT_KINDS.APPROVAL], authors: [operatorPubkey], since, limit: TRANSCRIPT_LIMIT },
    { kinds: [ASSISTANT_KINDS.STATUS, ASSISTANT_KINDS.RESULT], authors: [servicePubkey], since, limit: TRANSCRIPT_LIMIT }
  ];
}

function liveFilters(operatorPubkey, servicePubkey, since) {
  return [
    { kinds: [ASSISTANT_KINDS.SESSION], authors: [servicePubkey], '#p': [operatorPubkey], since },
    { kinds: [ASSISTANT_KINDS.PROMPT_REQUEST, ASSISTANT_KINDS.APPROVAL], authors: [operatorPubkey], since },
    { kinds: [ASSISTANT_KINDS.STATUS, ASSISTANT_KINDS.RESULT], authors: [servicePubkey], since }
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

function startLiveSubscription({ operatorPubkey, servicePubkey, since }) {
  if (liveUnsubscribe) liveUnsubscribe();
  liveUnsubscribe = nostr.subscribe(liveFilters(operatorPubkey, servicePubkey, since), {
    onEvent: (event) => applyAssistantEvent(event),
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
    const liveSince = nowSeconds();
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

      const events = await nostr.queryUntilEose(bootstrapFilters(operatorPubkey, servicePubkey));
      for (const event of events) applyAssistantEvent(event);

      assistantConnection.ready = true;
      assistantConnection.status = 'live';
      assistantConnection.lastEoseAt = new Date().toISOString();
      startLiveSubscription({ operatorPubkey, servicePubkey, since: liveSince });
      refreshSessions();
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
}

export function createAssistantSessionId() {
  const random = globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`;
  return `assistant-${random}`;
}

export async function publishAssistantPrompt({ prompt, sessionId, routeContext = null, selectedRefs = [] } = {}) {
  const cleanPrompt = String(prompt || '').trim();
  if (!cleanPrompt) throw new Error('Prompt is required');
  const resolvedSessionId = sessionId || activeAssistantSession()?.sessionId || createAssistantSessionId();
  const turnId = globalThis.crypto?.randomUUID?.() || `${Date.now()}`;
  const d = `assistant-turn:${resolvedSessionId}:${turnId}`;
  const content = {
    prompt: cleanPrompt,
    session_id: resolvedSessionId,
    turn_id: turnId,
    route_context: routeContext || null,
    selected_refs: Array.isArray(selectedRefs) ? selectedRefs : []
  };

  const result = await publishRequest({
    kind: ASSISTANT_KINDS.PROMPT_REQUEST,
    tags: [['d', d], ['session', resolvedSessionId], ['turn', turnId]],
    content
  });

  pendingMap.set(result.requestEventId, {
    type: 'prompt',
    requestEventId: result.requestEventId,
    sessionId: resolvedSessionId,
    createdAt: nowSeconds(),
    status: 'still_waiting'
  });
  syncPendingRequests();
  applyAssistantEvent(result.event);
  setActiveAssistantSession(resolvedSessionId);
  return result;
}

export async function publishAssistantApproval({ sessionId, planHash, decision, message = '', modifiedPlan = null } = {}) {
  if (!sessionId) throw new Error('sessionId is required');
  if (!planHash) throw new Error('planHash is required');
  if (!['approve', 'reject', 'cancel'].includes(decision)) throw new Error('decision must be approve, reject, or cancel');
  const effectivePlanHash = modifiedPlan ? await computeAssistantPlanHash(modifiedPlan, sessionId) : planHash;
  const d = `assistant-approval:${sessionId}:${effectivePlanHash}`;
  const content = { session_id: sessionId, plan_hash: effectivePlanHash, decision, message };
  if (modifiedPlan) content.modified_plan = modifiedPlan;
  const result = await publishRequest({
    kind: ASSISTANT_KINDS.APPROVAL,
    tags: [['d', d], ['session', sessionId], ['plan-hash', effectivePlanHash], ['decision', decision]],
    content
  });

  pendingMap.set(result.requestEventId, {
    type: 'approval',
    requestEventId: result.requestEventId,
    sessionId,
    planHash: effectivePlanHash,
    decision,
    createdAt: nowSeconds(),
    status: 'still_waiting'
  });
  syncPendingRequests();
  applyAssistantEvent(result.event);
  return result;
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
