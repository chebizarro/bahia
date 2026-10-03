import { CONTEXTVM_MESSAGE, getTagValue, parseJsonContent } from './client.js';
import { requestEncryptedResult } from './encrypted-controlplane.js';

export const DNS_COMMANDS = {
  ZONE_CREATE: 'zone_create',
  POLICY_APPLY: 'policy_apply',
  RECORD_OVERRIDE: 'record_override',
  OVERRIDE_RETIRE: 'override_retire',
  DRIFT_REMEDIATE: 'drift_remediate'
};

export const DNS_CONTEXTVM_OPERATIONS = {
  [DNS_COMMANDS.DRIFT_REMEDIATE]: 'dns/drift-remediate'
};

export const DNS_ACTIONS = {
  [DNS_COMMANDS.DRIFT_REMEDIATE]: 'dns_drift_remediate'
};

function firstString(...values) {
  for (const value of values) {
    if (typeof value === 'string' && value.trim()) return value.trim();
  }
  return '';
}

function normalizeTags(tags = []) {
  if (!Array.isArray(tags)) return [];
  return tags
    .filter((tag) => Array.isArray(tag) && typeof tag[0] === 'string' && tag[0])
    .map((tag) => tag.map((value) => String(value)));
}

function addTag(tags, name, value) {
  if (value === null || value === undefined || value === '') return;
  tags.push([name, String(value)]);
}

function defaultTagsForCommand(command, payload = {}) {
  const tags = [];
  const zone = firstString(payload.zone, payload.zone_name, payload.name);

  if (command !== DNS_COMMANDS.DRIFT_REMEDIATE) throw new Error(`DNS ${command} uses signed intents`);
  addTag(tags, 'zone', zone);

  addTag(tags, 'action', DNS_ACTIONS[command]);
  addTag(tags, 'idempotency-key', firstString(payload.idempotency_key, payload.idempotencyKey));
  return tags;
}

export function buildDNSCommandRequest({ command, payload = {}, tags = [] } = {}) {
  const operation = DNS_CONTEXTVM_OPERATIONS[command];
  if (!operation) throw new Error(`Unknown DNS command: ${command}`);
  return {
    operation,
    tags: [...defaultTagsForCommand(command, payload), ...normalizeTags(tags)],
    payload
  };
}

function taggedRequestEventId(event) {
  const tag = (event?.tags || []).find((candidate) =>
    Array.isArray(candidate) && candidate[0] === 'e' && candidate[1] && (!candidate[3] || candidate[3] === 'reply')
  );
  return tag?.[1] || '';
}

function resultEventFromContextVM(response) {
  if (response?.resultEvent) return response.resultEvent;
  return {
    id: response?.requestEventId || '',
    kind: CONTEXTVM_MESSAGE,
    pubkey: '',
    created_at: Math.floor(Date.now() / 1000),
    tags: [['e', response?.requestEventId || '', '', 'reply']],
    content: JSON.stringify(response?.result ?? {})
  };
}

export function parseDNSOperationEvent(event) {
  const content = parseJsonContent(event, {});
  return {
    id: event?.id || '',
    kind: event?.kind,
    pubkey: event?.pubkey || '',
    createdAt: event?.created_at || 0,
    requestEventId: taggedRequestEventId(event) || content.request_event_id || content.requestEventId || '',
    action: getTagValue(event, 'action', content.action || ''),
    status: getTagValue(event, 'status', content.status || ''),
    step: getTagValue(event, 'step', content.step || ''),
    zone: getTagValue(event, 'zone', content.zone || ''),
    message: content.message || event?.content || '',
    error: getTagValue(event, 'error', content.error || ''),
    content,
    event
  };
}

export function dnsResultIsFailure(result) {
  const status = String(result?.status || '').toLowerCase();
  return status === 'error' || status === 'failed' || status === 'rejected';
}

export async function startDNSCommand({ command, payload = {}, tags = [], signal } = {}) {
  const request = buildDNSCommandRequest({ command, payload, tags });
  const response = await requestEncryptedResult({
    operation: request.operation,
    payload: request.payload,
    tags: request.tags,
    signal
  });
  const resultEvent = resultEventFromContextVM(response);
  const parsedResult = parseDNSOperationEvent(resultEvent);

  return {
    command,
    requestEventId: response.requestEventId,
    resultKind: CONTEXTVM_MESSAGE,
    request,
    event: response.event,
    ok: response.ok,
    acceptedRelays: response.acceptedRelays,
    rejectedRelays: response.rejectedRelays,
    result: Promise.resolve(parsedResult),
    unsubscribeStatus: () => {}
  };
}
