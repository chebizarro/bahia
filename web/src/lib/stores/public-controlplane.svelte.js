import { goto } from '$app/navigation';
import { KINDS, getTagValue, parseJsonContent } from '$lib/nostr/client.js';
import { publishRequest, awaitResult } from '$lib/nostr/controlplane-requests.js';
import { bootstrapControlplane } from './controlplane.svelte.js';

const ACTION_RESULTS = [
  KINDS.BAHIA_ACTION_RESULT,
  KINDS.BAHIA_DEPLOYMENT_RESULT,
  KINDS.BAHIA_SERVICE_CREATE_RESULT,
  KINDS.BAHIA_ENVIRONMENT_CREATE_RESULT
];

export function resultContent(event) {
  return parseJsonContent(event, {});
}

export function throwIfErrorResult(event) {
  const status = String(getTagValue(event, 'status') || resultContent(event)?.status || '').toLowerCase();
  if (status === 'error' || status === 'failed') {
    const content = resultContent(event);
    throw new Error(getTagValue(event, 'error') || content.error || event.content || 'Nostr command failed');
  }
  return event;
}

export async function publishCommand({ kind, tags = [], content = {}, resultKinds = ACTION_RESULTS } = {}) {
  await bootstrapControlplane();
  const { requestEventId } = await publishRequest({ kind, tags, content });
  const result = await awaitResult({ requestEventId, resultKinds });
  return throwIfErrorResult(result);
}

export function createService(payload) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_SERVICE_CREATE, content: payload, resultKinds: [KINDS.BAHIA_SERVICE_CREATE_RESULT, KINDS.BAHIA_DEPLOYMENT_RESULT] });
}

export function updateService(id, payload) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_SERVICE_UPDATE, tags: [['service', id]], content: { ...payload, id } });
}

export function deleteService(id, force = false) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_SERVICE_DELETE, tags: [['service', id]], content: { id, force } });
}

export function createEnvironment(payload) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_ENVIRONMENT_CREATE, content: payload, resultKinds: [KINDS.BAHIA_ENVIRONMENT_CREATE_RESULT, KINDS.BAHIA_DEPLOYMENT_RESULT] });
}

export function updateEnvironment(id, payload) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_ENVIRONMENT_UPDATE, tags: [['environment', id]], content: { ...payload, id } });
}

export function deleteEnvironment(id, force = false) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_ENVIRONMENT_DELETE, tags: [['environment', id]], content: { id, force } });
}

export function createDeploymentIntent(serviceId, environmentId, artifactId) {
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_DEPLOY,
    tags: [['service', serviceId], ['environment', environmentId], ['artifact', artifactId]],
    content: { service_id: serviceId, environment_id: environmentId, artifact_id: artifactId },
    resultKinds: [KINDS.BAHIA_DEPLOYMENT_RESULT]
  });
}

export function rollbackDeployment(serviceId, environmentId) {
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_ROLLBACK,
    tags: [['service', serviceId], ['environment', environmentId]],
    content: { service_id: serviceId, environment_id: environmentId },
    resultKinds: [KINDS.BAHIA_DEPLOYMENT_RESULT]
  });
}

export function approveDeploymentIntent(id) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_DEPLOYMENT_APPROVAL, tags: [['intent', id], ['decision', 'approve']], content: { intent_id: id, decision: 'approve' } });
}

export function rejectDeploymentIntent(id) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_DEPLOYMENT_APPROVAL, tags: [['intent', id], ['decision', 'reject']], content: { intent_id: id, decision: 'reject' } });
}

export function createLLMRoute(payload) {
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_LLM_ROUTE_CREATE,
    content: payload,
    resultKinds: [KINDS.BAHIA_LLM_ROUTE_CREATE_RESULT]
  });
}

export function registerLLMRelease(payload) {
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_LLM_RELEASE_REGISTER,
    tags: [['route', payload.route_id]].filter((tag) => tag[1]),
    content: payload,
    resultKinds: [KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT]
  });
}

async function requestLLMAsyncLifecycle(kind, payload, tags) {
  await bootstrapControlplane();
  const { requestEventId } = await publishRequest({
    kind,
    tags,
    content: payload
  });
  const event = await awaitResult({
    requestEventId,
    resultKinds: [KINDS.BAHIA_LLM_DEPLOYMENT_STATUS, KINDS.BAHIA_LLM_DEPLOYMENT_RESULT]
  });
  return {
    requestEventId,
    event: throwIfErrorResult(event)
  };
}

export function requestLLMDeploy(payload) {
  return requestLLMAsyncLifecycle(
    KINDS.BAHIA_REQUEST_LLM_DEPLOY,
    payload,
    [
      ['route', payload.route_id],
      ['environment', payload.environment_id],
      ['release', payload.release_id]
    ].filter((tag) => tag[1])
  );
}

export function requestLLMRollback(payload) {
  return requestLLMAsyncLifecycle(
    KINDS.BAHIA_REQUEST_LLM_ROLLBACK,
    payload,
    [
      ['route', payload.route_id],
      ['environment', payload.environment_id]
    ].filter((tag) => tag[1])
  );
}

export function approveLLMDeploymentIntent(id) {
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_LLM_DEPLOYMENT_APPROVAL,
    tags: [['intent', id], ['decision', 'approve']],
    content: { intent_id: id, decision: 'approve' },
    resultKinds: [KINDS.BAHIA_LLM_DEPLOYMENT_RESULT]
  });
}

export function rejectLLMDeploymentIntent(id) {
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_LLM_DEPLOYMENT_APPROVAL,
    tags: [['intent', id], ['decision', 'reject']],
    content: { intent_id: id, decision: 'reject' },
    resultKinds: [KINDS.BAHIA_LLM_DEPLOYMENT_RESULT]
  });
}

export function registerArtifact(payload) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_ARTIFACT_REGISTER, tags: [['service', payload.service_id], ['build', payload.build_id]].filter((tag) => tag[1]), content: payload });
}

export function promotePackage(payload) {
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_PACKAGE_PROMOTE,
    tags: [
      ['operation', 'promote'],
      ['repository', payload.source_repository_id],
      ['repository_name', payload.source_repository_name],
      ['target_repository', payload.target_repository_id],
      ['target_repository_name', payload.target_repository_name],
      ['namespace', payload.namespace],
      ['package', payload.package_name],
      ['version', payload.version],
      ['filename', payload.filename]
    ].filter((tag) => tag[1]),
    content: payload,
    resultKinds: [KINDS.BAHIA_PACKAGE_RESULT]
  });
}

export function yankPackage(payload) {
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_PACKAGE_YANK,
    tags: [
      ['operation', payload.deprecated ? 'deprecate' : 'yank'],
      ['repository', payload.repository_id],
      ['repository_name', payload.repository_name],
      ['namespace', payload.namespace],
      ['package', payload.package_name],
      ['version', payload.version],
      ['filename', payload.filename]
    ].filter((tag) => tag[1]),
    content: payload,
    resultKinds: [KINDS.BAHIA_PACKAGE_RESULT]
  });
}

export function createPolicy(payload) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_POLICY_CREATE, tags: payload.environment_id ? [['environment', payload.environment_id]] : [], content: payload });
}

export function updatePolicy(id, payload) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_POLICY_UPDATE, tags: [['policy', id]], content: { ...payload, id } });
}

export function deletePolicy(id) {
  return publishCommand({ kind: KINDS.BAHIA_REQUEST_POLICY_DELETE, tags: [['policy', id]], content: { id } });
}

export async function evaluatePolicy(payload) {
  const event = await publishCommand({ kind: KINDS.BAHIA_REQUEST_POLICY_EVALUATE, tags: [['artifact', payload.artifact_id], ['environment', payload.environment_id]], content: payload, resultKinds: [KINDS.BAHIA_ACTION_RESULT, KINDS.BAHIA_DEPLOYMENT_RESULT] });
  return resultContent(event);
}

function randomId() {
  const cryptoApi = globalThis.crypto;
  if (cryptoApi?.randomUUID) return cryptoApi.randomUUID();
  if (cryptoApi?.getRandomValues) {
    const bytes = new Uint8Array(16);
    cryptoApi.getRandomValues(bytes);
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  }
  throw new Error('Browser cryptographic random ID generation is unavailable');
}

function backupIdempotencyKey(prefix, id) {
  return `web.backup.${prefix}:${id || 'fleet'}:${randomId()}`;
}

export function probeBackupRepository(repository) {
  const repositoryId = repository?.id || repository?.repository_id || '';
  if (!repositoryId) throw new Error('repository id is required');
  const idempotencyKey = backupIdempotencyKey('repository_probe', repositoryId);
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_BACKUP_REPOSITORY_PROBE,
    tags: [
      ['d', idempotencyKey],
      ['repository_id', repositoryId],
      ['repository', repository?.name || repositoryId]
    ].filter((tag) => tag[1]),
    content: {
      repository_id: repositoryId,
      repository: repository?.name || '',
      idempotency_key: idempotencyKey,
      metadata: { source: 'web.backup.repositories' }
    },
    resultKinds: [KINDS.BAHIA_BACKUP_REPOSITORY_PROBE_RESULT]
  });
}

export function decideBackupRestore(restore, approved, message = '') {
  const restoreId = restore?.id || restore?.restore_id || '';
  if (!restoreId) throw new Error('restore id is required');
  const decision = approved ? 'approve' : 'reject';
  const idempotencyKey = backupIdempotencyKey(`restore_${decision}`, restoreId);
  return publishCommand({
    kind: KINDS.BAHIA_REQUEST_BACKUP_RESTORE_APPROVAL,
    tags: [
      ['d', idempotencyKey],
      ['restore_id', restoreId],
      ['restore', restoreId],
      ['decision', decision]
    ],
    content: {
      restore_id: restoreId,
      approved,
      decision,
      message,
      reason_code: approved ? 'operator_approved' : 'operator_rejected',
      reason: { source: 'web.backup.restores' },
      idempotency_key: idempotencyKey
    },
    resultKinds: [KINDS.BAHIA_BACKUP_RESTORE_APPROVAL_RESULT]
  });
}

export function approveBackupRestore(restore, message = '') {
  return decideBackupRestore(restore, true, message);
}

export function rejectBackupRestore(restore, message = '') {
  return decideBackupRestore(restore, false, message);
}

export function navigateAfterCommand(path) {
  return goto(path);
}
