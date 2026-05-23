import { KINDS, getTagValue, parseJsonContent } from './client.js';
import { awaitResult, publishRequest, subscribeStatus } from './controlplane-requests.js';

export const DNS_COMMANDS = {
  ZONE_CREATE: 'zone_create',
  POLICY_APPLY: 'policy_apply',
  RECORD_OVERRIDE: 'record_override',
  DRIFT_REMEDIATE: 'drift_remediate'
};

export const DNS_OPERATION_STATUS_KIND = KINDS.BAHIA_DNS_OPERATION_STATUS;

export const DNS_COMMAND_KINDS = {
  [DNS_COMMANDS.ZONE_CREATE]: KINDS.BAHIA_REQUEST_DNS_ZONE_CREATE,
  [DNS_COMMANDS.POLICY_APPLY]: KINDS.BAHIA_REQUEST_DNS_POLICY_APPLY,
  [DNS_COMMANDS.RECORD_OVERRIDE]: KINDS.BAHIA_REQUEST_DNS_RECORD_OVERRIDE,
  [DNS_COMMANDS.DRIFT_REMEDIATE]: KINDS.BAHIA_REQUEST_DNS_DRIFT_REMEDIATE
};

export const DNS_RESULT_KINDS = {
  [DNS_COMMANDS.ZONE_CREATE]: KINDS.BAHIA_DNS_ZONE_CREATE_RESULT,
  [DNS_COMMANDS.POLICY_APPLY]: KINDS.BAHIA_DNS_POLICY_APPLY_RESULT,
  [DNS_COMMANDS.RECORD_OVERRIDE]: KINDS.BAHIA_DNS_RECORD_OVERRIDE_RESULT,
  [DNS_COMMANDS.DRIFT_REMEDIATE]: KINDS.BAHIA_DNS_DRIFT_REMEDIATE_RESULT
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
  const kind = DNS_COMMAND_KINDS[command];
  if (!kind) throw new Error(`Unknown DNS command: ${command}`);
  return {
    kind,
    tags: [...defaultTagsForCommand(command, payload), ...normalizeTags(tags)],
    content: payload
  };
}

function taggedRequestEventId(event) {
  const tag = (event?.tags || []).find((candidate) =>
    Array.isArray(candidate) && candidate[0] === 'e' && candidate[1] && (!candidate[3] || candidate[3] === 'reply')
  );
  return tag?.[1] || '';
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

export async function startDNSCommand({ command, payload = {}, tags = [], signal, onStatus, onClosed, servicePubkey } = {}) {
  const request = buildDNSCommandRequest({ command, payload, tags });
  const published = await publishRequest(request);
  const requestEventId = published.requestEventId;
  const resultKind = DNS_RESULT_KINDS[command];

  let unsubscribeStatus = subscribeStatus({
    requestEventId,
    statusKinds: [DNS_OPERATION_STATUS_KIND],
    servicePubkey,
    onStatus: (event, relay) => {
      if (typeof onStatus === 'function') onStatus(parseDNSOperationEvent(event), event, relay);
    },
    onClosed: (reason, relay) => {
      if (typeof onClosed === 'function') onClosed(new Error(`Nostr DNS status subscription closed${relay ? ` on ${relay}` : ''}: ${reason || 'closed'}`), reason, relay);
    }
  });

  const result = awaitResult({
    requestEventId,
    resultKinds: [resultKind],
    signal,
    servicePubkey
  }).then((event) => parseDNSOperationEvent(event)).finally(() => {
    if (unsubscribeStatus) unsubscribeStatus();
    unsubscribeStatus = null;
  });

  return {
    command,
    requestEventId,
    resultKind,
    request,
    event: published.event,
    ok: published.ok,
    acceptedRelays: published.acceptedRelays,
    rejectedRelays: published.rejectedRelays,
    result,
    unsubscribeStatus: () => {
      if (unsubscribeStatus) unsubscribeStatus();
      unsubscribeStatus = null;
    }
  };
}
