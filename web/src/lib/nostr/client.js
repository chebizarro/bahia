// Nostr client for Soul Factory web UI
// Uses nostr-tools for WebSocket relay connections

import { verifyEvent } from 'nostr-tools';
import { createNostrPoolClient as createPoolClient, NostrIncompleteEOSEError } from './pool.js';

export { NostrIncompleteEOSEError };

const HEX_64 = /^[0-9a-f]{64}$/;
const HEX_128 = /^[0-9a-f]{128}$/;
const MAX_FUTURE_SKEW_SECONDS = 10 * 60;
const MAX_PAST_SKEW_SECONDS = 365 * 24 * 60 * 60;

function currentUnixTime() {
  return Math.floor(Date.now() / 1000);
}

function isStringTagValue(value) {
  return typeof value === 'string';
}

async function sha256Hex(input) {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle || typeof subtle.digest !== 'function') {
    throw new Error('crypto.subtle is unavailable for Nostr event hash validation');
  }
  const digest = await subtle.digest('SHA-256', new TextEncoder().encode(input));
  return Array.from(new Uint8Array(digest))
    .map((byte) => byte.toString(16).padStart(2, '0'))
    .join('');
}

function stableJsonValue(value) {
  if (Array.isArray(value)) return value.map(stableJsonValue);
  if (value && typeof value === 'object') {
    return Object.fromEntries(
      Object.keys(value)
        .sort()
        .map((key) => [key, stableJsonValue(value[key])])
    );
  }
  return value;
}

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

export async function validateInboundNostrEvent(event, { now = currentUnixTime() } = {}) {
  if (!event || typeof event !== 'object' || Array.isArray(event)) {
    throw new Error('event must be an object');
  }
  if (typeof event.id !== 'string' || !HEX_64.test(event.id)) {
    throw new Error('event id must be 64 lowercase hex characters');
  }
  if (typeof event.pubkey !== 'string' || !HEX_64.test(event.pubkey)) {
    throw new Error('event pubkey must be 64 lowercase hex characters');
  }
  if (typeof event.sig !== 'string' || !HEX_128.test(event.sig)) {
    throw new Error('event signature must be 128 lowercase hex characters');
  }
  if (!Number.isInteger(event.kind) || event.kind < 0) {
    throw new Error('event kind must be an integer');
  }
  if (!Number.isInteger(event.created_at)) {
    throw new Error('event created_at must be an integer');
  }
  if (event.created_at > now + MAX_FUTURE_SKEW_SECONDS) {
    throw new Error('event created_at is too far in the future');
  }
  if (event.created_at < now - MAX_PAST_SKEW_SECONDS) {
    throw new Error('event created_at is too far in the past');
  }
  if (typeof event.content !== 'string') {
    throw new Error('event content must be a string');
  }
  if (!Array.isArray(event.tags)) {
    throw new Error('event tags must be an array');
  }
  for (const tag of event.tags) {
    if (!Array.isArray(tag) || tag.some((value) => !isStringTagValue(value))) {
      throw new Error('event tags must be arrays of strings');
    }
  }

  const serialized = JSON.stringify([
    0,
    event.pubkey,
    event.created_at,
    event.kind,
    event.tags,
    event.content
  ]);
  const computedId = await sha256Hex(serialized);
  if (computedId !== event.id) {
    throw new Error('event id does not match NIP-01 hash');
  }

  if (globalThis.__BAHIA_E2E_TRUST_MOCK_RELAY_EVENTS === true && event.sig === '0'.repeat(128)) {
    return true;
  }

  if (!verifyEvent(event)) {
    throw new Error('event signature is invalid');
  }

  return true;
}

// Soul Factory event kinds
export const KINDS = {
  SOUL_TEMPLATE: 31950,
  AGENT_SOUL: 31951,
  SOUL_DRAFT: 31952,
  PROVISIONING_REQUEST: 5950,
  PROVISIONING_STATUS: 6950,
  PROVISIONING_RESULT: 7950,
  SOUL_ACTION: 1950,
  SOUL_ACTION_LEGACY_RESULT: 1951,
  RUNTIME_CAPABILITY: 30317,
  RUNTIME_CONTROL_REQUEST: 38384,
  RUNTIME_CONTROL_RESULT: 38386,
  REPOSITORY: 30617,

  // Bahia canonical control-plane/read-model kinds
  BAHIA_REQUEST_DNS_ZONE_CREATE: 5941,
  BAHIA_REQUEST_DNS_POLICY_APPLY: 5942,
  BAHIA_REQUEST_DNS_RECORD_OVERRIDE: 5943,
  BAHIA_REQUEST_DNS_DRIFT_REMEDIATE: 5944,
  BAHIA_DNS_OPERATION_STATUS: 6941,
  BAHIA_DNS_ZONE_CREATE_RESULT: 7941,
  BAHIA_DNS_POLICY_APPLY_RESULT: 7942,
  BAHIA_DNS_RECORD_OVERRIDE_RESULT: 7943,
  BAHIA_DNS_DRIFT_REMEDIATE_RESULT: 7944,
  BAHIA_REQUEST_DEPLOY: 5961,
  BAHIA_REQUEST_ROLLBACK: 5962,
  BAHIA_REQUEST_SERVICE_ACTION: 5963,
  BAHIA_REQUEST_SERVICE_CREATE: 5964,
  BAHIA_REQUEST_ENVIRONMENT_CREATE: 5965,
  BAHIA_REQUEST_DEPLOYMENT_APPROVAL: 5966,
  BAHIA_REQUEST_OBSERVATION_SUBMIT: 5967,
  BAHIA_REQUEST_DRIFT_REMEDIATE: 5968,
  BAHIA_REQUEST_LLM_ROUTE_CREATE: 5971,
  BAHIA_REQUEST_LLM_RELEASE_REGISTER: 5972,
  BAHIA_REQUEST_LLM_DEPLOY: 5973,
  BAHIA_REQUEST_LLM_DEPLOYMENT_APPROVAL: 5974,
  BAHIA_REQUEST_LLM_ROLLBACK: 5975,
  BAHIA_REQUEST_SERVICE_UPDATE: 5981,
  BAHIA_REQUEST_SERVICE_DELETE: 5982,
  BAHIA_REQUEST_ENVIRONMENT_UPDATE: 5983,
  BAHIA_REQUEST_ENVIRONMENT_DELETE: 5984,
  BAHIA_REQUEST_ARTIFACT_REGISTER: 5985,
  BAHIA_REQUEST_POLICY_CREATE: 5986,
  BAHIA_REQUEST_POLICY_UPDATE: 5987,
  BAHIA_REQUEST_POLICY_DELETE: 5988,
  BAHIA_REQUEST_POLICY_EVALUATE: 5989,
  BAHIA_REQUEST_PACKAGE_PROMOTE: 5994,
  BAHIA_REQUEST_PACKAGE_YANK: 5995,
  BAHIA_REQUEST_WORKER_CORDON: 5997,
  BAHIA_REQUEST_WORKER_UNCORDON: 5998,
  BAHIA_REQUEST_WORKER_DRAIN: 5999,
  BAHIA_REQUEST_WORKER_UNDRAIN: 6000,
  BAHIA_REQUEST_WORKER_MAINTENANCE_ENTER: 6001,
  BAHIA_REQUEST_WORKER_MAINTENANCE_EXIT: 6002,
  BAHIA_REQUEST_WORKER_LABELS_UPDATE: 6003,
  BAHIA_REQUEST_WORKER_POLICY_APPLY: 6004,
  BAHIA_REQUEST_WORKLOAD_PIN: 6005,
  BAHIA_REQUEST_WORKER_CLEANUP: 6006,
  BAHIA_PACKAGE_STATUS: 6991,
  BAHIA_WORKER_STATUS: 6997,
  BAHIA_PACKAGE_RESULT: 7991,
  BAHIA_PACKAGE_DRIFT_EVENT: 7992,
  BAHIA_WORKER_RESULT: 7997,
  BAHIA_DEPLOYMENT_STATUS: 6961,
  BAHIA_SERVICE_STATUS: 6962,
  BAHIA_LLM_DEPLOYMENT_STATUS: 6973,
  BAHIA_DEPLOYMENT_RESULT: 7961,
  BAHIA_ACTION_RESULT: 7962,
  BAHIA_SERVICE_CREATE_RESULT: 7963,
  BAHIA_ENVIRONMENT_CREATE_RESULT: 7964,
  BAHIA_OBSERVATION_RESULT: 7965,
  BAHIA_REMEDIATION_RESULT: 7966,
  BAHIA_LLM_ROUTE_CREATE_RESULT: 7971,
  BAHIA_LLM_RELEASE_REGISTER_RESULT: 7972,
  BAHIA_LLM_DEPLOYMENT_RESULT: 7973,
  BAHIA_SERVICE_STATE: 31961,
  BAHIA_SERVICE_REGISTRY: 31962,
  BAHIA_ENVIRONMENT_REGISTRY: 31963,
  BAHIA_LLM_ROUTE_REGISTRY: 31964,
  BAHIA_LLM_ROUTE_STATE: 31965,
  BAHIA_ARTIFACT_REGISTRY: 31966,
  BAHIA_DEPLOYMENT_INTENT_REGISTRY: 31967,
  BAHIA_DEPLOYMENT_RUN_REGISTRY: 31968,
  BAHIA_BUILD_REGISTRY: 31969,
  BAHIA_POLICY_REGISTRY: 31970,
  BAHIA_PACKAGE_REPOSITORY_REGISTRY: 31971,
  BAHIA_PACKAGE_ARTIFACT_REGISTRY: 31972,
  BAHIA_PACKAGE_PROMOTION_REGISTRY: 31973,
  BAHIA_SYSTEM_DISCOVERY: 31974,
  BAHIA_DNS_ZONE_STATE: 31975,
  BAHIA_DNS_ENDPOINT_STATE: 31976,
  BAHIA_DNS_POLICY_STATE: 31977,
  BAHIA_DNS_BACKEND_STATE: 31978,
  BAHIA_WORKER_STATE: 32000,
  BAHIA_WORKER_ASSIGNMENT_STATE: 32001,
  BAHIA_WORKER_DRAIN_STATUS: 32002,
  BAHIA_WORKER_ELIGIBILITY_PREVIEW: 32003,
  BAHIA_LEGACY_WORKER_STATE: 31974,
  BAHIA_LEGACY_WORKER_ASSIGNMENT_STATE: 31991,
  BAHIA_LEGACY_WORKER_DRAIN_STATUS: 31992,
  BAHIA_LEGACY_WORKER_ELIGIBILITY_PREVIEW: 31993,
  NIP51_RELAY_SET: 30002,
  LOOM_WORKER_AD: 10100,

  // Inference read-model kinds (31980-31989)
  BAHIA_ML_MODEL_REGISTRY: 31980,
  BAHIA_ML_MODEL_VERSION_REGISTRY: 31981,
  BAHIA_ML_DATASET_REGISTRY: 31982,
  BAHIA_ML_RECIPE_REGISTRY: 31983,
  BAHIA_ML_RECIPE_RUN_STATE: 31984,
  BAHIA_ML_ENDPOINT_REGISTRY: 31985,
  BAHIA_ML_ENDPOINT_STATE: 31986,
  BAHIA_ML_EVALUATION_STATE: 31987,
  BAHIA_ML_ARTIFACT_PROVENANCE: 31988,
  BAHIA_ML_RUNTIME_CAPABILITY: 31989,

  // Backup control-plane command, result, status, attestation, and read-model kinds
  BAHIA_REQUEST_BACKUP_RUN: 38400,
  BAHIA_REQUEST_BACKUP_VERIFICATION: 38401,
  BAHIA_REQUEST_BACKUP_RESTORE: 38402,
  BAHIA_REQUEST_BACKUP_RESTORE_APPROVAL: 38403,
  BAHIA_REQUEST_BACKUP_RETENTION: 38404,
  BAHIA_REQUEST_BACKUP_REPOSITORY_REGISTER: 38405,
  BAHIA_REQUEST_BACKUP_POLICY_APPLY: 38406,
  BAHIA_REQUEST_BACKUP_RECIPE_APPLY: 38407,
  BAHIA_REQUEST_BACKUP_DEFINITION_APPLY: 38408,
  BAHIA_REQUEST_BACKUP_REPOSITORY_PROBE: 38409,
  BAHIA_BACKUP_RUN_RESULT: 38410,
  BAHIA_BACKUP_VERIFICATION_RESULT: 38411,
  BAHIA_BACKUP_RESTORE_RESULT: 38412,
  BAHIA_BACKUP_RESTORE_APPROVAL_RESULT: 38413,
  BAHIA_BACKUP_RETENTION_RESULT: 38414,
  BAHIA_BACKUP_REPOSITORY_REGISTER_RESULT: 38415,
  BAHIA_BACKUP_POLICY_APPLY_RESULT: 38416,
  BAHIA_BACKUP_RECIPE_APPLY_RESULT: 38417,
  BAHIA_BACKUP_DEFINITION_APPLY_RESULT: 38418,
  BAHIA_BACKUP_REPOSITORY_PROBE_RESULT: 38419,
  BAHIA_BACKUP_RUN_STATUS: 6981,
  BAHIA_BACKUP_RESTORE_STATUS: 6982,
  BAHIA_BACKUP_VERIFICATION_STATUS: 6983,
  BAHIA_BACKUP_OBSERVATION: 6984,
  BAHIA_BACKUP_RUN_ATTESTATION: 31310,
  BAHIA_BACKUP_VERIFICATION_ATTESTATION: 31311,
  BAHIA_BACKUP_DEFINITION_REGISTRY: 31991,
  BAHIA_BACKUP_POLICY_REGISTRY: 31992,
  BAHIA_BACKUP_REPOSITORY_REGISTRY: 31993,
  BAHIA_BACKUP_RETENTION_REGISTRY: 31994,
  BAHIA_BACKUP_RECIPE_REGISTRY: 31995,
  BAHIA_BACKUP_RUN_STATE: 31996,
  BAHIA_BACKUP_VERIFICATION_STATE: 31997,
  BAHIA_BACKUP_RESTORE_STATE: 31998,
  BAHIA_BACKUP_RUNTIME_OBSERVATION_STATE: 31999,

  // Operator assistant event kinds
  ASSISTANT_SESSION: 31990,
  ASSISTANT_PROMPT_REQUEST: 38420,
  ASSISTANT_APPROVAL: 38421,
  ASSISTANT_STATUS: 38422,
  ASSISTANT_RESULT: 38423,
  
  // SBOM Attestation/Index kinds (NIP-51 style parameterized lists)
  SBOM_ATTESTATION: 30078,
  SBOM_INDEX: 30079
};

export const SOUL_FACTORY_RUNTIME_CONTROL_SCHEMA = 'soulfactory-runtime-control/v1';
export const SOUL_FACTORY_RUNTIME_CAPABILITY_SCHEMA = 'soulfactory-runtime-capability/v1';

export const SOUL_RUNTIME_TARGETS = {
  OPENCLAW: 'openclaw',
  METIQ: 'metiq'
};

export const SOUL_LIFECYCLE_ACTIONS = {
  SUSPEND: 'suspend',
  RESUME: 'resume',
  REVOKE: 'revoke',
  REGENERATE: 'regenerate',
  REDEPLOY: 'redeploy',
  UPDATE: 'update'
};

export const SOUL_RUNTIME_METHODS = {
  PROVISION: 'soulfactory.provision',
  UPDATE: 'soulfactory.update',
  SUSPEND: 'soulfactory.suspend',
  RESUME: 'soulfactory.resume',
  REDEPLOY: 'soulfactory.redeploy',
  REVOKE: 'soulfactory.revoke'
};

export function isLifecycleResultKind(kind) {
  return kind === KINDS.PROVISIONING_RESULT || kind === KINDS.SOUL_ACTION_LEGACY_RESULT;
}

export function canonicalLifecycleResultKind(kind) {
  return kind === KINDS.SOUL_ACTION_LEGACY_RESULT ? KINDS.PROVISIONING_RESULT : kind;
}

export const BAHIA_KINDS = {
  DNS_ZONE_CREATE_REQUEST: KINDS.BAHIA_REQUEST_DNS_ZONE_CREATE,
  DNS_POLICY_APPLY_REQUEST: KINDS.BAHIA_REQUEST_DNS_POLICY_APPLY,
  DNS_RECORD_OVERRIDE_REQUEST: KINDS.BAHIA_REQUEST_DNS_RECORD_OVERRIDE,
  DNS_DRIFT_REMEDIATE_REQUEST: KINDS.BAHIA_REQUEST_DNS_DRIFT_REMEDIATE,
  DNS_OPERATION_STATUS: KINDS.BAHIA_DNS_OPERATION_STATUS,
  DNS_ZONE_CREATE_RESULT: KINDS.BAHIA_DNS_ZONE_CREATE_RESULT,
  DNS_POLICY_APPLY_RESULT: KINDS.BAHIA_DNS_POLICY_APPLY_RESULT,
  DNS_RECORD_OVERRIDE_RESULT: KINDS.BAHIA_DNS_RECORD_OVERRIDE_RESULT,
  DNS_DRIFT_REMEDIATE_RESULT: KINDS.BAHIA_DNS_DRIFT_REMEDIATE_RESULT,
  DEPLOY_REQUEST: KINDS.BAHIA_REQUEST_DEPLOY,
  ROLLBACK_REQUEST: KINDS.BAHIA_REQUEST_ROLLBACK,
  SERVICE_ACTION: KINDS.BAHIA_REQUEST_SERVICE_ACTION,
  SERVICE_CREATE: KINDS.BAHIA_REQUEST_SERVICE_CREATE,
  ENVIRONMENT_CREATE: KINDS.BAHIA_REQUEST_ENVIRONMENT_CREATE,
  DEPLOYMENT_APPROVAL: KINDS.BAHIA_REQUEST_DEPLOYMENT_APPROVAL,
  OBSERVATION_SUBMIT: KINDS.BAHIA_REQUEST_OBSERVATION_SUBMIT,
  DRIFT_REMEDIATE: KINDS.BAHIA_REQUEST_DRIFT_REMEDIATE,
  LLM_ROUTE_CREATE: KINDS.BAHIA_REQUEST_LLM_ROUTE_CREATE,
  LLM_RELEASE_REGISTER: KINDS.BAHIA_REQUEST_LLM_RELEASE_REGISTER,
  LLM_DEPLOY_REQUEST: KINDS.BAHIA_REQUEST_LLM_DEPLOY,
  LLM_DEPLOYMENT_APPROVAL: KINDS.BAHIA_REQUEST_LLM_DEPLOYMENT_APPROVAL,
  LLM_ROLLBACK_REQUEST: KINDS.BAHIA_REQUEST_LLM_ROLLBACK,
  SERVICE_UPDATE: KINDS.BAHIA_REQUEST_SERVICE_UPDATE,
  SERVICE_DELETE: KINDS.BAHIA_REQUEST_SERVICE_DELETE,
  ENVIRONMENT_UPDATE: KINDS.BAHIA_REQUEST_ENVIRONMENT_UPDATE,
  ENVIRONMENT_DELETE: KINDS.BAHIA_REQUEST_ENVIRONMENT_DELETE,
  ARTIFACT_REGISTER: KINDS.BAHIA_REQUEST_ARTIFACT_REGISTER,
  POLICY_CREATE: KINDS.BAHIA_REQUEST_POLICY_CREATE,
  POLICY_UPDATE: KINDS.BAHIA_REQUEST_POLICY_UPDATE,
  POLICY_DELETE: KINDS.BAHIA_REQUEST_POLICY_DELETE,
  POLICY_EVALUATE: KINDS.BAHIA_REQUEST_POLICY_EVALUATE,
  PACKAGE_PROMOTE: KINDS.BAHIA_REQUEST_PACKAGE_PROMOTE,
  PACKAGE_YANK: KINDS.BAHIA_REQUEST_PACKAGE_YANK,
  PACKAGE_STATUS: KINDS.BAHIA_PACKAGE_STATUS,
  PACKAGE_RESULT: KINDS.BAHIA_PACKAGE_RESULT,
  PACKAGE_DRIFT_EVENT: KINDS.BAHIA_PACKAGE_DRIFT_EVENT,
  WORKER_STATUS: KINDS.BAHIA_WORKER_STATUS,
  WORKER_RESULT: KINDS.BAHIA_WORKER_RESULT,
  DEPLOYMENT_STATUS: KINDS.BAHIA_DEPLOYMENT_STATUS,
  SERVICE_STATUS: KINDS.BAHIA_SERVICE_STATUS,
  LLM_DEPLOYMENT_STATUS: KINDS.BAHIA_LLM_DEPLOYMENT_STATUS,
  DEPLOYMENT_RESULT: KINDS.BAHIA_DEPLOYMENT_RESULT,
  ACTION_RESULT: KINDS.BAHIA_ACTION_RESULT,
  SERVICE_CREATE_RESULT: KINDS.BAHIA_SERVICE_CREATE_RESULT,
  ENVIRONMENT_CREATE_RESULT: KINDS.BAHIA_ENVIRONMENT_CREATE_RESULT,
  OBSERVATION_RESULT: KINDS.BAHIA_OBSERVATION_RESULT,
  REMEDIATION_RESULT: KINDS.BAHIA_REMEDIATION_RESULT,
  LLM_ROUTE_CREATE_RESULT: KINDS.BAHIA_LLM_ROUTE_CREATE_RESULT,
  LLM_RELEASE_REGISTER_RESULT: KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT,
  LLM_DEPLOYMENT_RESULT: KINDS.BAHIA_LLM_DEPLOYMENT_RESULT,
  SERVICE_STATE: KINDS.BAHIA_SERVICE_STATE,
  SERVICE_REGISTRY: KINDS.BAHIA_SERVICE_REGISTRY,
  ENVIRONMENT_REGISTRY: KINDS.BAHIA_ENVIRONMENT_REGISTRY,
  LLM_ROUTE_REGISTRY: KINDS.BAHIA_LLM_ROUTE_REGISTRY,
  LLM_ROUTE_STATE: KINDS.BAHIA_LLM_ROUTE_STATE,
  ARTIFACT_REGISTRY: KINDS.BAHIA_ARTIFACT_REGISTRY,
  DEPLOYMENT_INTENT_REGISTRY: KINDS.BAHIA_DEPLOYMENT_INTENT_REGISTRY,
  DEPLOYMENT_RUN_REGISTRY: KINDS.BAHIA_DEPLOYMENT_RUN_REGISTRY,
  BUILD_REGISTRY: KINDS.BAHIA_BUILD_REGISTRY,
  POLICY_REGISTRY: KINDS.BAHIA_POLICY_REGISTRY,
  PACKAGE_REPOSITORY_REGISTRY: KINDS.BAHIA_PACKAGE_REPOSITORY_REGISTRY,
  PACKAGE_ARTIFACT_REGISTRY: KINDS.BAHIA_PACKAGE_ARTIFACT_REGISTRY,
  PACKAGE_PROMOTION_REGISTRY: KINDS.BAHIA_PACKAGE_PROMOTION_REGISTRY,
  SYSTEM_DISCOVERY: KINDS.BAHIA_SYSTEM_DISCOVERY,
  DNS_ZONE_STATE: KINDS.BAHIA_DNS_ZONE_STATE,
  DNS_ENDPOINT_STATE: KINDS.BAHIA_DNS_ENDPOINT_STATE,
  DNS_POLICY_STATE: KINDS.BAHIA_DNS_POLICY_STATE,
  DNS_BACKEND_STATE: KINDS.BAHIA_DNS_BACKEND_STATE,
  WORKER_STATE: KINDS.BAHIA_WORKER_STATE,
  WORKER_ASSIGNMENT_STATE: KINDS.BAHIA_WORKER_ASSIGNMENT_STATE,
  WORKER_DRAIN_STATUS: KINDS.BAHIA_WORKER_DRAIN_STATUS,
  WORKER_ELIGIBILITY_PREVIEW: KINDS.BAHIA_WORKER_ELIGIBILITY_PREVIEW,
  BACKUP_RUN_REQUEST: KINDS.BAHIA_REQUEST_BACKUP_RUN,
  BACKUP_VERIFICATION_REQUEST: KINDS.BAHIA_REQUEST_BACKUP_VERIFICATION,
  BACKUP_RESTORE_REQUEST: KINDS.BAHIA_REQUEST_BACKUP_RESTORE,
  BACKUP_RESTORE_APPROVAL: KINDS.BAHIA_REQUEST_BACKUP_RESTORE_APPROVAL,
  BACKUP_RETENTION_REQUEST: KINDS.BAHIA_REQUEST_BACKUP_RETENTION,
  BACKUP_REPOSITORY_REGISTER_REQUEST: KINDS.BAHIA_REQUEST_BACKUP_REPOSITORY_REGISTER,
  BACKUP_POLICY_APPLY_REQUEST: KINDS.BAHIA_REQUEST_BACKUP_POLICY_APPLY,
  BACKUP_RECIPE_APPLY_REQUEST: KINDS.BAHIA_REQUEST_BACKUP_RECIPE_APPLY,
  BACKUP_DEFINITION_APPLY_REQUEST: KINDS.BAHIA_REQUEST_BACKUP_DEFINITION_APPLY,
  BACKUP_REPOSITORY_PROBE_REQUEST: KINDS.BAHIA_REQUEST_BACKUP_REPOSITORY_PROBE,
  BACKUP_RUN_RESULT: KINDS.BAHIA_BACKUP_RUN_RESULT,
  BACKUP_VERIFICATION_RESULT: KINDS.BAHIA_BACKUP_VERIFICATION_RESULT,
  BACKUP_RESTORE_RESULT: KINDS.BAHIA_BACKUP_RESTORE_RESULT,
  BACKUP_RESTORE_APPROVAL_RESULT: KINDS.BAHIA_BACKUP_RESTORE_APPROVAL_RESULT,
  BACKUP_RETENTION_RESULT: KINDS.BAHIA_BACKUP_RETENTION_RESULT,
  BACKUP_REPOSITORY_REGISTER_RESULT: KINDS.BAHIA_BACKUP_REPOSITORY_REGISTER_RESULT,
  BACKUP_POLICY_APPLY_RESULT: KINDS.BAHIA_BACKUP_POLICY_APPLY_RESULT,
  BACKUP_RECIPE_APPLY_RESULT: KINDS.BAHIA_BACKUP_RECIPE_APPLY_RESULT,
  BACKUP_DEFINITION_APPLY_RESULT: KINDS.BAHIA_BACKUP_DEFINITION_APPLY_RESULT,
  BACKUP_REPOSITORY_PROBE_RESULT: KINDS.BAHIA_BACKUP_REPOSITORY_PROBE_RESULT,
  BACKUP_DEFINITION_REGISTRY: KINDS.BAHIA_BACKUP_DEFINITION_REGISTRY,
  BACKUP_POLICY_REGISTRY: KINDS.BAHIA_BACKUP_POLICY_REGISTRY,
  BACKUP_REPOSITORY_REGISTRY: KINDS.BAHIA_BACKUP_REPOSITORY_REGISTRY,
  BACKUP_RETENTION_REGISTRY: KINDS.BAHIA_BACKUP_RETENTION_REGISTRY,
  BACKUP_RECIPE_REGISTRY: KINDS.BAHIA_BACKUP_RECIPE_REGISTRY,
  BACKUP_RUN_STATE: KINDS.BAHIA_BACKUP_RUN_STATE,
  BACKUP_VERIFICATION_STATE: KINDS.BAHIA_BACKUP_VERIFICATION_STATE,
  BACKUP_RESTORE_STATE: KINDS.BAHIA_BACKUP_RESTORE_STATE,
  BACKUP_RUNTIME_OBSERVATION_STATE: KINDS.BAHIA_BACKUP_RUNTIME_OBSERVATION_STATE,
  RELAY_SET: KINDS.NIP51_RELAY_SET,
  WORKER_ADVERTISEMENT: KINDS.LOOM_WORKER_AD,
  AUDIT_MIN: 31000,
  AUDIT_MAX: 31099
};

export const BAHIA_READ_MODEL_KINDS = [
  KINDS.BAHIA_SERVICE_REGISTRY,
  KINDS.BAHIA_ENVIRONMENT_REGISTRY,
  KINDS.BAHIA_SERVICE_STATE,
  KINDS.BAHIA_LLM_ROUTE_REGISTRY,
  KINDS.BAHIA_LLM_ROUTE_STATE,
  KINDS.BAHIA_ARTIFACT_REGISTRY,
  KINDS.BAHIA_DEPLOYMENT_INTENT_REGISTRY,
  KINDS.BAHIA_DEPLOYMENT_RUN_REGISTRY,
  KINDS.BAHIA_BUILD_REGISTRY,
  KINDS.BAHIA_POLICY_REGISTRY,
  KINDS.BAHIA_PACKAGE_REPOSITORY_REGISTRY,
  KINDS.BAHIA_PACKAGE_ARTIFACT_REGISTRY,
  KINDS.BAHIA_PACKAGE_PROMOTION_REGISTRY,
  KINDS.BAHIA_DNS_ZONE_STATE,
  KINDS.BAHIA_DNS_ENDPOINT_STATE,
  KINDS.BAHIA_DNS_POLICY_STATE,
  KINDS.BAHIA_DNS_BACKEND_STATE,
  KINDS.BAHIA_WORKER_STATE,
  KINDS.BAHIA_WORKER_ASSIGNMENT_STATE,
  KINDS.BAHIA_WORKER_DRAIN_STATUS,
  KINDS.BAHIA_WORKER_ELIGIBILITY_PREVIEW,
  KINDS.BAHIA_LEGACY_WORKER_STATE,
  KINDS.BAHIA_LEGACY_WORKER_ASSIGNMENT_STATE,
  KINDS.BAHIA_LEGACY_WORKER_DRAIN_STATUS,
  KINDS.BAHIA_LEGACY_WORKER_ELIGIBILITY_PREVIEW,
  KINDS.LOOM_WORKER_AD,
  // Inference
  KINDS.BAHIA_ML_MODEL_REGISTRY,
  KINDS.BAHIA_ML_MODEL_VERSION_REGISTRY,
  KINDS.BAHIA_ML_ENDPOINT_REGISTRY,
  KINDS.BAHIA_ML_ENDPOINT_STATE,
  KINDS.BAHIA_ML_RUNTIME_CAPABILITY,
  // Backup
  KINDS.BAHIA_BACKUP_DEFINITION_REGISTRY,
  KINDS.BAHIA_BACKUP_POLICY_REGISTRY,
  KINDS.BAHIA_BACKUP_REPOSITORY_REGISTRY,
  KINDS.BAHIA_BACKUP_RETENTION_REGISTRY,
  KINDS.BAHIA_BACKUP_RECIPE_REGISTRY,
  KINDS.BAHIA_BACKUP_RUN_STATE,
  KINDS.BAHIA_BACKUP_VERIFICATION_STATE,
  KINDS.BAHIA_BACKUP_RESTORE_STATE,
  KINDS.BAHIA_BACKUP_RUNTIME_OBSERVATION_STATE
];

export const BAHIA_STATUS_KINDS = [
  KINDS.BAHIA_DNS_OPERATION_STATUS,
  KINDS.BAHIA_DNS_ZONE_CREATE_RESULT,
  KINDS.BAHIA_DNS_POLICY_APPLY_RESULT,
  KINDS.BAHIA_DNS_RECORD_OVERRIDE_RESULT,
  KINDS.BAHIA_DNS_DRIFT_REMEDIATE_RESULT,
  KINDS.BAHIA_DEPLOYMENT_STATUS,
  KINDS.BAHIA_SERVICE_STATUS,
  KINDS.BAHIA_LLM_DEPLOYMENT_STATUS,
  KINDS.BAHIA_PACKAGE_STATUS,
  KINDS.BAHIA_WORKER_STATUS,
  KINDS.BAHIA_DEPLOYMENT_RESULT,
  KINDS.BAHIA_ACTION_RESULT,
  KINDS.BAHIA_SERVICE_CREATE_RESULT,
  KINDS.BAHIA_ENVIRONMENT_CREATE_RESULT,
  KINDS.BAHIA_OBSERVATION_RESULT,
  KINDS.BAHIA_REMEDIATION_RESULT,
  KINDS.BAHIA_LLM_ROUTE_CREATE_RESULT,
  KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT,
  KINDS.BAHIA_LLM_DEPLOYMENT_RESULT,
  KINDS.BAHIA_PACKAGE_RESULT,
  KINDS.BAHIA_PACKAGE_DRIFT_EVENT,
  KINDS.BAHIA_WORKER_RESULT,
  KINDS.BAHIA_BACKUP_RUN_STATUS,
  KINDS.BAHIA_BACKUP_RESTORE_STATUS,
  KINDS.BAHIA_BACKUP_VERIFICATION_STATUS,
  KINDS.BAHIA_BACKUP_OBSERVATION,
  KINDS.BAHIA_BACKUP_RUN_RESULT,
  KINDS.BAHIA_BACKUP_VERIFICATION_RESULT,
  KINDS.BAHIA_BACKUP_RESTORE_RESULT,
  KINDS.BAHIA_BACKUP_RESTORE_APPROVAL_RESULT,
  KINDS.BAHIA_BACKUP_RETENTION_RESULT,
  KINDS.BAHIA_BACKUP_REPOSITORY_REGISTER_RESULT,
  KINDS.BAHIA_BACKUP_POLICY_APPLY_RESULT,
  KINDS.BAHIA_BACKUP_RECIPE_APPLY_RESULT,
  KINDS.BAHIA_BACKUP_DEFINITION_APPLY_RESULT,
  KINDS.BAHIA_BACKUP_REPOSITORY_PROBE_RESULT
];

export const BAHIA_SBOM_KINDS = [
  KINDS.SBOM_ATTESTATION,
  KINDS.SBOM_INDEX
];

export const BAHIA_AUDIT_KINDS = Array.from({ length: 100 }, (_, i) => 31000 + i);

export const BAHIA_CONTROLPLANE_KINDS = [
  ...BAHIA_READ_MODEL_KINDS,
  ...BAHIA_STATUS_KINDS,
  ...BAHIA_AUDIT_KINDS,
  ...BAHIA_SBOM_KINDS
];

// Operator assistant kinds are intentionally kept separate from the canonical
// Bahia control-plane arrays. Assistant subscriptions compose these explicitly.
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
const DEFAULT_RELAYS = [
  'wss://bahia.sharegap.net/relay'
];

// Storage key for user-configured relays
const RELAY_CONFIG_KEY = 'bahia_nostr_relays';


/**
 * Get configured relays from localStorage or return defaults
 */
function getConfiguredRelays() {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.getItem !== 'function') return DEFAULT_RELAYS;
  
  try {
    const stored = localStorage.getItem(RELAY_CONFIG_KEY);
    if (stored) {
      const relays = JSON.parse(stored);
      if (Array.isArray(relays) && relays.length > 0) {
        return relays;
      }
    }
  } catch (e) {
    console.error('[nostr] Failed to load relay config:', e);
  }
  
  return DEFAULT_RELAYS;
}

/**
 * Save relay configuration to localStorage
 */
export function saveRelayConfig(relays) {
  if (typeof window === 'undefined' || typeof localStorage === 'undefined' || typeof localStorage.setItem !== 'function') return;
  
  try {
    if (Array.isArray(relays) && relays.length > 0) {
      localStorage.setItem(RELAY_CONFIG_KEY, JSON.stringify(relays));
    } else {
      localStorage.removeItem(RELAY_CONFIG_KEY);
    }
  } catch (e) {
    console.error('[nostr] Failed to save relay config:', e);
  }
}

/**
 * Get the default relay list
 */
export function getDefaultRelays() {
  return [...DEFAULT_RELAYS];
}

export function getTagValues(event, name) {
  if (!event || !Array.isArray(event.tags)) return [];
  return event.tags
    .filter(tag => Array.isArray(tag) && tag[0] === name && tag[1])
    .map(tag => tag[1]);
}

export function getTagValue(event, name, fallback = '') {
  const values = getTagValues(event, name);
  return values.length > 0 ? values[values.length - 1] : fallback;
}

export function getDTag(event) {
  return getTagValue(event, 'd', '');
}

export function replaceableKey(event) {
  if (!event || !event.kind || !event.pubkey) return '';
  const d = getDTag(event);
  return d ? `${event.kind}:${event.pubkey}:${d}` : `${event.kind}:${event.pubkey}`;
}

export function parseJsonContent(event, fallback = {}) {
  if (!event || !event.content) return fallback;
  try {
    return JSON.parse(event.content);
  } catch {
    return fallback;
  }
}

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

export function normalizeSoulDraftContent(content = {}) {
  const identity = content.identity || {};
  const permissions = content.permissions || {};
  const runtime = content.runtime || {};
  const relayPolicy = content.relay_policy || content.relayPolicy || {};
  const workspace = content.workspace || {};
  const assets = content.assets || {};
  const avatar = content.avatar || content.avatar_spec || content.avatarSpec || null;
  const voice = content.voice || content.voice_spec || content.voiceSpec || null;
  const memory = content.memory || content.memory_spec || content.memorySpec || null;
  const persona = content.persona || content.persona_spec || content.personaSpec || null;
  const avatarRef = assets.avatar_ref || assets.avatarRef || content.avatar_ref || (
    avatar?.current === 'uploaded'
      ? avatar?.uploaded_ref || avatar?.uploadedRef || avatar?.generated_ref || avatar?.generatedRef
      : avatar?.generated_ref || avatar?.generatedRef || avatar?.uploaded_ref || avatar?.uploadedRef
  ) || '';

  return {
    ...content,
    ...(avatar ? { avatar } : {}),
    ...(voice ? { voice } : {}),
    ...(memory ? { memory } : {}),
    ...(persona ? { persona } : {}),
    identity: {
      ...identity,
      name: identity.name || content.name || '',
      purpose: identity.purpose || content.purpose || content.brief || '',
      tier: identity.tier || content.tier || 'standard',
      nip05: identity.nip05 || content.nip05 || ''
    },
    runtime: {
      ...runtime,
      target: runtime.target || runtime.runtime || '',
      runtime_pubkey: runtime.runtime_pubkey || runtime.runtimePubkey || '',
      capability_ref: runtime.capability_ref || runtime.capabilityRef || '',
      runtime_binding: runtime.runtime_binding || runtime.runtimeBinding || '',
      state: runtime.state || ''
    },
    permissions: {
      ...permissions,
      allowed_kinds: permissions.allowed_kinds || permissions.allowedKinds || content.allowed_kinds || [],
      tool_grants: permissions.tool_grants || permissions.toolGrants || content.tool_grants || [],
      approval_policy: permissions.approval_policy || permissions.approvalPolicy || ''
    },
    relay_policy: {
      read: relayPolicy.read || [],
      write: relayPolicy.write || [],
      control: relayPolicy.control || [],
      nip65_discovery: relayPolicy.nip65_discovery ?? relayPolicy.nip65Discovery ?? false
    },
    workspace: {
      ...workspace,
      repo: workspace.repo || workspace.repository || '',
      branch: workspace.branch || '',
      environment: workspace.environment || ''
    },
    assets: {
      ...assets,
      avatar_ref: avatarRef,
      voice_ref: assets.voice_ref || assets.voiceRef || content.voice_ref || ''
    },
    spec_hash: content.spec_hash || content.specHash || '',
    previous_spec_hash: content.previous_spec_hash || content.previousSpecHash || ''
  };
}

export function isReplaceableTombstone(event) {
  const content = parseJsonContent(event, {});
  if (content?.deleted === true) return true;
  return getTagValue(event, 'deleted') === 'true';
}

export function shouldAcceptReplaceableEvent(existing, incoming) {
  if (!incoming?.id) return false;
  if (!existing) return true;
  if (existing.id === incoming.id) return false;
  const incomingCreated = Number(incoming.created_at || 0);
  const existingCreated = Number(existing.created_at || 0);
  if (incomingCreated > existingCreated) return true;
  if (incomingCreated < existingCreated) return false;
  return String(incoming.id) > String(existing.id);
}

export function upsertReplaceableEvent(map, event) {
  const key = replaceableKey(event);
  if (!key) return { accepted: false, key: '', deleted: false };
  const existing = map.get(key);
  if (!shouldAcceptReplaceableEvent(existing, event)) {
    return { accepted: false, key, deleted: false };
  }
  if (isReplaceableTombstone(event)) {
    map.set(key, event);
    return { accepted: true, key, deleted: true };
  }
  map.set(key, event);
  return { accepted: true, key, deleted: false };
}

export function dedupeReplaceableEvents(events = []) {
  const map = new Map();
  for (const event of events || []) {
    upsertReplaceableEvent(map, event);
  }
  return Array.from(map.values())
    .filter((event) => !isReplaceableTombstone(event))
    .sort((a, b) => Number(b.created_at || 0) - Number(a.created_at || 0));
}

export function eventCoordinate(event) {
  const d = getDTag(event);
  if (!event?.kind || !event?.pubkey || !d) return '';
  return `${event.kind}:${event.pubkey}:${d}`;
}

function arrayFrom(value) {
  if (!value) return [];
  return Array.isArray(value) ? value.filter((item) => item !== undefined && item !== null && item !== '') : [value];
}

function unique(values = []) {
  return Array.from(new Set(values.filter(Boolean)));
}

function normalizeRelayHints(input = {}) {
  return {
    read: arrayFrom(input.read),
    write: arrayFrom(input.write),
    control: arrayFrom(input.control)
  };
}

export function parseRuntimeCapabilityEvent(event) {
  if (!event) return null;

  const content = parseJsonContent(event, {});
  const methods = [...arrayFrom(content.methods)];
  const controllerPubkeys = [
    ...arrayFrom(content.controller_pubkeys || content.controllerPubkeys),
    ...getTagValues(event, 'controller')
  ];
  const relayHints = normalizeRelayHints(content.relay_hints || content.relayHints || {});

  for (const tag of event.tags || []) {
    if (!Array.isArray(tag) || tag.length < 2) continue;
    switch (tag[0]) {
      case 'method':
        methods.push(tag[1]);
        break;
      case 'relay': {
        const scope = tag[2] || 'control';
        if (relayHints[scope]) relayHints[scope].push(tag[1]);
        break;
      }
      case 'read-relay':
        relayHints.read.push(tag[1]);
        break;
      case 'write-relay':
        relayHints.write.push(tag[1]);
        break;
      case 'control-relay':
        relayHints.control.push(tag[1]);
        break;
    }
  }

  const runtime = getTagValue(event, 'runtime', content.runtime || getDTag(event));
  const schema = getTagValue(event, 'schema', content.schema || '');
  const controlSchema = getTagValue(event, 'control-schema', content.control_schema || content.controlSchema || '');

  return {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    identifier: getDTag(event),
    coordinate: eventCoordinate(event),
    runtime,
    schema,
    controlSchema,
    methods: unique(methods),
    controllerPubkeys: unique(controllerPubkeys),
    relayHints: {
      read: unique(relayHints.read),
      write: unique(relayHints.write),
      control: unique(relayHints.control)
    },
    content,
    event,
    compatible:
      schema === SOUL_FACTORY_RUNTIME_CAPABILITY_SCHEMA &&
      controlSchema === SOUL_FACTORY_RUNTIME_CONTROL_SCHEMA
  };
}

export function runtimeCapabilitySupports(capability, { runtime = '', method = '', controllerPubkey = '' } = {}) {
  if (!capability?.compatible) return false;
  if (runtime && capability.runtime !== runtime) return false;
  if (method && !capability.methods.includes(method)) return false;
  if (controllerPubkey && capability.controllerPubkeys.length > 0 && !capability.controllerPubkeys.includes(controllerPubkey)) {
    return false;
  }
  return true;
}


export function createNostrPoolClient(options = {}) {
  return createPoolClient({
    relays: getConfiguredRelays(),
    saveRelayConfig,
    validateEvent: validateInboundNostrEvent,
    ...options
  });
}

// Singleton instance
export const nostr = createNostrPoolClient();

// --- Soul Factory specific helpers ---

export async function fetchTemplates(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_TEMPLATE] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return dedupeReplaceableEvents(await nostr.queryUntilEose([filter]));
}

export async function fetchSouls(authorPubkey = null) {
  const filter = { kinds: [KINDS.AGENT_SOUL] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return dedupeReplaceableEvents(await nostr.queryUntilEose([filter]));
}

export async function fetchSoulDrafts(authorPubkey = null) {
  const filter = { kinds: [KINDS.SOUL_DRAFT] };
  if (authorPubkey) {
    filter.authors = [authorPubkey];
  }
  return dedupeReplaceableEvents(await nostr.queryUntilEose([filter]));
}

export async function fetchSoulDraft(agentId, authorPubkey) {
  const events = await nostr.queryUntilEose([{
    kinds: [KINDS.SOUL_DRAFT],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }]);
  return dedupeReplaceableEvents(events)[0] || null;
}

export async function fetchRuntimeCapabilities({ runtime = null, controllerPubkey = null, method = null, limit = 200 } = {}) {
  const events = dedupeReplaceableEvents(await nostr.queryUntilEose([{ kinds: [KINDS.RUNTIME_CAPABILITY], limit }]));
  return events
    .map(parseRuntimeCapabilityEvent)
    .filter(Boolean)
    .filter((capability) => runtimeCapabilitySupports(capability, { runtime, controllerPubkey, method }));
}

export async function fetchSoul(agentId, authorPubkey) {
  const events = await nostr.queryUntilEose([{
    kinds: [KINDS.AGENT_SOUL],
    '#d': [agentId],
    authors: authorPubkey ? [authorPubkey] : undefined
  }]);
  return dedupeReplaceableEvents(events)[0] || null;
}

export function subscribeToProvisioningProgress(requestEventId, onStatus, onResult) {
  return nostr.subscribe([
    { kinds: [KINDS.PROVISIONING_STATUS], '#e': [requestEventId] },
    { kinds: [KINDS.PROVISIONING_RESULT, KINDS.SOUL_ACTION_LEGACY_RESULT], '#e': [requestEventId] }
  ], {
    onEvent: (event) => {
      if (event.kind === KINDS.PROVISIONING_STATUS) {
        onStatus(parseProvisioningStatus(event));
      } else if (isLifecycleResultKind(event.kind)) {
        onResult(parseProvisioningResult(event));
      }
    }
  });
}

export function parseSoulEvent(event) {
  const soul = {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    content: event.content,
    agentId: '',
    name: '',
    purpose: '',
    tier: 'standard',
    status: 'active',
    deployStatus: '',
    npub: '',
    agentPubkey: '',
    avatarUrl: '',
    nip05: '',
    workspace: '',
    qdrant: '',
    bahiaServiceId: '',
    allowedKinds: [],
    tools: [],
    draftRef: '',
    specHash: '',
    previousSpecHash: '',
    runtime: {},
    relayPolicy: {},
    permissions: {},
    workspaceSpec: {},
    assets: {},
    capabilityRef: '',
    lastResultRef: ''
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'd': soul.agentId = tag[1]; break;
      case 'name': soul.name = tag[1]; break;
      case 'purpose': soul.purpose = tag[1]; break;
      case 'tier': soul.tier = tag[1]; break;
      case 'status': soul.status = tag[1]; break;
      case 'deploy-status': soul.deployStatus = tag[1]; break;
      case 'npub': soul.npub = tag[1]; break;
      case 'p': if (tag[2] === 'agent') soul.agentPubkey = tag[1]; break;
      case 'avatar': soul.avatarUrl = tag[1]; break;
      case 'nip05': soul.nip05 = tag[1]; break;
      case 'workspace': soul.workspace = tag[1]; break;
      case 'qdrant': soul.qdrant = tag[1]; break;
      case 'service': soul.bahiaServiceId = tag[1]; break;
      case 'allowed-kind': soul.allowedKinds.push(parseInt(tag[1])); break;
      case 'tool': soul.tools.push({ server: tag[1], scopes: tag.slice(2) }); break;
      case 'draft': soul.draftRef = tag[1]; break;
      case 'spec-hash': soul.specHash = tag[1]; break;
      case 'previous-spec-hash': soul.previousSpecHash = tag[1]; break;
      case 'runtime': soul.runtime.target = tag[1]; break;
      case 'runtime-pubkey': soul.runtime.runtime_pubkey = tag[1]; break;
      case 'runtime-binding': soul.runtime.runtime_binding = tag[1]; break;
      case 'runtime-state': soul.runtime.state = tag[1]; break;
      case 'capability': soul.capabilityRef = tag[1]; soul.runtime.capability_ref = tag[1]; break;
      case 'last-result': soul.lastResultRef = tag[1]; break;
    }
  }

  const content = parseJsonContent(event, null);
  if (content && typeof content === 'object') {
    soul.runtime = { ...soul.runtime, ...(content.runtime || {}) };
    soul.relayPolicy = content.relay_policy || content.relayPolicy || soul.relayPolicy;
    soul.permissions = content.permissions || soul.permissions;
    soul.workspaceSpec = content.workspace || soul.workspaceSpec;
    soul.assets = content.assets || soul.assets;
    if (soul.allowedKinds.length === 0 && Array.isArray(soul.permissions?.allowed_kinds)) {
      soul.allowedKinds = soul.permissions.allowed_kinds;
    }
    if (soul.tools.length === 0 && Array.isArray(soul.permissions?.tool_grants)) {
      soul.tools = soul.permissions.tool_grants.map((grant) => {
        if (typeof grant === 'string') return { server: grant, scopes: [] };
        return {
          server: grant?.mcp_server || grant?.server || grant?.name || '',
          scopes: Array.isArray(grant?.scopes) ? grant.scopes : []
        };
      }).filter((grant) => grant.server);
    }
    soul.avatarUrl = soul.avatarUrl || content.avatar_url || content.avatarUrl || (String(soul.assets?.avatar_ref || '').startsWith('http') ? soul.assets.avatar_ref : '');
    soul.specHash = soul.specHash || content.spec_hash || content.specHash || '';
    soul.previousSpecHash = soul.previousSpecHash || content.previous_spec_hash || content.previousSpecHash || '';
    soul.draftRef = soul.draftRef || content.draft_ref || content.draftRef || '';
    soul.capabilityRef = soul.capabilityRef || content.capability_ref || content.capabilityRef || soul.runtime.capability_ref || '';
    soul.lastResultRef = soul.lastResultRef || content.last_result_ref || content.lastResultRef || '';
  }

  return soul;
}

export function parseSoulDraftEvent(event) {
  if (!event) return null;
  const content = normalizeSoulDraftContent(parseJsonContent(event, {}));
  return {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    agentId: getDTag(event),
    name: getTagValue(event, 'name', content.identity?.name || ''),
    tier: getTagValue(event, 'tier', content.identity?.tier || 'standard'),
    templateRef: getTagValue(event, 'template', ''),
    specHash: getTagValue(event, 'spec-hash', content.spec_hash || ''),
    previousSpecHash: getTagValue(event, 'previous-spec-hash', content.previous_spec_hash || ''),
    content
  };
}

export function parseTemplateEvent(event) {
  const content = parseJsonContent(event, null);
  const customization = content && typeof content === 'object'
    ? normalizeSoulDraftContent({
      schema: 'soulfactory-draft/v2',
      ...(content.customization || {}),
      persona: content.persona || content.customization?.persona,
      avatar: content.avatar || content.customization?.avatar,
      voice: content.voice || content.customization?.voice,
      memory: content.memory || content.customization?.memory
    })
    : null;
  const template = {
    id: event.id,
    pubkey: event.pubkey,
    createdAt: event.created_at,
    identifier: '',
    name: content?.name || '',
    description: content?.description || '',
    tier: content?.tier || 'standard',
    basePrompt: content?.brief || content?.basePrompt || content?.prompt || event.content,
    defaultCustomization: customization,
    defaultKinds: [],
    defaultTools: [],
    tags: []
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'd': template.identifier = tag[1]; break;
      case 'name': template.name = tag[1]; break;
      case 'description': template.description = tag[1]; break;
      case 'tier': template.tier = tag[1]; break;
      case 't': template.tags.push(tag[1]); break;
      case 'default-kind': template.defaultKinds.push(parseInt(tag[1])); break;
    }
  }

  return template;
}

export function parseRepositoryEvent(event) {
  if (!event || !event.id || !event.pubkey || !Array.isArray(event.tags)) {
    return null;
  }

  const repo = {
    id: event.id,
    pubkey: event.pubkey,
    created_at: event.created_at,
    identifier: '',
    name: '',
    description: '',
    webUrls: [],
    cloneUrls: [],
    relayUrls: [],
    earliestUniqueCommitId: '',
    maintainers: []
  };

  for (const tag of event.tags) {
    if (!Array.isArray(tag) || tag.length < 2) continue;

    switch (tag[0]) {
      case 'd':
        repo.identifier = tag[1] || '';
        break;
      case 'name':
        repo.name = tag[1] || '';
        break;
      case 'description':
        repo.description = tag[1] || '';
        break;
      case 'web':
        repo.webUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'clone':
        repo.cloneUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'relays':
        repo.relayUrls.push(...tag.slice(1).filter(Boolean));
        break;
      case 'r':
        repo.earliestUniqueCommitId = tag[1] || '';
        break;
      case 'maintainers':
        repo.maintainers.push(...tag.slice(1).filter(Boolean));
        break;
    }
  }

  if (!repo.identifier) {
    return null;
  }

  repo.repoCoordinate = `${KINDS.REPOSITORY}:${repo.pubkey}:${repo.identifier}`;
  repo.primaryUrl = repo.cloneUrls[0] || repo.webUrls[0] || '';
  repo.displayName = repo.name || repo.identifier || repo.primaryUrl;
  repo.searchText = [
    repo.identifier,
    repo.name,
    repo.description,
    repo.primaryUrl,
    repo.repoCoordinate,
    ...repo.cloneUrls,
    ...repo.webUrls,
    ...repo.relayUrls,
    ...repo.maintainers
  ].join(' ').toLowerCase();

  return repo;
}

export async function fetchRepositories({ authors = null, limit = 200, since = null } = {}) {
  const filter = {
    kinds: [KINDS.REPOSITORY],
    limit
  };

  if (Array.isArray(authors) && authors.length > 0) {
    filter.authors = authors;
  }

  if (typeof since === 'number') {
    filter.since = since;
  }

  const events = await nostr.query([filter]);
  const deduped = new Map();

  for (const event of events) {
    const parsed = parseRepositoryEvent(event);
    if (!parsed) continue;

    const existing = deduped.get(parsed.repoCoordinate);
    if (!existing || parsed.created_at >= existing.created_at) {
      deduped.set(parsed.repoCoordinate, parsed);
    }
  }

  return Array.from(deduped.values());
}

function parseProvisioningStatus(event) {
  const status = {
    id: event.id,
    step: '',
    progress: { current: 0, total: 0 },
    message: event.content
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'step': status.step = tag[1]; break;
      case 'progress':
        status.progress = { current: parseInt(tag[1]), total: parseInt(tag[2]) };
        break;
    }
  }

  return status;
}

function parseProvisioningResult(event) {
  const result = {
    id: event.id,
    success: false,
    error: '',
    soulRef: '',
    action: '',
    requestKind: '',
    specHash: '',
    data: {},
    legacyKind: event.kind === KINDS.SOUL_ACTION_LEGACY_RESULT
  };

  for (const tag of event.tags) {
    switch (tag[0]) {
      case 'status':
        result.success = tag[1] === 'success';
        if (tag[1] === 'error') result.error = event.content;
        break;
      case 'soul': result.soulRef = tag[1]; break;
      case 'action': result.action = tag[1]; break;
      case 'request-kind': result.requestKind = tag[1]; break;
      case 'spec-hash': result.specHash = tag[1]; break;
    }
  }

  if (event.content) {
    try {
      result.data = JSON.parse(event.content);
    } catch (e) {
      // Content might not be JSON
    }
  }

  return result;
}

export default nostr;
