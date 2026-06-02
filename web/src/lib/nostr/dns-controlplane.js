import { CONTEXTVM_MESSAGE, getTagValue, parseJsonContent } from './client.js';
import { requestEncryptedResult } from './encrypted-controlplane.js';

export const DNS_COMMANDS = {
  ZONE_CREATE: 'zone_create',
  POLICY_APPLY: 'policy_apply',
  RECORD_OVERRIDE: 'record_override',
  DRIFT_REMEDIATE: 'drift_remediate'
};

export const DNS_CONTEXTVM_OPERATIONS = {
  [DNS_COMMANDS.ZONE_CREATE]: 'dns/zone-create',
  [DNS_COMMANDS.POLICY_APPLY]: 'dns/policy-apply',
  [DNS_COMMANDS.RECORD_OVERRIDE]: 'dns/record-set',
  [DNS_COMMANDS.DRIFT_REMEDIATE]: 'dns/drift-remediate'
};

export const DNS_ACTIONS = {
  [DNS_COMMANDS.ZONE_CREATE]: 'dns_zone_create',
  [DNS_COMMANDS.POLICY_APPLY]: 'dns_policy_apply',
  [DNS_COMMANDS.RECORD_OVERRIDE]: 'dns_record_override',
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

  switch (command) {
    case DNS_COMMANDS.ZONE_CREATE:
      addTag(tags, 'zone', zone);
      break;
    case DNS_COMMANDS.POLICY_APPLY:
      addTag(tags, 'policy', firstString(payload.policy_id, payload.id, payload.name));
      addTag(tags, 'zone', firstString(payload.zone, payload.zone_name, payload.zone_id));
      addTag(tags, 'environment', firstString(payload.environment_id));
      break;
    case DNS_COMMANDS.RECORD_OVERRIDE:
      addTag(tags, 'zone', firstString(payload.zone_name, payload.zone));
      addTag(tags, 'record', firstString(payload.record_name, payload.name));
      addTag(tags, 'record-type', firstString(payload.record_type, payload.type));
      break;
    case DNS_COMMANDS.DRIFT_REMEDIATE:
      addTag(tags, 'zone', zone);
      break;
    default:
      throw new Error(`Unknown DNS command: ${command}`);
  }

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
