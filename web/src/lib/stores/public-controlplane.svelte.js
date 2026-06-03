import { goto } from '$app/navigation';
import { getTagValue, parseJsonContent } from '$lib/nostr/client.js';
import { requestEncryptedResult } from '$lib/nostr/encrypted-controlplane.js';
import { bootstrapControlplane } from './controlplane.svelte.js';

function operationResultEvent({ requestEventId, resultEvent, result }) {
  if (result !== undefined) {
    return {
      id: resultEvent?.id || requestEventId || '',
      kind: resultEvent?.kind || 25910,
      tags: resultEvent?.tags || [['e', requestEventId || '']],
      content: JSON.stringify(result ?? {})
    };
  }
  return resultEvent || {
    id: requestEventId || '',
    kind: 25910,
    tags: [['e', requestEventId || '']],
    content: JSON.stringify({})
  };
}

function unwrapCommandPayload(content) {
  if (!content || typeof content !== 'object' || Array.isArray(content)) return content;
  const status = String(content.status || '').toLowerCase();
  if ((status === 'ok' || status === 'success') && Object.prototype.hasOwnProperty.call(content, 'payload')) {
    return content.payload;
  }
  return content;
}

export function resultContent(event) {
  return unwrapCommandPayload(parseJsonContent(event, {}));
}

export function throwIfErrorResult(event) {
  const rawContent = parseJsonContent(event, {});
  const status = String(getTagValue(event, 'status') || rawContent?.status || '').toLowerCase();
  if (status === 'error' || status === 'failed') {
    const error = rawContent?.error;
    const message = typeof error === 'object' && error !== null ? error.message : error;
    throw new Error(getTagValue(event, 'error') || message || rawContent?.message || event.content || 'Nostr command failed');
  }
  return event;
}

export async function publishCommand({ operation, tags = [], content = {}, payload, signal, timeoutMs } = {}) {
  if (typeof operation !== 'string' || !operation.trim()) {
    throw new Error('ContextVM operation is required for Nostr control-plane commands');
  }
  const bootstrap = await bootstrapControlplane();
  if (!bootstrap?.ok) {
    throw new Error(bootstrap?.reason || 'Failed to bootstrap relay-backed control plane');
  }
  const response = await requestEncryptedResult({
    operation,
    payload: payload ?? content,
    tags,
    signal,
    timeoutMs
  });
  return throwIfErrorResult(operationResultEvent(response));
}

export function createService(payload) {
  return publishCommand({ operation: 'service/create', content: payload });
}

export function updateService(id, payload) {
  return publishCommand({ operation: 'service/update', tags: [['service', id]], content: { ...payload, id } });
}

export function deleteService(id, force = false) {
  return publishCommand({ operation: 'service/delete', tags: [['service', id]], content: { id, force } });
}

export function createEnvironment(payload) {
  return publishCommand({ operation: 'environment/create', content: payload });
}

export function updateEnvironment(id, payload) {
  return publishCommand({ operation: 'environment/update', tags: [['environment', id]], content: { ...payload, id } });
}

export function deleteEnvironment(id, force = false) {
  return publishCommand({ operation: 'environment/delete', tags: [['environment', id]], content: { id, force } });
}

export function createDeploymentIntent(serviceId, environmentId, artifactId) {
  return publishCommand({
    operation: 'service/deploy',
    tags: [['service', serviceId], ['environment', environmentId], ['artifact', artifactId]],
    content: { service_id: serviceId, environment_id: environmentId, artifact_id: artifactId }
  });
}

export function rollbackDeployment(serviceId, environmentId) {
  return publishCommand({
    operation: 'service/rollback',
    tags: [['service', serviceId], ['environment', environmentId]],
    content: { service_id: serviceId, environment_id: environmentId }
  });
}

export function approveDeploymentIntent(id) {
  return publishCommand({ operation: 'approval/approve', tags: [['intent', id], ['decision', 'approve']], content: { intent_id: id, decision: 'approve' } });
}

export function rejectDeploymentIntent(id) {
  return publishCommand({ operation: 'approval/reject', tags: [['intent', id], ['decision', 'reject']], content: { intent_id: id, decision: 'reject' } });
}

export function createLLMRoute(payload) {
  return publishCommand({ operation: 'llm/route-create', content: payload });
}

export function registerLLMRelease(payload) {
  return publishCommand({
    operation: 'llm/release-register',
    tags: [['route', payload.route_id]].filter((tag) => tag[1]),
    content: payload
  });
}

async function requestLLMAsyncLifecycle(operation, payload, tags) {
  await bootstrapControlplane();
  const response = await requestEncryptedResult({ operation, payload, tags });
  const event = throwIfErrorResult(operationResultEvent(response));
  return { requestEventId: response.requestEventId, event };
}

export function requestLLMDeploy(payload) {
  return requestLLMAsyncLifecycle(
    'llm/deploy',
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
    'llm/rollback',
    payload,
    [
      ['route', payload.route_id],
      ['environment', payload.environment_id]
    ].filter((tag) => tag[1])
  );
}

export function approveLLMDeploymentIntent(id) {
  return publishCommand({
    operation: 'approval/llm-approve',
    tags: [['intent', id], ['decision', 'approve']],
    content: { intent_id: id, decision: 'approve' }
  });
}

export function rejectLLMDeploymentIntent(id) {
  return publishCommand({
    operation: 'approval/llm-reject',
    tags: [['intent', id], ['decision', 'reject']],
    content: { intent_id: id, decision: 'reject' }
  });
}

export function registerArtifact(payload) {
  return publishCommand({ operation: 'artifact/register', tags: [['service', payload.service_id], ['build', payload.build_id]].filter((tag) => tag[1]), content: payload });
}

export function promotePackage(payload) {
  return publishCommand({
    operation: 'package/promote',
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
    content: payload
  });
}

export function yankPackage(payload) {
  return publishCommand({
    operation: 'package/yank',
    tags: [
      ['operation', payload.deprecated ? 'deprecate' : 'yank'],
      ['repository', payload.repository_id],
      ['repository_name', payload.repository_name],
      ['namespace', payload.namespace],
      ['package', payload.package_name],
      ['version', payload.version],
      ['filename', payload.filename]
    ].filter((tag) => tag[1]),
    content: payload
  });
}

export function createPolicy(payload) {
  return publishCommand({ operation: 'policy/create', tags: payload.environment_id ? [['environment', payload.environment_id]] : [], content: payload });
}

export function updatePolicy(id, payload) {
  return publishCommand({ operation: 'policy/update', tags: [['policy', id]], content: { ...payload, id } });
}

export function deletePolicy(id) {
  return publishCommand({ operation: 'policy/delete', tags: [['policy', id]], content: { id } });
}

export async function evaluatePolicy(payload) {
  const event = await publishCommand({ operation: 'policy/evaluate', tags: [['artifact', payload.artifact_id], ['environment', payload.environment_id]], content: payload });
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
    operation: 'backup/repository-probe',
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
    }
  });
}

export function decideBackupRestore(restore, approved, message = '') {
  const restoreId = restore?.id || restore?.restore_id || '';
  if (!restoreId) throw new Error('restore id is required');
  const decision = approved ? 'approve' : 'reject';
  const idempotencyKey = backupIdempotencyKey(`restore_${decision}`, restoreId);
  return publishCommand({
    operation: 'approval/backup-restore-approve',
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
    }
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
