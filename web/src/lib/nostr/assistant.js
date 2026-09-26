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

// The v2 contract hashes executable input, not previews, dispatch keys, or
// private command arguments. Object keys use UTF-16 ordering (RFC 8785/JCS).
function assertIJson(value) {
  if (typeof value === 'string') {
    if (/[\uD800-\uDBFF](?![\uDC00-\uDFFF])|(?<![\uD800-\uDBFF])[\uDC00-\uDFFF]/u.test(value)) throw new Error('Invalid Unicode in assistant JSON');
  } else if (typeof value === 'number' && !Number.isFinite(value)) {
    throw new Error('Non-finite assistant JSON number');
  } else if (Array.isArray(value)) {
    value.forEach(assertIJson);
  } else if (value && typeof value === 'object') {
    for (const [key, child] of Object.entries(value)) { assertIJson(key); assertIJson(child); }
  } else if (value === undefined || typeof value === 'bigint' || typeof value === 'function') {
    throw new Error('Invalid assistant JSON value');
  }
}

export function canonicalAssistantJson(value) {
  assertIJson(value);
  function encode(item) {
    if (Array.isArray(item)) return `[${item.map(encode).join(',')}]`;
    if (item && typeof item === 'object') {
      return `{${Object.keys(item).sort().map((key) => `${JSON.stringify(key)}:${encode(item[key])}`).join(',')}}`;
    }
    return JSON.stringify(item);
  }
  return encode(value);
}

export function parseAssistantArgumentObjectText(text) {
  let index = 0;
  const skip = () => { while (/\s/.test(text[index] || '')) index++; };
  function readString() {
    const start = index++;
    while (index < text.length) {
      if (text[index] === '\\') { index += 2; continue; }
      if (text[index++] === '"') return JSON.parse(text.slice(start, index));
    }
    throw new Error('Unterminated JSON string');
  }
  function scan() {
    skip();
    if (text[index] === '{') {
      index++; skip();
      const keys = new Set();
      if (text[index] === '}') { index++; return; }
      while (index < text.length) {
        skip();
        if (text[index] !== '"') throw new Error('JSON object key expected');
        const key = readString();
        if (keys.has(key)) throw new Error(`Duplicate JSON key: ${key}`);
        keys.add(key); skip();
        if (text[index++] !== ':') throw new Error('JSON colon expected');
        scan(); skip();
        if (text[index] === '}') { index++; return; }
        if (text[index++] !== ',') throw new Error('JSON comma expected');
      }
    } else if (text[index] === '[') {
      index++; skip();
      if (text[index] === ']') { index++; return; }
      while (index < text.length) {
        scan(); skip();
        if (text[index] === ']') { index++; return; }
        if (text[index++] !== ',') throw new Error('JSON comma expected');
      }
    } else if (text[index] === '"') {
      readString();
    } else {
      const start = index;
      while (index < text.length && !/[\s,}\]]/.test(text[index])) index++;
      JSON.parse(text.slice(start, index));
    }
  }
  scan(); skip();
  if (index !== text.length) throw new Error('Trailing JSON value');
  const value = JSON.parse(text);
  if (!value || typeof value !== 'object' || Array.isArray(value)) throw new Error('Arguments must be a JSON object');
  canonicalAssistantJson(value);
  return value;
}

export function normalizeAssistantExecutablePlan(plan) {
  if (!plan || typeof plan !== 'object' || Array.isArray(plan)) throw new Error('Plan must be an object');
  const permitted = new Set(['summary', 'needs_clarification', 'clarifying_question', 'risk_level', 'context_refs', 'steps']);
  for (const key of Object.keys(plan)) if (!permitted.has(key)) throw new Error(`Unknown plan field: ${key}`);
  if (typeof plan.summary !== 'string' || typeof plan.needs_clarification !== 'boolean' || typeof plan.risk_level !== 'string' ||
      (plan.clarifying_question !== undefined && typeof plan.clarifying_question !== 'string') ||
      (plan.context_refs !== undefined && (!Array.isArray(plan.context_refs) || plan.context_refs.some((ref) => typeof ref !== 'string')))) throw new Error('Invalid plan field types');
  if (!Array.isArray(plan.steps)) throw new Error('Plan steps must be an array');
  const seen = new Set();
  const steps = plan.steps.map((step, index) => {
    if (!step || typeof step !== 'object' || Array.isArray(step)) throw new Error(`Invalid step ${index + 1}`);
    const allowed = new Set(['step_id', 'title', 'description', 'tool_name', 'tool_args', 'args_preview', 'idempotency_key']);
    for (const key of Object.keys(step)) if (!allowed.has(key)) throw new Error(`Unknown step field: ${key}`);
    const stepId = step.step_id;
    if (typeof stepId !== 'string' || !stepId.trim() || typeof step.tool_name !== 'string' || !step.tool_name.trim()) throw new Error(`Step ${index + 1} lacks identity or tool`);
    if (typeof step.title !== 'string' || typeof step.description !== 'string') throw new Error(`Step ${stepId} has invalid text fields`);
    if (seen.has(stepId)) throw new Error(`Duplicate step ID: ${stepId}`);
    seen.add(stepId);
    if (!step.tool_args || typeof step.tool_args !== 'object' || Array.isArray(step.tool_args)) throw new Error(`Step ${stepId} arguments must be a JSON object`);
    return { step_id: stepId, title: step.title || '', description: step.description || '', tool_name: step.tool_name, tool_args: step.tool_args };
  });
  const normalized = {
    summary: plan.summary || '', needs_clarification: Boolean(plan.needs_clarification),
    risk_level: plan.risk_level || '', steps
  };
  if (plan.clarifying_question) normalized.clarifying_question = plan.clarifying_question;
  if (Array.isArray(plan.context_refs) && plan.context_refs.length) normalized.context_refs = plan.context_refs;
  return normalized;
}

export function assistantBatchApprovalEnvelope({ sessionId, runId, proposalId, revision, scope, plan }) {
  if (!sessionId || !runId || !proposalId || !Number.isSafeInteger(revision) || revision < 1) throw new Error('Invalid batch approval identity');
  if (!scope || !Object.hasOwn(scope, 'allowed_tools') || Object.hasOwn(scope, 'arguments')) throw new Error('Public command-scope commitment required');
  if ((scope.command_name !== undefined && typeof scope.command_name !== 'string') ||
      (scope.selected_refs !== undefined && (!Array.isArray(scope.selected_refs) || scope.selected_refs.some((ref) => typeof ref !== 'string')))) throw new Error('Invalid public scope fields');
  if (scope.allowed_tools !== null && (!Array.isArray(scope.allowed_tools) || scope.allowed_tools.some((tool) => typeof tool !== 'string'))) throw new Error('Invalid allowed_tools commitment');
  if (scope.arguments_digest && !/^[0-9a-f]{64}$/.test(scope.arguments_digest)) throw new Error('Invalid arguments digest commitment');
  const publicScope = { allowed_tools: scope.allowed_tools };
  if (scope.command_name) publicScope.command_name = scope.command_name;
  if (Array.isArray(scope.selected_refs) && scope.selected_refs.length) publicScope.selected_refs = scope.selected_refs;
  if (scope.arguments_digest) publicScope.arguments_digest = scope.arguments_digest;
  return { version: 2, session_id: sessionId, run_id: runId, workflow: 'batch', proposal_id: proposalId,
    revision, scope: publicScope, plan: normalizeAssistantExecutablePlan(plan) };
}

export async function computeAssistantBatchApprovalHash(input) {
  const canonical = canonicalAssistantJson(assistantBatchApprovalEnvelope(input));
  return { canonical, hash: await sha256Hex(canonical) };
}

// Operator requests fail in four distinguishable ways. Only a JSON-RPC error
// is a definitive service answer; a timeout, disconnect or abort leaves the
// outcome unknown and must never be presented as an execution failure.
export const ASSISTANT_REQUEST_ERROR_KINDS = Object.freeze({
  STALE: 'stale', // acted on superseded run/proposal/action state
  INVALID: 'invalid', // refused locally; nothing was sent
  REJECTED: 'rejected', // the service answered with a JSON-RPC error
  UNKNOWN: 'unknown' // transport interruption; outcome unknown
});

// Service reason codes meaning the operator acted on superseded run, proposal
// or action state (e.g. `stale_approval`, `plan_hash_mismatch`,
// `proposal_changed_requires_review`, `approval_contract_upgrade_required`).
const ASSISTANT_STALE_REASON = /stale|proposal_changed|requires_review|approval_contract_upgrade_required|hash_mismatch|not_current|run_changed|superseded|invalid_state|invalid_cancel/;
const ASSISTANT_REJECTED_RESULT_STATUSES = new Set(['failed', 'rejected', 'error']);

export function assistantRequestError(kind, message, code = '') {
  const error = new Error(message);
  error.assistantErrorKind = kind;
  if (code) error.code = code;
  return error;
}

// Assistant handlers answer business refusals with a successful ContextVM
// result `{status:"failed", step:"<reason>", error}`. That is the service's
// verdict on the *request*; it never describes downstream execution.
export function assertAssistantRequestAccepted(response) {
  const result = response?.result;
  const status = String(result?.status || '').toLowerCase();
  if (!result || !ASSISTANT_REJECTED_RESULT_STATUSES.has(status)) return response;
  const code = String(result.step || result.code || result.reason || status);
  const detail = String(result.error || result.summary || result.message || code);
  const error = new Error(detail.includes(code) ? detail : `${code}: ${detail}`);
  error.serviceResult = result;
  error.code = code;
  error.data = result;
  throw error;
}

export function classifyAssistantRequestError(error) {
  const detail = error?.message || String(error || 'Assistant request failed');
  if (Object.values(ASSISTANT_REQUEST_ERROR_KINDS).includes(error?.assistantErrorKind)) {
    return { kind: error.assistantErrorKind, detail };
  }
  if (error?.rpcError || error?.serviceResult) {
    const reasons = [error.code, error.data?.code, error.data?.reason, error.data?.step, detail]
      .filter((value) => typeof value === 'string').join(' ').toLowerCase();
    return { kind: ASSISTANT_STALE_REASON.test(reasons) ? ASSISTANT_REQUEST_ERROR_KINDS.STALE : ASSISTANT_REQUEST_ERROR_KINDS.REJECTED, detail };
  }
  return { kind: ASSISTANT_REQUEST_ERROR_KINDS.UNKNOWN, detail };
}

export function describeAssistantRequestError(error, subject = 'Request') {
  const { kind, detail } = classifyAssistantRequestError(error);
  if (kind === ASSISTANT_REQUEST_ERROR_KINDS.UNKNOWN) return { kind, detail, message: `${subject} outcome unknown / reconnecting: ${detail}` };
  if (kind === ASSISTANT_REQUEST_ERROR_KINDS.REJECTED) return { kind, detail, message: `${subject} rejected by the assistant service: ${detail}` };
  if (kind === ASSISTANT_REQUEST_ERROR_KINDS.STALE) return { kind, detail, message: `${subject} refers to superseded state: ${detail}` };
  return { kind, detail, message: `${subject} not sent: ${detail}` };
}

export const ASSISTANT_SESSION_SCHEMA_V1 = 'bahia.assistant-session.v1';
export const ASSISTANT_SESSION_SCHEMA_V2 = 'bahia.assistant-session.v2';
export const ASSISTANT_SESSION_SCHEMA = ASSISTANT_SESSION_SCHEMA_V1;
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

// v2 execution phases (internal/domain/assistant_execution.go).
export const ASSISTANT_EXECUTION_TERMINAL_PHASES = Object.freeze(['completed', 'failed', 'cancelled']);
export const ASSISTANT_EXECUTION_CANCELLABLE_PHASES = Object.freeze(['proposing', 'awaiting_approval', 'executing', 'waiting_async', 'blocked']);

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
  const schema = getTagValue(event, 'schema', '');
  if (![ASSISTANT_SESSION_SCHEMA_V1, ASSISTANT_SESSION_SCHEMA_V2].includes(schema)) return null;
  const content = parseJsonContent(event, {});
  const v2 = schema === ASSISTANT_SESSION_SCHEMA_V2 && content.execution_version === 2;
  if (schema === ASSISTANT_SESSION_SCHEMA_V2 && (!v2 || Object.hasOwn(content.scope || {}, 'arguments') || Object.hasOwn(content, 'command_scope'))) return null;
  const sessionId = getTagValue(event, 'session', content.session_id || getDTag(event));
  if (v2 && (getDTag(event) !== `${schema}:${sessionId}` || content.session_id !== sessionId)) return null;
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
    schema,
    executionVersion: v2 ? 2 : 1,
    workflow: v2 ? content.workflow || '' : '',
    currentRunId: v2 ? content.current_run_id || '' : '',
    executionRevision: v2 ? content.execution_revision || 0 : 0,
    phase: v2 ? content.phase || '' : '',
    scope: v2 ? content.scope || null : null,
    proposal: v2 ? content.proposal || null : null,
    pendingApprovals: v2 && Array.isArray(content.pending_approvals) ? content.pending_approvals : [],
    submittedEffects: v2 ? Number(content.submitted_effects || 0) : 0,
    uncertainEffects: v2 ? Number(content.uncertain_effects || 0) : 0,
    checkpointEventId: v2 ? content.checkpoint_event_id || '' : '',
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
    turnId: getTagValue(event, 'turn', content.turn_id || content.turnId || ''),
    runId: content.run_id || content.runId || '',
    iteration: Number(content.iteration || 0),
    planHash: getTagValue(event, 'plan-hash', content.plan_hash || ''),
    stepId: getTagValue(event, 'step', content.step_id || ''),
    downstreamRequestId: getTagValue(event, 'downstream-request', content.downstream_request_id || content.downstreamRequest || content.downstream_request || ''),
    actionId,
    toolCallId,
    toolName,
    argsPreview,
    observationId: getTagValue(event, 'observation', content.observation_id || content.observationId || ''),
    observationStatus: content.observation_status || content.observationStatus || '',
    subagent: content.subagent || content.subagent_name || content.subagentName || '',
    command: content.command || '',
    approvalPrompt: content.approval_prompt || content.approvalPrompt || '',
    permission: content.permission || null,
    summary: content.summary || '',
    error: content.error || '',
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
    toolCalls: Array.isArray(message?.tool_calls) ? message.tool_calls : [],
    observation: message?.observation || null,
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
    turnId: getTagValue(event, 'turn', content.turn_id || content.turnId || ''),
    runId: content.run_id || content.runId || '',
    iteration: Number(content.iteration || 0),
    planHash: getTagValue(event, 'plan-hash', content.plan_hash || ''),
    downstreamRequestId: getTagValue(event, 'downstream-request', content.downstream_request_id || content.downstreamRequest || content.downstream_request || ''),
    actionId: getTagValue(event, 'action', content.action_id || content.actionId || ''),
    toolCallId: getTagValue(event, 'tool-call', content.tool_call_id || content.toolCallId || ''),
    toolName: getTagValue(event, 'tool', content.tool_name || content.toolName || ''),
    phase: content.phase || '',
    observationStatus: content.observation_status || content.observationStatus || '',
    subagent: content.subagent || content.subagent_name || content.subagentName || '',
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
