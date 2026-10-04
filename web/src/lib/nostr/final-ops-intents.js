import { mintEntityId } from '../entity-id.js';

function required(value, label) {
  const text = String(value || '').trim();
  if (!text) throw new Error(`${label} is required for a signed intent`);
  return text;
}

function request(domain, op, coordinate, orgId, content, intentId = mintEntityId(), includeIntentId = false) {
  return { domain, op, coordinate, orgId, intentId,
    content: includeIntentId ? { intent_id: intentId, ...content } : { ...content } };
}

export function buildRequestIntent(payload, orgId, intentId) {
  const serviceId = required(payload.service_id, 'Service id');
  const { _meta, idempotency_key, ...content } = payload;
  return request('build', 'request', `build-request:${serviceId}`, orgId, content, intentId);
}

export function artifactBuildResultIntent(buildId, orgId, intentId) {
  const id = required(buildId, 'Build id');
  return request('artifact', 'register-build-result', `build-result:${id}`, orgId, { build_id: id }, intentId, true);
}

export function artifactSignatureVerifyIntent(artifactId, orgId, intentId) {
  const id = required(artifactId, 'Artifact id');
  return request('artifact', 'signature-verify', `artifact:${id}`, orgId, { artifact_id: id }, intentId, true);
}

export function securityScanIntent(target, { force = false } = {}, orgId, intentId = mintEntityId()) {
  if (!target || typeof target !== 'object') throw new Error('Security scan target is required');
  return request('security', 'scan-run', `security-scan:${intentId}`, orgId,
    { target, ...(force ? { force: true } : {}) }, intentId, true);
}

export function sbomIntent(op, content, orgId, intentId = mintEntityId()) {
  if (!['generate', 'import'].includes(op)) throw new Error(`Unsupported SBOM operation: ${op}`);
  const { idempotencyKey, intent_id, ...fields } = content;
  return request('sbom', op, `sbom-${op}:${intentId}`, orgId,
    { ...fields, idempotencyKey: intentId }, intentId, true);
}

export function relayPolicyIntent(policy, expectedProjection, replacementConfirmation, orgId, intentId) {
  return request('relay', 'policy-set', 'relay-settings:operator', orgId, {
    ...policy,
    ...(expectedProjection ? { expected_projection: expectedProjection } : {}),
    ...(replacementConfirmation ? { replacement_confirmation: replacementConfirmation } : {})
  }, intentId, true);
}

export function notificationChannelTestIntent(id, orgId, intentId) {
  const channelId = required(id, 'Notification channel id');
  return request('notification', 'channel-test', channelId, orgId, { id: channelId }, intentId, true);
}

export function environmentWorkerPolicyIntent(environmentId, policy, expectedUpdatedAt, orgId, intentId) {
  const id = required(environmentId, 'Environment id');
  return request('environment', 'worker-policy-apply', id, orgId,
    { environment_id: id, policy, expected_updated_at: required(expectedUpdatedAt, 'Canonical updated_at') }, intentId, true);
}

export function mlPinIntent(endpointId, environmentId, workerPubkey, expectedUpdatedAt, orgId, intentId) {
  const id = required(endpointId, 'Inference endpoint id');
  return request('ml', 'pin', `endpoint:${id}`, orgId, {
    workload_id: id, workload_kind: 'ml_inference',
    environment_id: required(environmentId, 'Environment id'),
    worker_pubkey: required(workerPubkey, 'Worker pubkey'),
    expected_updated_at: required(expectedUpdatedAt, 'Canonical updated_at')
  }, intentId, true);
}

export function mlOperationIntent(op, payload, orgId, intentId = mintEntityId()) {
  let coordinate;
  let content;
  switch (op) {
    case 'model-import': {
      const slug = required(payload.model || payload.model_slug, 'Model').replace(/^model:/, '');
      const model = `model:${slug}`;
      coordinate = model;
      content = { ...payload, model, source: payload.source || payload.source_kind,
        source_uri: payload.source_uri || payload.uri };
      delete content.model_slug;
      delete content.source_kind;
      delete content.uri;
      delete content.tags;
      delete content.idempotency_key;
      break;
    }
    case 'recipe-apply':
      coordinate = `recipe:${required(payload.name, 'Recipe name')}:${required(payload.version, 'Recipe version')}`;
      content = payload;
      break;
    case 'recipe-run':
      coordinate = `recipe-run:${required(payload.recipe_id, 'Recipe id')}`;
      content = payload;
      break;
    case 'inference-deploy':
      coordinate = `inference-deploy:${required(payload.endpoint_id, 'Endpoint id')}`;
      content = { ...payload };
      delete content.tags;
      delete content.idempotency_key;
      break;
    case 'inference-approval':
      coordinate = `inference-approval:${required(payload.intent_id, 'Inference intent id')}`;
      content = payload;
      break;
    case 'inference-rollback':
      coordinate = `inference-rollback:${required(payload.endpoint_id, 'Endpoint id')}`;
      content = payload;
      break;
    default: throw new Error(`Unsupported ML operation: ${op}`);
  }
  return request('ml', op, coordinate, orgId, content, intentId);
}

export function toolApprovalResponseIntent(payload, orgId, intentId) {
  const id = required(payload.intent_id, 'Tool approval intent id');
  return request('tool', 'approval-response', `tool-approval:${id}`, orgId,
    { intent_id: id, action: required(payload.action, 'Approval action'), reason: String(payload.reason || '') }, intentId);
}

export function adoptionScanIntent(payload, orgId, intentId) {
  return request('adoption', 'scan', `adoption:${required(orgId, 'Organization id')}`, orgId,
    { targets: payload.targets || [], limit: payload.limit ?? 20, offset: payload.offset ?? 0 }, intentId);
}
