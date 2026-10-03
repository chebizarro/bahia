import { goto } from '$app/navigation';
import { getTagValue, parseJsonContent } from '$lib/nostr/client.js';
import { CONTEXTVM_MESSAGE_KIND, publishEncryptedRequest, requestEncryptedResult } from '$lib/nostr/encrypted-controlplane.js';
import { bootstrapControlplane } from './controlplane.svelte.js';
import { mintEntityId, withEntityId } from '$lib/entity-id.js';
import { publishIntent, canonicalIntentRecord, resolveIntentOrgId } from '$lib/nostr/intent-client.svelte.js';
import { orgRoles } from './auth-roles.svelte.js';
import { orgsState } from './orgs.svelte.js';
import { currentSystemInfo } from './system.svelte.js';
import { backupRecipes, backupRepositories, backupPolicies, backupDefinitions } from './collections/backup.svelte.js';

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function intentOrgId(payload, current, domain) {
  const explicit = [payload?.org_id, current?.org_id,
    payload?.environment_id ? canonicalIntentRecord(payload.environment_id)?.content?.org_id : null]
    .find(value => UUID.test(String(value || '')));
  return resolveIntentOrgId(domain, explicit, [...Object.keys(orgRoles),
    ...orgsState.orgs.map(org => org.id || org.org_id), currentSystemInfo()?.organization_id]);
}

async function mutateIntent(domain, op, payload, id = payload?.id) {
  const coordinate = String(id || '').trim();
  if (!coordinate) throw new Error(`${domain} intent requires an entity id`);
  const current = op === 'create' ? null : canonicalIntentRecord(coordinate);
  if (op === 'update' && !current?.content?.updated_at) {
    throw new Error('Current canonical revision is unavailable; re-read and resubmit');
  }
  const orgId = intentOrgId(payload, current?.content, domain);
  const content = op === 'delete'
    ? { id: coordinate, org_id: orgId, deleted: true, ...(payload?.force ? { force: true } : {}) }
    : { ...(current?.content || {}), ...payload, id: coordinate, org_id: orgId };
  delete content.expected_updated_at;
  return publishIntent({ domain, op, coordinate, orgId, content, currentRecord: current?.content });
}

function operationResultEvent({ requestEventId, resultEvent, result }) {
  if (result !== undefined) {
    return {
      id: resultEvent?.id || requestEventId || '',
      requestEventId: requestEventId || '',
      kind: resultEvent?.kind || 25910,
      tags: resultEvent?.tags || [['e', requestEventId || '']],
      content: JSON.stringify(result ?? {})
    };
  }
  return resultEvent || {
    id: requestEventId || '',
    requestEventId: requestEventId || '',
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
  if (event?.pending) return { ...event.desiredState, status: 'pending', message: 'Signed intent pending daemon acceptance' };
  return unwrapCommandPayload(parseJsonContent(event, {}));
}

export function throwIfErrorResult(event, operation = '') {
  const rawContent = parseJsonContent(event, {});
  const status = String(getTagValue(event, 'status') || rawContent?.status || '').toLowerCase();
  if (status === 'error' || status === 'failed') {
    const payloadError = rawContent?.error;
    const message = typeof payloadError === 'object' && payloadError !== null ? payloadError.message : payloadError;
    const error = new Error(getTagValue(event, 'error') || message || rawContent?.message || event.content || 'Nostr command failed');
    const code = typeof payloadError === 'object' && payloadError !== null
      ? payloadError.code
      : rawContent?.code ?? rawContent?.error_code;
    const data = typeof payloadError === 'object' && payloadError !== null
      ? payloadError.data
      : rawContent?.data;
    if (code !== undefined) error.code = code;
    if (data !== undefined) error.data = data;
    if (operation) error.operation = operation;
    throw error;
  }
  return event;
}

export async function publishCommand({ operation, tags = [], content = {}, payload, signal, timeoutMs, requestId } = {}) {
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
    kind: CONTEXTVM_MESSAGE_KIND,
    resultKinds: [CONTEXTVM_MESSAGE_KIND],
    signal,
    timeoutMs,
    ...(requestId ? { requestId } : {})
  });
  return throwIfErrorResult(operationResultEvent(response), operation);
}

/**
 * Publish a ContextVM command and return immediately after relay acceptance.
 * Does NOT wait for a result event — the caller must subscribe to canonical
 * observables for durable progress and terminal truth (per AGENTS.md).
 * Use this for long-running operations (e.g. sbom/generate) where completion
 * is detected via scoped subscriptions, not timeout-based result waiting.
 */
export async function publishCommandOnly({ operation, tags = [], content = {}, payload, signal } = {}) {
  if (typeof operation !== 'string' || !operation.trim()) {
    throw new Error('ContextVM operation is required for Nostr control-plane commands');
  }
  const bootstrap = await bootstrapControlplane();
  if (!bootstrap?.ok) {
    throw new Error(bootstrap?.reason || 'Failed to bootstrap relay-backed control plane');
  }
  return publishEncryptedRequest({
    operation,
    payload: payload ?? content,
    tags,
    kind: CONTEXTVM_MESSAGE_KIND,
    signal
  });
}

// Create intents carry a client-minted entity id (bahia-irsry.35). Callers that
// may retry should mint it once and pass it in; otherwise one is minted here.
export async function createService(payload) {
  return mutateIntent('service', 'create', withEntityId(payload));
}

export function updateService(id, payload) {
  return mutateIntent('service', 'update', payload, id);
}

export function deleteService(id, force = false) {
  return mutateIntent('service', 'delete', { id, force }, id);
}

export async function createEnvironment(payload) {
  return mutateIntent('environment', 'create', withEntityId(payload));
}

export function updateEnvironment(id, payload) {
  return mutateIntent('environment', 'update', payload, id);
}

export function deleteEnvironment(id, force = false) {
  return mutateIntent('environment', 'delete', { id, force }, id);
}

export async function previewServiceDeployment(payload) {
  const unitId = String(payload?.deployment_unit_id || '').trim();
  const event = await publishCommand({
    operation: 'service/deploy-preview',
    tags: [
      ['service', payload?.service_id],
      ['environment', payload?.environment_id],
      ...(unitId ? [['unit', unitId]] : []),
      ['artifact', payload?.artifact_id]
    ].filter((tag) => tag[1]),
    content: {
      ...payload,
      ...(unitId ? { deployment_unit_id: unitId } : {})
    }
  });
  return resultContent(event);
}

export function createDeploymentIntent(serviceId, environmentId, artifactId, deploymentUnitId = '', expectedDesiredStateHash = '', publicRoute = null) {
  const unitId = String(deploymentUnitId || '').trim();
  const expectedHash = String(expectedDesiredStateHash || '').trim();
  const content = {
    service_id: serviceId,
    environment_id: environmentId,
    ...(unitId ? { deployment_unit_id: unitId } : {}),
    artifact_id: artifactId,
    ...(publicRoute ? { public_route: publicRoute } : {}),
    ...(expectedHash ? { expected_desired_state_hash: expectedHash, idempotency_key: expectedHash } : {})
  };
  return publishCommand({
    operation: 'service/deploy',
    tags: [
      ['service', serviceId],
      ['environment', environmentId],
      ...(unitId ? [['unit', unitId]] : []),
      ['artifact', artifactId]
    ],
    content,
    ...(expectedHash ? { requestId: expectedHash } : {})
  });
}

export function rollbackDeployment(payload) {
  if (!payload || typeof payload !== 'object') {
    return Promise.reject(new Error('Rollback requires an explicit artifact target from deployment history.'));
  }
  const serviceId = payload.service_id;
  const environmentId = payload.environment_id;
  const unitId = payload.deployment_unit_id;
  const artifactId = payload.target_artifact_id;
  const supersedesIntentId = payload.supersedes_intent_id;
  if (!artifactId) {
    return Promise.reject(new Error('Rollback requires an explicit artifact target from deployment history.'));
  }
  return publishCommand({
    operation: 'service/rollback',
    tags: [
      ['service', serviceId],
      ['environment', environmentId],
      ['unit', unitId],
      ['artifact', artifactId],
      ['intent', supersedesIntentId]
    ].filter((tag) => tag[1]),
    content: payload
  });
}

export function approveDeploymentIntent(id) {
  return publishCommand({ operation: 'approval/approve', tags: [['intent', id], ['decision', 'approve']], content: { intent_id: id, decision: 'approve' } });
}

export function rejectDeploymentIntent(id) {
  return publishCommand({ operation: 'approval/reject', tags: [['intent', id], ['decision', 'reject']], content: { intent_id: id, decision: 'reject' } });
}

// The route id is client-minted (bahia-irsry.42): pass the same payload.id to
// retry; one is minted when absent.
export async function createLLMRoute(payload) {
  const content = withEntityId(payload);
  return publishIntent({ domain: 'llm', op: 'create', coordinate: content.id,
    orgId: intentOrgId(content, canonicalIntentRecord(content.id)?.content, 'llm'), content });
}

export function registerLLMRelease(payload) {
  const content = withEntityId(payload);
  return publishIntent({ domain: 'llm', op: 'release-register', coordinate: `llm-release:${content.id}`,
    orgId: intentOrgId(content, canonicalIntentRecord(`llm-release:${content.id}`)?.content, 'llm'), content });
}

async function requestLLMAsyncLifecycle(operation, payload, tags) {
  await bootstrapControlplane();
  const response = await requestEncryptedResult({
    operation,
    payload,
    tags,
    kind: CONTEXTVM_MESSAGE_KIND,
    resultKinds: [CONTEXTVM_MESSAGE_KIND]
  });
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

function artifactDigest(artifact) {
  return String(artifact?.digest || artifact?.image_digest || artifact?.metadata?.digest || '').trim();
}

function artifactRepository(artifact) {
  const candidates = [
    artifact?.image_repo,
    artifact?.image_repository,
    artifact?.oci_repository,
    artifact?.repository,
    artifact?.artifact_repo,
    artifact?.service_artifact_repo,
    artifact?.metadata?.image_repo,
    artifact?.metadata?.image_repository,
    artifact?.metadata?.oci_repository,
    artifact?.metadata?.repository
  ];
  return String(candidates.find((candidate) => String(candidate || '').trim()) || '').trim();
}

function artifactImageLocator(artifact, digest = artifactDigest(artifact)) {
  const explicit = String(artifact?.image_ref || artifact?.oci_ref || artifact?.source_ref || artifact?.metadata?.image_ref || artifact?.metadata?.oci_ref || artifact?.metadata?.source_ref || '').trim();
  if (explicit) return explicit;
  const repo = artifactRepository(artifact);
  const tag = String(artifact?.image_tag || artifact?.tag || artifact?.version || artifact?.metadata?.image_tag || artifact?.metadata?.tag || artifact?.metadata?.version || '').trim();
  if (repo && digest) return `${repo}@${digest}`;
  if (repo && tag) return `${repo}:${tag}`;
  return '';
}

function artifactDisplayName(artifact) {
  return String(artifact?.name || artifact?.image_repo || artifact?.image_tag || artifact?.id || '').trim();
}

// An inline SBOM travels base64-encoded (4/3 larger) inside one relay message,
// and Bahia's relay drops frames over 512,000 bytes without an OK. 360 KiB
// encodes to 491,520 bytes and leaves room for the envelope. The daemon
// enforces the same limit (maxContextVMInlineSBOMBytes).
export const MAX_CONTEXTVM_INLINE_SBOM_BYTES = 360 * 1024;

function normalizeSBOMFormat(format) {
  const normalized = String(format || '').trim().toLowerCase();
  if (normalized === 'spdx' || normalized === 'cyclonedx') return normalized;
  throw new Error('SBOM format must be SPDX or CycloneDX');
}

function normalizeSBOMGenerator(generator, fallback = 'import') {
  if (generator && typeof generator === 'object' && !Array.isArray(generator)) {
    const id = String(generator.id || fallback).trim() || fallback;
    return {
      id,
      ...(generator.version ? { version: String(generator.version) } : {}),
      ...(generator.pubkey ? { pubkey: String(generator.pubkey) } : {})
    };
  }
  return { id: String(generator || fallback).trim() || fallback };
}

function decodedBase64Length(payloadBase64) {
  const normalized = String(payloadBase64 || '').replace(/\s/g, '');
  if (!normalized) return 0;
  const padding = normalized.endsWith('==') ? 2 : normalized.endsWith('=') ? 1 : 0;
  return Math.floor((normalized.length * 3) / 4) - padding;
}

function inlineSBOMSourceKey(payloadBase64) {
  const normalized = String(payloadBase64 || '').replace(/\s/g, '');
  return `inline:${decodedBase64Length(normalized)}:${normalized.slice(0, 24)}:${normalized.slice(-24)}`;
}

export function inlineSBOMLimitMessage() {
  return `Inline SBOM imports are limited to ${MAX_CONTEXTVM_INLINE_SBOM_BYTES} bytes (360 KiB); upload larger SBOM files to Blossom and import them by location.`;
}

export function generateArtifactSBOM(artifact, { formats = ['spdx', 'cyclonedx'], generator = 'syft', signal } = {}) {
  const artifactId = String(artifact?.id || '').trim();
  if (!artifactId) throw new Error('artifact id is required');
  const digest = artifactDigest(artifact);
  if (!digest) throw new Error('artifact digest is required');
  const locator = artifactImageLocator(artifact, digest);
  if (!locator) throw new Error('artifact image repository or OCI image ref is required');
  const normalizedFormats = Array.from(new Set((Array.isArray(formats) ? formats : [formats]).map((format) => String(format || '').trim()).filter(Boolean)));
  if (normalizedFormats.length === 0) throw new Error('at least one SBOM format is required');
  const generatorId = String(generator || 'syft').trim() || 'syft';
  const idempotencyKey = `web.sbom.generate:artifact:${artifactId}:${digest}:${normalizedFormats.join(',')}:${generatorId}`;
  // Publish-only: do NOT wait for a ContextVM result with a timeout.
  // SBOM generation is a long-running operation; terminal truth arrives as
  // canonical 30078/30004 observable events via the caller's scoped subscription.
  return publishCommandOnly({
    operation: 'sbom/generate',
    tags: [
      ['domain', 'sbom'],
      ['operation', 'sbom/generate'],
      ['subject_type', 'artifact'],
      ['artifact', artifactId],
      ['subject', digest],
      ['generator', generatorId]
    ],
    content: {
      idempotencyKey,
      subject: {
        type: 'artifact',
        id: artifactId,
        display_name: artifactDisplayName(artifact),
        digest
      },
      source: {
        kind: 'oci-image',
        locator
      },
      formats: normalizedFormats,
      generator: generatorId,
      storage: 'blossom'
    },
    signal
  });
}

export function importArtifactSBOM(artifact, { format = 'spdx', payloadBase64 = '', location = null, storage = '', generator = { id: 'import' }, idempotencyKey = '', signal } = {}) {
  const artifactId = String(artifact?.id || '').trim();
  if (!artifactId) throw new Error('artifact id is required');
  const digest = artifactDigest(artifact);
  if (!digest) throw new Error('artifact digest is required');
  const normalizedFormat = normalizeSBOMFormat(format);
  const inlinePayload = String(payloadBase64 || '').replace(/\s/g, '');
  const hasInlinePayload = inlinePayload.length > 0;
  const normalizedLocation = location && typeof location === 'object'
    ? {
        type: String(location.type || storage || 'blossom').trim() || 'blossom',
        uri: String(location.uri || '').trim(),
        ...(location.mediaType ? { mediaType: String(location.mediaType) } : {})
      }
    : null;
  const hasLocation = Boolean(normalizedLocation?.uri);
  if (hasInlinePayload && hasLocation) throw new Error('provide either inline payloadBase64 or location, not both');
  if (!hasInlinePayload && !hasLocation) throw new Error('SBOM import requires an inline payload or a Blossom/REST compatibility import reference');
  if (hasInlinePayload && decodedBase64Length(inlinePayload) > MAX_CONTEXTVM_INLINE_SBOM_BYTES) {
    throw new Error(inlineSBOMLimitMessage());
  }
  const generatorInfo = normalizeSBOMGenerator(generator, 'import');
  const storageType = String(storage || normalizedLocation?.type || 'blossom').trim() || 'blossom';
  const sourceKey = hasLocation ? `location:${normalizedLocation.type}:${normalizedLocation.uri}` : inlineSBOMSourceKey(inlinePayload);
  const finalIdempotencyKey = String(idempotencyKey || '').trim() || `web.sbom.import:artifact:${artifactId}:${digest}:${normalizedFormat}:${sourceKey}:${generatorInfo.id}`;

  return publishCommandOnly({
    operation: 'sbom/import',
    tags: [
      ['domain', 'sbom'],
      ['operation', 'sbom/import'],
      ['subject_type', 'artifact'],
      ['artifact', artifactId],
      ['subject', digest],
      ['format', normalizedFormat],
      ['generator', generatorInfo.id]
    ],
    content: {
      idempotencyKey: finalIdempotencyKey,
      subject: {
        type: 'artifact',
        id: artifactId,
        display_name: artifactDisplayName(artifact),
        digest
      },
      format: normalizedFormat,
      ...(hasInlinePayload ? { payloadBase64: inlinePayload } : { location: normalizedLocation }),
      storage: storageType,
      generator: generatorInfo
    },
    signal
  });
}

export function promotePackage(payload) {
  const coordinate = ['package', payload.target_repository_id, payload.namespace,
    payload.package_name, payload.version, payload.filename].map(encodeURIComponent).join(':');
  return publishIntent({ domain: 'package', op: 'promote', coordinate,
    orgId: intentOrgId(payload, null, 'package'), content: payload });
}

export function yankPackage(payload) {
  const coordinate = ['package', payload.repository_id, payload.namespace,
    payload.package_name, payload.version, payload.filename].map(encodeURIComponent).join(':');
  return publishIntent({ domain: 'package', op: 'yank', coordinate,
    orgId: intentOrgId(payload, null, 'package'), content: payload });
}

// The policy id is client-minted (bahia-irsry.42): pass the same payload.id to
// retry; one is minted when absent.
export async function createPolicy(payload) {
  return mutateIntent('policy', 'create', withEntityId(payload));
}

export function updatePolicy(id, payload) {
  return mutateIntent('policy', 'update', payload, id);
}

export function deletePolicy(id) {
  return mutateIntent('policy', 'delete', { id }, id);
}

export async function evaluatePolicy(payload) {
  const event = await publishCommand({
    operation: 'policy/evaluate',
    tags: [
      ['service', payload.service_id],
      ['environment', payload.environment_id],
      ['unit', payload.deployment_unit_id],
      ['artifact', payload.artifact_id]
    ].filter((tag) => tag[1]),
    content: payload
  });
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

function backupMetadata(source, metadata = {}) {
  return { ...(metadata && typeof metadata === 'object' && !Array.isArray(metadata) ? metadata : {}), source };
}

function backupRequired(value, label) {
  const text = String(value || '').trim();
  if (!text) throw new Error(`${label} is required`);
  return text;
}

export function registerBackupRepository(payload) {
  const name = backupRequired(payload?.name, 'repository name');
  const backend = backupRequired(payload?.backend, 'repository backend');
  const repositoryUri = backupRequired(payload?.repository_uri || payload?.uri, 'repository URI');
  const existing = backupRepositories.find(row => row.name === name);
  const content = {
    ...withEntityId({ ...payload, id: payload?.id || existing?.id }),
    name,
    backend,
    repository_uri: repositoryUri,
    metadata: backupMetadata('web.backup.repositories.register', payload?.metadata)
  };
  return publishIntent({ domain: 'backup', op: 'repository-register', coordinate: `backup-repository:${content.id}`,
    orgId: intentOrgId(content, canonicalIntentRecord(`backup-repository:${content.id}`)?.content, 'backup'), content });
}

export function applyBackupPolicy(payload) {
  const name = backupRequired(payload?.name, 'policy name');
  const verificationMode = String(payload?.verification_mode || (payload?.require_verification ? 'kopia_snapshot_verify' : 'none')).trim() || 'none';
  const existing = backupPolicies.find(row => row.name === name);
  const content = {
    ...withEntityId({ ...payload, id: payload?.id || existing?.id }),
    name,
    require_verification: Boolean(payload?.require_verification),
    verification_mode: verificationMode,
    metadata: backupMetadata('web.backup.policies.apply', payload?.metadata)
  };
  return publishIntent({ domain: 'backup', op: 'policy-apply', coordinate: `backup-policy:${content.id}`,
    orgId: intentOrgId(content, canonicalIntentRecord(`backup-policy:${content.id}`)?.content, 'backup'), content });
}

export function applyBackupRecipe(payload) {
  const name = backupRequired(payload?.name || payload?.recipe_name, 'recipe name');
  const version = backupRequired(payload?.version || payload?.recipe_version, 'recipe version');
  const repositoryId = backupRequired(payload?.repository_id, 'repository id');
  const backend = backupRequired(payload?.backend, 'recipe backend');
  const targetRef = backupRequired(payload?.target_ref || payload?.target, 'target ref');
  const existing = backupRecipes.find(row => row.name === name && row.version === version);
  const content = {
    ...withEntityId({ ...payload, id: payload?.id || existing?.id }),
    name,
    version,
    backend,
    repository_id: repositoryId,
    target_ref: targetRef,
    verification_mode: String(payload?.verification_mode || 'none').trim() || 'none',
    metadata: backupMetadata('web.backup.recipes.apply', payload?.metadata)
  };
  return publishIntent({ domain: 'backup', op: 'recipe-apply', coordinate: `backup-recipe:${content.id}`,
    orgId: intentOrgId(content, canonicalIntentRecord(`backup-recipe:${content.id}`)?.content, 'backup'), content });
}

export function applyBackupDefinition(payload) {
  const name = backupRequired(payload?.name || payload?.definition, 'definition name');
  const repositoryId = backupRequired(payload?.repository_id, 'repository id');
  const policyId = backupRequired(payload?.policy_id, 'policy id');
  const recipeId = backupRequired(payload?.recipe_id, 'recipe id');
  const existing = backupDefinitions.find(row => row.name === name);
  const content = {
    ...withEntityId({ ...payload, id: payload?.id || existing?.id }),
    name,
    repository_id: repositoryId,
    policy_id: policyId,
    recipe_id: recipeId,
    schedule_enabled: Boolean(payload?.schedule_enabled),
    requires_approval: Boolean(payload?.requires_approval),
    metadata: backupMetadata('web.backup.definitions.apply', payload?.metadata)
  };
  return publishIntent({ domain: 'backup', op: 'definition-apply', coordinate: `backup-definition:${content.id}`,
    orgId: intentOrgId(content, canonicalIntentRecord(`backup-definition:${content.id}`)?.content, 'backup'), content });
}

export function requestBackupRun(recipeOrDefinition) {
  const recipeId = backupRequired(recipeOrDefinition?.recipe_id || recipeOrDefinition?.id, 'recipe id');
  const recipe = backupRecipes.find(row => row.id === recipeId) || recipeOrDefinition;
  const content = {
    id: mintEntityId(), recipe_id: recipeId,
    repository_id: backupRequired(recipe.repository_id, 'repository id'),
    ...(recipe.policy_id ? { policy_id: recipe.policy_id } : {}),
    backend: backupRequired(recipe.backend, 'backup backend'),
    target_ref: backupRequired(recipe.target_ref, 'target ref'),
    verification_mode: recipe.verification_mode || 'none',
    metadata: { source: 'web.backup.run' }
  };
  return publishIntent({ domain: 'backup', op: 'run', coordinate: `backup-run:${content.id}`,
    orgId: intentOrgId(content, canonicalIntentRecord(`backup-run:${content.id}`)?.content, 'backup'), content });
}

export function requestBackupVerification(run, mode = '') {
  const backupRunId = backupRequired(run?.id || run?.backup_run_id || run?.run_id, 'backup run id');
  const verificationMode = String(mode || run?.verification_mode || 'kopia_snapshot_verify').trim() || 'kopia_snapshot_verify';
  const content = { id: mintEntityId(), backup_run_id: backupRunId, mode: verificationMode,
    status: 'pending', verified: false };
  return publishIntent({ domain: 'backup', op: 'verification', coordinate: `backup-verification:${content.id}`,
    orgId: intentOrgId(content, canonicalIntentRecord(`backup-verification:${content.id}`)?.content, 'backup'), content });
}

export function requestBackupRestore(run, restoreTargetRef) {
  const backupRunId = backupRequired(run?.id || run?.backup_run_id || run?.run_id, 'backup run id');
  const target = backupRequired(restoreTargetRef || run?.restore_target_ref || run?.target_ref, 'restore target');
  const content = { id: mintEntityId(), backup_run_id: backupRunId,
    restore_target_ref: target, metadata: { source: 'web.backup.restore' } };
  return publishIntent({ domain: 'backup', op: 'restore', coordinate: `backup-restore:${content.id}`,
    orgId: intentOrgId(content, canonicalIntentRecord(`backup-restore:${content.id}`)?.content, 'backup'), content });
}

export function requestBackupRetention(input) {
  const repositoryId = backupRequired(input?.repository_id || input?.id, 'repository id');
  const repository = backupRepositories.find(row => row.id === repositoryId) || input;
  const policyId = input?.policy_id || '';
  const dryRun = Boolean(input?.dry_run);
  const content = { id: mintEntityId(), repository_id: repositoryId,
    ...(policyId ? { policy_id: policyId } : {}),
    backend: backupRequired(repository.backend, 'repository backend'), dry_run: dryRun,
    metadata: { source: 'web.backup.retention' } };
  return publishIntent({ domain: 'backup', op: 'retention', coordinate: `backup-retention:${content.id}`,
    orgId: intentOrgId(content, canonicalIntentRecord(`backup-retention:${content.id}`)?.content, 'backup'), content });
}

export function probeBackupRepository(repository) {
  const repositoryId = repository?.id || repository?.repository_id || '';
  if (!repositoryId) throw new Error('repository id is required');
  const content = { repository_id: repositoryId, repository: repository?.name || '',
    metadata: { source: 'web.backup.repositories' } };
  return publishIntent({ domain: 'backup', op: 'repository-probe',
    coordinate: `backup-repository-probe:${mintEntityId()}`, orgId: intentOrgId(content, null, 'backup'), content });
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
