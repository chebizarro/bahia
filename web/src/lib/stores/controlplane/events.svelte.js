import {
  KINDS,
  BAHIA_READ_MODEL_KINDS,
  BAHIA_STATUS_KINDS,
  BAHIA_AUDIT_KINDS,
  BAHIA_SBOM_KINDS
} from '../../nostr/client.js';
import { controlplaneConnection } from './connection.svelte.js';
import { applyServiceEvent } from '../collections/services.svelte.js';
import { applyEnvironmentEvent } from '../collections/environments.svelte.js';
import { deploymentApplicators } from '../collections/deployments.svelte.js';
import {
  applyWorkerEvent,
  applyWorkerStateEvent,
  hasWorkerEligibilityPreviewShape,
  hasWorkerReadModelTag,
  workerApplicators
} from '../collections/workers.svelte.js';
import {
  backupApplicators,
  hasBackupDefinitionShape,
  hasBackupPolicyShape,
  hasBackupRepositoryShape
} from '../collections/backup.svelte.js';
import { mlApplicators } from '../collections/ml.svelte.js';
import { applyActivityEvent } from '../collections/activity.svelte.js';
import { refreshCollections } from '../collections/index.svelte.js';

const ACTIVITY_BACKFILL_LIMIT = 100;
const READ_MODEL_LIMIT = 1000;
const ACTIVITY_BACKFILL_SECONDS = 7 * 24 * 60 * 60;
const CANONICAL_READ_MODEL_KINDS = BAHIA_READ_MODEL_KINDS.filter((kind) => kind !== KINDS.LOOM_WORKER_AD);
const ACTIVITY_KINDS = [...BAHIA_AUDIT_KINDS, ...BAHIA_STATUS_KINDS, ...BAHIA_SBOM_KINDS];

const replaceableEvents = new Map();
const seenEventIds = new Set();

function canonicalAuthorFilter() {
  const servicePubkey = controlplaneConnection.servicePubkey;
  return servicePubkey ? { authors: [servicePubkey] } : {};
}

export function readModelFilters() {
  const authorFilter = canonicalAuthorFilter();
  return [
    { kinds: CANONICAL_READ_MODEL_KINDS, limit: READ_MODEL_LIMIT, ...authorFilter },
    { kinds: [KINDS.LOOM_WORKER_AD], limit: READ_MODEL_LIMIT },
    {
      kinds: ACTIVITY_KINDS,
      since: Math.floor(Date.now() / 1000) - ACTIVITY_BACKFILL_SECONDS,
      limit: ACTIVITY_BACKFILL_LIMIT,
      ...authorFilter
    }
  ];
}

function isCanonicalBahiaKind(kind) {
  return CANONICAL_READ_MODEL_KINDS.includes(kind) || ACTIVITY_KINDS.includes(kind);
}

function shouldAcceptControlplaneEvent(event) {
  const servicePubkey = controlplaneConnection.servicePubkey;
  if (!servicePubkey || !isCanonicalBahiaKind(event.kind)) return true;
  return event.pubkey === servicePubkey;
}

export function resetEventRouting() {
  replaceableEvents.clear();
  seenEventIds.clear();
}

function applyLegacyAssignment(event) {
  return hasBackupDefinitionShape(event)
    ? backupApplicators.definition(event, replaceableEvents)
    : (hasWorkerReadModelTag(event) ? workerApplicators.assignment(event, replaceableEvents) : false);
}

function applyLegacyDrainStatus(event) {
  return hasBackupPolicyShape(event)
    ? backupApplicators.policy(event, replaceableEvents)
    : (hasWorkerReadModelTag(event) ? workerApplicators.drainStatus(event, replaceableEvents) : false);
}

function applyLegacyEligibility(event) {
  return hasBackupRepositoryShape(event)
    ? backupApplicators.repository(event, replaceableEvents)
    : (hasWorkerEligibilityPreviewShape(event) ? workerApplicators.eligibilityPreview(event, replaceableEvents) : false);
}

const handlers = new Map([
  [KINDS.BAHIA_SERVICE_REGISTRY, applyServiceEvent],
  [KINDS.BAHIA_ENVIRONMENT_REGISTRY, applyEnvironmentEvent],
  [KINDS.BAHIA_SERVICE_STATE, deploymentApplicators.serviceState],
  [KINDS.BAHIA_LLM_ROUTE_REGISTRY, deploymentApplicators.llmRoute],
  [KINDS.BAHIA_LLM_ROUTE_STATE, deploymentApplicators.llmRouteState],
  [KINDS.BAHIA_ARTIFACT_REGISTRY, deploymentApplicators.artifact],
  [KINDS.BAHIA_BUILD_REGISTRY, deploymentApplicators.build],
  [KINDS.BAHIA_DEPLOYMENT_INTENT_REGISTRY, deploymentApplicators.intent],
  [KINDS.BAHIA_DEPLOYMENT_RUN_REGISTRY, deploymentApplicators.run],
  [KINDS.BAHIA_POLICY_REGISTRY, deploymentApplicators.policy],
  [KINDS.BAHIA_PACKAGE_REPOSITORY_REGISTRY, deploymentApplicators.packageRepository],
  [KINDS.BAHIA_PACKAGE_ARTIFACT_REGISTRY, deploymentApplicators.packageArtifact],
  [KINDS.BAHIA_PACKAGE_PROMOTION_REGISTRY, deploymentApplicators.packagePromotion],
  [KINDS.LOOM_WORKER_AD, applyWorkerEvent],
  [KINDS.BAHIA_WORKER_STATE, applyWorkerStateEvent],
  [KINDS.BAHIA_WORKER_ASSIGNMENT_STATE, workerApplicators.assignment],
  [KINDS.BAHIA_WORKER_DRAIN_STATUS, workerApplicators.drainStatus],
  [KINDS.BAHIA_WORKER_ELIGIBILITY_PREVIEW, workerApplicators.eligibilityPreview],
  [KINDS.BAHIA_BACKUP_RETENTION_REGISTRY, backupApplicators.retention],
  [KINDS.BAHIA_BACKUP_RECIPE_REGISTRY, backupApplicators.recipe],
  [KINDS.BAHIA_BACKUP_RUN_STATE, backupApplicators.run],
  [KINDS.BAHIA_BACKUP_VERIFICATION_STATE, backupApplicators.verification],
  [KINDS.BAHIA_BACKUP_RESTORE_STATE, backupApplicators.restore],
  [KINDS.BAHIA_BACKUP_RUNTIME_OBSERVATION_STATE, backupApplicators.runtimeObservation],
  [KINDS.BAHIA_ML_MODEL_REGISTRY, mlApplicators.model],
  [KINDS.BAHIA_ML_MODEL_VERSION_REGISTRY, mlApplicators.modelVersion],
  [KINDS.BAHIA_ML_ENDPOINT_REGISTRY, mlApplicators.endpoint],
  [KINDS.BAHIA_ML_ENDPOINT_STATE, mlApplicators.endpointState]
]);

export function applyControlplaneEvent(event) {
  if (!event?.id || typeof event.kind !== 'number') return false;
  if (!shouldAcceptControlplaneEvent(event)) return false;
  if (seenEventIds.has(event.id)) return false;
  seenEventIds.add(event.id);

  let changed = false;
  if (event.kind === KINDS.BAHIA_LEGACY_WORKER_STATE) {
    changed = hasWorkerReadModelTag(event) ? applyWorkerStateEvent(event, replaceableEvents) : false;
  } else if (event.kind === KINDS.BAHIA_LEGACY_WORKER_ASSIGNMENT_STATE) {
    changed = applyLegacyAssignment(event);
  } else if (event.kind === KINDS.BAHIA_LEGACY_WORKER_DRAIN_STATUS) {
    changed = applyLegacyDrainStatus(event);
  } else if (event.kind === KINDS.BAHIA_LEGACY_WORKER_ELIGIBILITY_PREVIEW) {
    changed = applyLegacyEligibility(event);
  } else {
    const handler = handlers.get(event.kind);
    changed = handler ? handler(event, replaceableEvents) : applyActivityEvent(event);
  }

  if (changed) {
    controlplaneConnection.lastEventAt = new Date().toISOString();
    refreshCollections();
  }
  return changed;
}
