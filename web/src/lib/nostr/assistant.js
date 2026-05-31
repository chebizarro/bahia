import { stableJsonValue, parseJsonContent } from './content.js';
import { KINDS } from './kinds.js';
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
  if (!event || event.kind !== KINDS.ASSISTANT_SESSION) return null;
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
