import { goto } from '$app/navigation';
import { parseJsonContent } from '$lib/nostr/client.js';
import { mintEntityId, withEntityId } from '$lib/entity-id.js';
import { publishIntent, publishIntentForStatus, canonicalIntentRecord, resolveIntentOrgId } from '$lib/nostr/intent-client.svelte.js';
import { artifactRegisterIntent, observedArtifactImportIntent, adoptionImportIntent,
  deploymentPreviewIntent, deploymentRouteAttachIntent, policyEvaluateIntent } from '$lib/nostr/last-ops-intents.js';
import { adoptionScanIntent, sbomIntent } from '$lib/nostr/final-ops-intents.js';
import { orgRoles } from './auth-roles.svelte.js';
import { orgsState } from './orgs.svelte.js';
import { currentSystemInfo } from './system.svelte.js';
import { backupRecipes, backupRepositories, backupPolicies, backupDefinitions } from './collections/backup.svelte.js';
import { services } from './collections/services.svelte.js';
import { deploymentIntents, llmRoutes, llmRouteStates } from './collections/deployments.svelte.js';
import { deploymentIntentRequest, runtimeIntentRequest, llmLifecycleIntentRequest, backupRestoreApprovalIntentRequest } from '$lib/nostr/domain-intents.js';

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
  const status = await publishIntentForStatus(deploymentPreviewIntent(payload,
    intentOrgId({ ...payload, org_id: payload.org_id || serviceOrgId(payload.service_id) }, null, 'deployment')));
  if (!status?.data) throw new Error('Accepted deployment preview has no plan data');
  return status.data;
}

function serviceOrgId(serviceId) {
  return services.find(service => service.id === serviceId)?.org_id;
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
  return publishIntent(deploymentIntentRequest('create', content,
    intentOrgId({ ...content, org_id: serviceOrgId(serviceId) }, null, 'deployment')));
}

export function rollbackDeployment(payload) {
  if (!payload?.target_artifact_id) {
    return Promise.reject(new Error('Rollback requires an explicit artifact target from deployment history.'));
  }
  return publishIntent(deploymentIntentRequest('rollback', payload,
    intentOrgId({ ...payload, org_id: serviceOrgId(payload.service_id) }, null, 'deployment')));
}

function deploymentDecision(id, op) {
  const current = deploymentIntents.find(intent => intent.id === id);
  if (!current?.updated_at) throw new Error('Current deployment revision is unavailable; re-read and resubmit');
  return publishIntent(deploymentIntentRequest(op,
    { deployment_intent_id: id, expected_updated_at: current.updated_at },
    intentOrgId({ ...current, org_id: current.org_id || serviceOrgId(current.service_id) }, null, 'deployment')));
}

export function approveDeploymentIntent(id) { return deploymentDecision(id, 'approve'); }
export function rejectDeploymentIntent(id) { return deploymentDecision(id, 'reject'); }

export function requestRuntimeAction(op, payload) {
  return publishIntent(runtimeIntentRequest(op, payload,
    intentOrgId({ ...payload, org_id: serviceOrgId(payload.service_id) }, null, 'runtime')));
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

function llmOrgId(routeId) {
  return llmRoutes.find(route => route.id === routeId || route.route_id === routeId)?.org_id;
}

export function requestLLMDeploy(payload) {
  return publishIntent(llmLifecycleIntentRequest('deploy', payload,
    intentOrgId({ ...payload, org_id: llmOrgId(payload.route_id) }, null, 'llm')));
}

export function requestLLMRollback(payload) {
  return publishIntent(llmLifecycleIntentRequest('rollback', payload,
    intentOrgId({ ...payload, org_id: llmOrgId(payload.route_id) }, null, 'llm')));
}

function decideLLMDeployment(id, op) {
  const state = llmRouteStates.find(row => row.desired_intent_id === id);
  const current = canonicalIntentRecord(id)?.content;
  const updatedAt = current?.updated_at || state?.desired_intent_updated_at;
  return publishIntent(llmLifecycleIntentRequest(op,
    { deployment_intent_id: id, ...(updatedAt ? { expected_updated_at: updatedAt } : {}) },
    intentOrgId({ org_id: llmOrgId(state?.route_id) }, current, 'llm')));
}

export function approveLLMDeploymentIntent(id) { return decideLLMDeployment(id, 'approve'); }
export function rejectLLMDeploymentIntent(id) { return decideLLMDeployment(id, 'reject'); }

export function registerArtifact(payload) {
  return publishIntent(artifactRegisterIntent(payload,
    intentOrgId({ ...payload, org_id: payload.org_id || serviceOrgId(payload.service_id) }, null, 'artifact')));
}

export function importObservedArtifact(payload) {
  return publishIntent(observedArtifactImportIntent(payload,
    intentOrgId({ ...payload, org_id: payload.org_id || serviceOrgId(payload.service_id) }, null, 'artifact')));
}

export function importAdoption(payload) {
  const orgId = intentOrgId(payload, null, 'adoption');
  return publishIntent(adoptionImportIntent(payload, orgId));
}

export async function scanAdoption(payload) {
  const orgId = intentOrgId(payload, null, 'adoption');
  const status = await publishIntentForStatus(adoptionScanIntent(payload, orgId));
  if (!Array.isArray(status.data?.findings)) throw new Error('Accepted adoption scan has no findings data');
  return status.data;
}

export function attachDeploymentRoute(payload) {
  return publishIntent(deploymentRouteAttachIntent(payload,
    intentOrgId({ ...payload, org_id: payload.org_id || serviceOrgId(payload.service_id) }, null, 'deployment')));
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

// Inline SBOM data is base64-encoded in one relay event; keep room for the
// signed intent envelope below the relay's 512 KiB message limit.
export const MAX_INLINE_SBOM_BYTES = 360 * 1024;

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

export function inlineSBOMLimitMessage() {
  return `Inline SBOM imports are limited to ${MAX_INLINE_SBOM_BYTES} bytes (360 KiB); upload larger SBOM files to Blossom and import them by location.`;
}

export function generateArtifactSBOM(artifact, { formats = ['spdx', 'cyclonedx'], generator = 'syft' } = {}) {
  const artifactId = String(artifact?.id || '').trim();
  if (!artifactId) throw new Error('artifact id is required');
  const digest = artifactDigest(artifact);
  if (!digest) throw new Error('artifact digest is required');
  const locator = artifactImageLocator(artifact, digest);
  if (!locator) throw new Error('artifact image repository or OCI image ref is required');
  const normalizedFormats = Array.from(new Set((Array.isArray(formats) ? formats : [formats]).map((format) => String(format || '').trim()).filter(Boolean)));
  if (normalizedFormats.length === 0) throw new Error('at least one SBOM format is required');
  const generatorId = String(generator || 'syft').trim() || 'syft';
  return publishIntent(sbomIntent('generate', {
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
    }, intentOrgId(artifact, null, 'sbom')));
}

export function importArtifactSBOM(artifact, { format = 'spdx', payloadBase64 = '', location = null, storage = '', generator = { id: 'import' } } = {}) {
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
  if (hasInlinePayload && decodedBase64Length(inlinePayload) > MAX_INLINE_SBOM_BYTES) {
    throw new Error(inlineSBOMLimitMessage());
  }
  const generatorInfo = normalizeSBOMGenerator(generator, 'import');
  const storageType = String(storage || normalizedLocation?.type || 'blossom').trim() || 'blossom';
  return publishIntent(sbomIntent('import', {
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
    }, intentOrgId(artifact, null, 'sbom')));
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
  const status = await publishIntentForStatus(policyEvaluateIntent(payload, intentOrgId(payload, null, 'policy')));
  if (!status?.evaluation) throw new Error('Accepted policy evaluation has no evaluation data');
  return status.evaluation;
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
  return publishIntent(backupRestoreApprovalIntentRequest(restoreId, approved ? 'approve' : 'reject', message,
    intentOrgId(restore, null, 'backup')));
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
