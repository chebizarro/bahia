import { stableJsonValue, parseJsonContent } from './content.js';
import { CAS_CONTROL_STATE, CONTEXTVM_MESSAGE, NIP38_STATUS } from './kinds.gen.js';
import { getDTag, getTagValue } from './tags.js';
import { sha256Hex } from './validation.js';

function normalizeAssistantPlanForHash(plan = {}) {
  const steps = Array.isArray(plan.steps) ? plan.steps : [];
  const normalized = {
    summary: plan.summary || '',
    needs_clarification: Boolean(plan.needs_clarification ?? plan.needsClarification)
  };
  const clarifyingQuestion = plan.clarifying_question || plan.clarifyingQuestion;
  if (clarifyingQuestion) normalized.clarifying_question = clarifyingQuestion;
  normalized.risk_level = plan.risk_level || plan.riskLevel || '';
  const contextRefs = plan.context_refs || plan.contextRefs;
  if (Array.isArray(contextRefs) && contextRefs.length > 0) normalized.context_refs = contextRefs;
  normalized.steps = steps.map((step = {}) => {
    const out = {
      step_id: step.step_id || step.stepId || '',
      title: step.title || '',
      description: step.description || '',
      tool_name: step.tool_name || step.toolName || '',
      tool_args: stableJsonValue(step.tool_args || step.toolArgs || {})
    };
    const argsPreview = step.args_preview || step.argsPreview;
    if (argsPreview && Object.keys(argsPreview).length > 0) out.args_preview = stableJsonValue(argsPreview);
    const idempotencyKey = step.idempotency_key || step.idempotencyKey;
    if (idempotencyKey) out.idempotency_key = idempotencyKey;
    return out;
  });
  return normalized;
}

export async function computeAssistantPlanHash(plan, sessionId) {
  if (!sessionId) throw new Error('sessionId is required to compute assistant plan hash');
  const payload = JSON.stringify({
    session_id: sessionId,
    plan: normalizeAssistantPlanForHash(plan)
  });
  return sha256Hex(payload);
}

export const ASSISTANT_SESSION_SCHEMA = 'bahia.assistant-session.v1';
export const ASSISTANT_STATUS_SCHEMA = 'bahia.assistant-status.v1';
export const ASSISTANT_TRANSCRIPT_SCHEMA = 'bahia.assistant-transcript.v1';
export const ASSISTANT_TRANSCRIPT_ENVELOPE = 'service-held-symmetric-key-aead';
export const ASSISTANT_TRANSCRIPT_KIND = 30316;

export const ASSISTANT_KINDS = {
  SESSION: CAS_CONTROL_STATE,
  STATUS: NIP38_STATUS,
  TRANSCRIPT: ASSISTANT_TRANSCRIPT_KIND,
  CONTEXTVM_RESULT: CONTEXTVM_MESSAGE
};

export const ASSISTANT_EVENT_KINDS = [ASSISTANT_KINDS.SESSION, ASSISTANT_KINDS.STATUS, ASSISTANT_KINDS.TRANSCRIPT];

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

function getTaggedPubkeyRefs(event, role = '') {
  return (event?.tags || [])
    .filter((candidate) => Array.isArray(candidate) && candidate[0] === 'p' && candidate[1] && (!role || candidate[3] === role))
    .map((candidate) => candidate[1]);
}

export function parseAssistantSessionEvent(event) {
  if (!event || event.kind !== ASSISTANT_KINDS.SESSION) return null;
  if (getTagValue(event, 'schema', '') !== ASSISTANT_SESSION_SCHEMA) return null;
  const content = parseJsonContent(event, {});
  const sessionId = getTagValue(event, 'session', content.session_id || getDTag(event));
  const state = getTagValue(event, 'status', content.state || ASSISTANT_SESSION_STATES.IDLE);
  const participants = Array.from(
    new Set([
      ...getTaggedPubkeyRefs(event, 'operator'),
      ...(Array.isArray(content.participants) ? content.participants : []),
      content.operator_pubkey || ''
    ].filter(Boolean))
  );

  return {
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    sessionId,
    state,
    operatorPubkey: getTaggedPubkeyRef(event, 'operator') || content.operator_pubkey || participants[0] || '',
    participants,
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
  if (!event || event.kind !== ASSISTANT_KINDS.STATUS) return null;
  if (getTagValue(event, 'schema', '') !== ASSISTANT_STATUS_SCHEMA) return null;
  const content = parseJsonContent(event, {});
  const status = getTagValue(event, 'status', content.status || '');
  const phase = getTagValue(event, 'phase', content.phase || '');
  const actionId = getTagValue(event, 'action', content.action_id || content.actionId || '');
  const toolCallId = getTagValue(event, 'tool-call', content.tool_call_id || content.toolCallId || '');
  const toolName = getTagValue(event, 'tool', content.tool_name || content.toolName || '');
  const argsPreview = content.args_preview || content.argsPreview || null;

  return {
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    sessionId: getTagValue(event, 'session', content.session_id || ''),
    assistantId: getTagValue(event, 'agent', content.assistant_id || ''),
    status,
    phase,
    requestEventId: getTaggedEventRef(event, 'reply') || content.request_event_id || '',
    planHash: getTagValue(event, 'plan-hash', content.plan_hash || ''),
    stepId: getTagValue(event, 'step', content.step_id || ''),
    downstreamRequestId: getTagValue(event, 'downstream-request', content.downstream_request_id || content.downstreamRequest || content.downstream_request || ''),
    actionId,
    toolCallId,
    toolName,
    argsPreview,
    observationId: getTagValue(event, 'observation', content.observation_id || content.observationId || ''),
    approvalPrompt: content.approval_prompt || content.approvalPrompt || '',
    permission: content.permission || null,
    message: content.message || content.summary || event.content || '',
    plan: content.plan || null,
    receipt: content.receipt || null,
    content,
    event
  };
}

function transcriptPayloadFromContent(content) {
  if (!content || typeof content !== 'object') return null;
  if (content.message || content.session_id || content.sessionId) return content;
  if (content.payload && typeof content.payload === 'object') return content.payload;
  return null;
}

function transcriptTextFromMessage(message) {
  if (!message || typeof message !== 'object') return '';
  if (typeof message.text === 'string') return message.text;
  const blocks = Array.isArray(message.content) ? message.content : [];
  const parts = [];
  for (const block of blocks) {
    if (!block || typeof block !== 'object') continue;
    if (typeof block.text === 'string' && block.text.trim()) parts.push(block.text);
    else if (block.json !== undefined) parts.push(JSON.stringify(block.json));
    else if (block.observation !== undefined) parts.push(JSON.stringify(block.observation));
  }
  if (parts.length > 0) return parts.join('\n');
  if (message.observation) return JSON.stringify(message.observation);
  return '';
}

export function parseAssistantTranscriptEvent(event) {
  if (!event || event.kind !== ASSISTANT_KINDS.TRANSCRIPT) return null;
  if (getTagValue(event, 'schema', '') !== ASSISTANT_TRANSCRIPT_SCHEMA) return null;
  const content = parseJsonContent(event, {});
  const payload = transcriptPayloadFromContent(content);
  const envelope = content?.envelope === ASSISTANT_TRANSCRIPT_ENVELOPE ? content : null;
  const message = payload?.message || null;
  const metadata = payload?.metadata || content?.metadata || {};
  const seq = Number(getTagValue(event, 'seq', payload?.seq ?? payload?.sequence ?? 0));

  return {
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    sessionId: getTagValue(event, 'session', payload?.session_id || payload?.sessionId || ''),
    assistantId: getTagValue(event, 'agent', payload?.assistant_id || ''),
    turnId: getTagValue(event, 'turn', payload?.turn_id || payload?.turnId || ''),
    runId: payload?.run_id || payload?.runId || metadata?.run_id || metadata?.runId || '',
    role: getTagValue(event, 'role', message?.role || ''),
    sequence: Number.isFinite(seq) ? seq : 0,
    phase: metadata?.phase || '',
    message,
    text: transcriptTextFromMessage(message),
    metadata,
    envelope,
    encrypted: Boolean(envelope),
    keyRef: getTagValue(event, 'key_ref', content?.key_ref || ''),
    keyVersion: getTagValue(event, 'key_version', content?.key_version || ''),
    content,
    event
  };
}

export function parseAssistantResultEvent(event) {
  if (!event || event.kind !== ASSISTANT_KINDS.CONTEXTVM_RESULT) return null;
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
    downstreamRequestId: getTagValue(event, 'downstream-request', content.downstream_request_id || content.downstreamRequest || content.downstream_request || ''),
    actionId: getTagValue(event, 'action', content.action_id || content.actionId || ''),
    toolCallId: getTagValue(event, 'tool-call', content.tool_call_id || content.toolCallId || ''),
    toolName: getTagValue(event, 'tool', content.tool_name || content.toolName || ''),
    phase: content.phase || '',
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
