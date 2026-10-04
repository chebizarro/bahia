import { mintEntityId } from '../entity-id.js';

function required(value, label) {
  const text = String(value || '').trim();
  if (!text) throw new Error(`${label} is required for a signed intent`);
  return text;
}

function revision(value) {
  const updatedAt = required(value, 'Canonical updated_at');
  if (Number.isNaN(Date.parse(updatedAt))) throw new Error('Canonical updated_at must be an RFC3339 timestamp');
  return updatedAt;
}

function request(domain, op, coordinate, orgId, content, intentId) {
  return { domain, op, coordinate, orgId: required(orgId, 'Organization id'), content,
    ...(intentId ? { intentId } : {}) };
}

// D69 fixtures include intent_id in content as well as the tag. Mint once so
// both copies are identical, including after an outbox retry.
export function deploymentIntentRequest(op, payload, orgId, intentId = mintEntityId()) {
  if (!['create', 'approve', 'reject', 'rollback'].includes(op)) throw new Error(`Unsupported deployment intent op: ${op}`);
  const coordinate = op === 'approve' || op === 'reject'
    ? required(payload.deployment_intent_id, 'Deployment intent id')
    : `${required(payload.service_id, 'Service id')}:${required(payload.environment_id, 'Environment id')}`;
  const { org_id, ...fields } = payload;
  const content = (op === 'approve' || op === 'reject')
    ? { deployment_intent_id: coordinate, expected_updated_at: revision(fields.expected_updated_at), intent_id: intentId }
    : { ...fields, intent_id: intentId };
  return request('deployment', op, coordinate, orgId, content, intentId);
}

export function runtimeIntentRequest(op, payload, orgId, intentId = mintEntityId()) {
  if (!['deploy', 'restart', 'stop'].includes(op)) throw new Error(`Unsupported runtime intent op: ${op}`);
  const serviceId = required(payload.service_id, 'Service id');
  const environmentId = required(payload.environment_id, 'Environment id');
  const content = { service_id: serviceId, environment_id: environmentId,
    ...(op === 'deploy' && payload.artifact_id ? { artifact_id: payload.artifact_id } : {}), intent_id: intentId };
  return request('runtime', op, `${serviceId}:${environmentId}`, orgId, content, intentId);
}

export function llmLifecycleIntentRequest(op, payload, orgId, intentId = mintEntityId()) {
  if (!['deploy', 'rollback', 'approve', 'reject'].includes(op)) throw new Error(`Unsupported LLM intent op: ${op}`);
  let coordinate;
  let content;
  if (op === 'approve' || op === 'reject') {
    coordinate = required(payload.deployment_intent_id, 'LLM deployment intent id');
    content = { deployment_intent_id: coordinate,
      ...(payload.expected_updated_at ? { expected_updated_at: revision(payload.expected_updated_at) } : {}), intent_id: intentId };
  } else {
    const routeId = required(payload.route_id, 'LLM route id');
    const environmentId = required(payload.environment_id, 'Environment id');
    coordinate = `${routeId}:${environmentId}`;
    content = { route_id: routeId, environment_id: environmentId,
      ...(op === 'deploy' ? { release_id: required(payload.release_id, 'LLM release id') } : {}), intent_id: intentId };
  }
  return request('llm', op, coordinate, orgId, content, intentId);
}

export function backupRestoreApprovalIntentRequest(restoreId, decision, message, orgId, intentId = mintEntityId()) {
  if (!['approve', 'reject'].includes(decision)) throw new Error(`Unsupported restore decision: ${decision}`);
  const coordinate = required(restoreId, 'Restore id');
  return request('backup', 'restore-approval', coordinate, orgId,
    { restore_id: coordinate, decision, message: String(message || ''), intent_id: intentId }, intentId);
}

export function dnsIntentRequest(op, payload, orgId, current = null) {
  const identities = {
    'zone-update': ['zone:', 'name'], 'zone-delete': ['zone:', 'name'],
    'endpoint-create': ['', 'coordinate'], 'endpoint-update': ['', 'coordinate'], 'endpoint-delete': ['', 'coordinate'],
    'backend-create': ['dnsbackend:', 'ref'], 'backend-update': ['dnsbackend:', 'ref'], 'backend-delete': ['dnsbackend:', 'ref'],
    'policy-update': ['dnspolicy:', 'id'], 'policy-delete': ['dnspolicy:', 'id']
  };
  if (identities[op]) {
    const [prefix, field] = identities[op];
    const identity = required(payload[field], `${field} for DNS ${op}`);
    const coordinate = `${prefix}${identity}`;
    const isDelete = op.endsWith('-delete');
    const content = isDelete ? { [field]: identity } : { ...payload, [field]: identity };
    delete content.expected_updated_at;
    if (op.endsWith('-update') || isDelete) {
      if (!current) throw new Error(`Current DNS record is required for ${op}`);
      content.expected_updated_at = revision(current.updated_at);
    }
    return request('dns', op, coordinate, orgId, content);
  }
  switch (op) {
    case 'zone-create': {
      const name = required(payload.name || payload.zone, 'Zone name');
      const { zone, idempotency_key, reconcile, ...fields } = payload;
      return request('dns', op, `zone:${name}`, orgId, { ...fields, name });
    }
    case 'policy-apply': {
      const id = required(payload.id || payload.policy_id, 'Policy id');
      const { policy_id, idempotency_key, ...fields } = payload;
      return request('dns', op, `dnspolicy:${id}`, orgId, { ...fields, id });
    }
    case 'record-set': {
      const id = required(payload.id, 'Record override id');
      const { idempotency_key, ...fields } = payload;
      return request('dns', op, `dns-override:${id}`, orgId, fields);
    }
    case 'override-retire': {
      const overrideId = required(payload.override_id, 'Override id');
      return request('dns', op, `dns-override:${overrideId}`, orgId,
        { override_id: overrideId, reason: required(payload.reason, 'Retirement reason') });
    }
    default: throw new Error(`Unsupported DNS intent op: ${op}`);
  }
}

export function mlIntentRequest(op, payload, orgId, current = null) {
  if (!['model-create', 'model-update', 'model-delete', 'version-create', 'version-update', 'version-delete',
    'endpoint-create', 'endpoint-update', 'endpoint-delete'].includes(op)) {
    throw new Error(`Unsupported ML intent op: ${op}`);
  }
  const id = required(payload.id || current?.id, 'ML entity id');
  const isUpdate = op.endsWith('-update');
  const isDelete = op.endsWith('-delete');
  if ((isUpdate || isDelete) && !current) throw new Error(`Current ML record is required for ${op}`);
  const coordinate = op.startsWith('model-') ? `model:${required(isDelete ? current.slug : payload.slug, 'Model slug')}`
    : op.startsWith('version-') ? `model-version:${id}` : `endpoint:${id}`;
  const content = isDelete ? { id } : { ...payload, id };
  delete content.expected_updated_at;
  if (isUpdate || isDelete) content.expected_updated_at = revision(current.updated_at);
  return request('ml', op, coordinate, orgId, content);
}

const WORKER_STATES = {
  cordon: 'cordoned', uncordon: 'active', drain: 'draining', undrain: 'active',
  'maintenance-enter': 'maintenance', 'maintenance-exit': 'active'
};
export function workerIntentRequest(op, worker, orgId, { reason = '', labels, cleanupMode } = {}) {
  if (![...Object.keys(WORKER_STATES), 'labels-update', 'cleanup'].includes(op)) {
    throw new Error(`Unsupported worker intent op: ${op}`);
  }
  const pubkey = required(worker?.pubkey, 'Worker pubkey');
  const state = WORKER_STATES[op] || worker.scheduling_state || 'active';
  const content = { worker_pubkey: pubkey, scheduling_state: state,
    labels: labels ?? worker.labels ?? {}, ...(op !== 'labels-update' ? { reason: String(reason || '') } : {}),
    ...(op === 'cleanup' ? { cleanup_mode: cleanupMode || 'reclaimable_only' } : {}) };
  return request('worker', op, `worker:${pubkey}`, orgId, content);
}
