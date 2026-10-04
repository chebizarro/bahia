import { mintEntityId, withEntityId } from '../entity-id.js';

function required(value, label) {
  const text = String(value || '').trim();
  if (!text) throw new Error(`${label} is required for a signed intent`);
  return text;
}

function intent(domain, op, coordinate, orgId, content, intentId) {
  return { domain, op, coordinate, orgId, content: { ...content, intent_id: intentId }, intentId };
}

export function artifactRegisterIntent(payload, orgId, intentId = mintEntityId()) {
  const { org_id, idempotency_key, ...fields } = withEntityId(payload);
  const id = required(fields.id, 'Artifact id');
  return intent('artifact', 'register', `artifact:${id}`, orgId, fields, intentId);
}

export function observedArtifactImportIntent(payload, orgId, intentId = mintEntityId()) {
  const { org_id, idempotency_key, ...fields } = payload;
  const serviceId = required(fields.service_id, 'Service id');
  const environmentId = required(fields.environment_id, 'Environment id');
  const digest = required(fields.image_digest, 'Image digest').toLowerCase();
  return intent('artifact', 'import-observed', `artifact-import:${serviceId}:${environmentId}:${digest}`,
    orgId, { ...fields, image_digest: digest }, intentId);
}

export function adoptionImportIntent(payload, orgId, intentId = mintEntityId()) {
  const { idempotency_key, ...fields } = payload;
  return intent('adoption', 'import', `adoption:${required(orgId, 'Organization id')}`, orgId,
    { ...fields, org_id: orgId }, intentId);
}

export function dnsDriftRemediateIntent(payload, orgId, intentId = mintEntityId()) {
  const zone = String(payload?.zone || payload?.zone_name || '').trim();
  return intent('dns', 'drift-remediate', `dns-remediate:${zone || 'all'}`, orgId,
    zone ? { zone } : {}, intentId);
}

export function deploymentPreviewIntent(payload, orgId, intentId = mintEntityId()) {
  const { org_id, idempotency_key, expected_updated_at, compact, ...fields } = payload;
  const serviceId = required(fields.service_id, 'Service id');
  const environmentId = required(fields.environment_id, 'Environment id');
  return intent('deployment', 'preview', `deployment-preview:${serviceId}:${environmentId}`, orgId,
    { ...fields, compact: true }, intentId);
}

export function deploymentRouteAttachIntent(payload, orgId, intentId = mintEntityId()) {
  const { org_id, idempotency_key, ...fields } = payload;
  const serviceId = required(fields.service_id, 'Service id');
  const environmentId = required(fields.environment_id, 'Environment id');
  return intent('deployment', 'route-attach', `deployment-route:${serviceId}:${environmentId}`,
    orgId, fields, intentId);
}

export function policyEvaluateIntent(payload, orgId, intentId = mintEntityId()) {
  const artifactId = required(payload?.artifact_id, 'Artifact id');
  const environmentId = required(payload?.environment_id, 'Environment id');
  return intent('policy', 'evaluate', `evaluation:${artifactId}:${environmentId}`, orgId,
    { artifact_id: artifactId, environment_id: environmentId }, intentId);
}
