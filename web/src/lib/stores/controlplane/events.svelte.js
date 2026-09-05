import {
  BAHIA_AUDIT_KINDS,
  BAHIA_READ_MODEL_KINDS,
  BAHIA_SBOM_KINDS,
  BAHIA_STATE_SCHEMAS,
  BAHIA_STATUS_KINDS,
  CASCADIA_CONTROLPLANE_STATE,
  LOOM_WORKER_ADVERTISEMENT,
  LOOM_JOB_REQUEST,
  LOOM_JOB_STATUS_UPDATE,
  LOOM_JOB_RESULT,
  parseJsonContent
} from '../../nostr/client.js';
import { controlplaneConnection } from './connection.svelte.js';
import { applyServiceEvent } from '../collections/services.svelte.js';
import { applyEnvironmentEvent } from '../collections/environments.svelte.js';
import { deploymentApplicators } from '../collections/deployments.svelte.js';
import {
  applyWorkerEvent,
  applyWorkerStateEvent,
  applyLoomJobRequestEvent,
  applyLoomJobStatusEvent,
  applyLoomJobResultEvent,
  workerApplicators
} from '../collections/workers.svelte.js';
import {
  BACKUP_ATTESTATION_KINDS,
  applyBackupAttestationEvent,
  backupApplicators
} from '../collections/backup.svelte.js';
import { mlApplicators } from '../collections/ml.svelte.js';
import { applyActivityEvent } from '../collections/activity.svelte.js';
import {
  CANONICAL_OPERATION_KINDS,
  EXTERNAL_OPERATION_KINDS,
  HIVE_CI_OPERATION_KINDS,
  OPERATION_REQUEST_KINDS,
  OPERATION_RESULT_KINDS,
  OPERATION_STATUS_KINDS,
  applyHiveCIWorkflowResultEvent,
  applyHiveCIWorkflowRunEvent,
  applyOperationRequestEvent,
  applyOperationResultEvent,
  applyOperationStatusEvent
} from '../collections/operations.svelte.js';
import { applySBOMReferenceEvent, applySBOMAvailabilityEvent } from '../collections/sbom.svelte.js';
import { refreshCollections, schedulePersistCachedCollections } from '../collections/index.svelte.js';

const ACTIVITY_BACKFILL_LIMIT = 100;
const READ_MODEL_LIMIT = 1000;
const ACTIVITY_BACKFILL_SECONDS = 7 * 24 * 60 * 60;
const LOOM_JOB_BACKFILL_SECONDS = 7 * 24 * 60 * 60;
const LOOM_JOB_LIMIT = 500;
const OPERATION_BACKFILL_SECONDS = 7 * 24 * 60 * 60;
const OPERATION_LIMIT = 1000;
const LOOM_JOB_KINDS = [LOOM_JOB_REQUEST, LOOM_JOB_STATUS_UPDATE, LOOM_JOB_RESULT];
const CANONICAL_READ_MODEL_KINDS = BAHIA_READ_MODEL_KINDS;
const ACTIVITY_KINDS = [...BAHIA_AUDIT_KINDS, ...BAHIA_STATUS_KINDS, ...BAHIA_SBOM_KINDS];
const CP_STATE_SCHEMA = 'bahia.cp-state.v1';

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
    { kinds: [LOOM_WORKER_ADVERTISEMENT], limit: READ_MODEL_LIMIT },
    {
      kinds: LOOM_JOB_KINDS,
      since: Math.floor(Date.now() / 1000) - LOOM_JOB_BACKFILL_SECONDS,
      limit: LOOM_JOB_LIMIT
    },
    {
      kinds: CANONICAL_OPERATION_KINDS,
      since: Math.floor(Date.now() / 1000) - OPERATION_BACKFILL_SECONDS,
      limit: OPERATION_LIMIT,
      ...authorFilter
    },
    {
      kinds: BACKUP_ATTESTATION_KINDS,
      since: Math.floor(Date.now() / 1000) - OPERATION_BACKFILL_SECONDS,
      limit: OPERATION_LIMIT,
      ...authorFilter
    },
    {
      kinds: EXTERNAL_OPERATION_KINDS,
      since: Math.floor(Date.now() / 1000) - OPERATION_BACKFILL_SECONDS,
      limit: OPERATION_LIMIT
    },
    {
      kinds: ACTIVITY_KINDS,
      since: Math.floor(Date.now() / 1000) - ACTIVITY_BACKFILL_SECONDS,
      limit: ACTIVITY_BACKFILL_LIMIT,
      ...authorFilter
    }
  ];
}

function isCanonicalBahiaKind(kind) {
  return CANONICAL_READ_MODEL_KINDS.includes(kind)
    || ACTIVITY_KINDS.includes(kind)
    || CANONICAL_OPERATION_KINDS.includes(kind)
    || BACKUP_ATTESTATION_KINDS.includes(kind);
}

function shouldAcceptControlplaneEvent(event) {
  const servicePubkey = controlplaneConnection.servicePubkey;
  if (!servicePubkey || !isCanonicalBahiaKind(event.kind)) return true;
  return event.pubkey === servicePubkey;
}

function firstTagValue(event, name) {
  for (const tag of event?.tags || []) {
    if (Array.isArray(tag) && tag.length >= 2 && tag[0] === name) return tag[1];
  }
  return '';
}

function eventSchema(event) {
  return firstTagValue(event, 'schema') || parseJsonContent(event, {})?.schema || '';
}

function eventDomain(event) {
  return firstTagValue(event, 'domain') || parseJsonContent(event, {})?.domain || '';
}

function eventLegacyKind(event) {
  return firstTagValue(event, 'legacy_kind') || '';
}

const legacyKindSchemaRoutes = new Map([
  ['31961', BAHIA_STATE_SCHEMAS.SERVICE_STATE],
  ['31962', BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY],
  ['31963', BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY],
  ['31964', BAHIA_STATE_SCHEMAS.LLM_ROUTE_REGISTRY],
  ['31965', BAHIA_STATE_SCHEMAS.LLM_ROUTE_STATE],
  ['31966', BAHIA_STATE_SCHEMAS.ARTIFACT_REGISTRY],
  ['31967', BAHIA_STATE_SCHEMAS.DEPLOYMENT_INTENT_REGISTRY],
  ['31968', BAHIA_STATE_SCHEMAS.DEPLOYMENT_RUN_REGISTRY],
  ['31969', BAHIA_STATE_SCHEMAS.BUILD_REGISTRY],
  ['31975', BAHIA_STATE_SCHEMAS.DNS_ZONE_STATE],
  ['31976', BAHIA_STATE_SCHEMAS.DNS_ENDPOINT_STATE],
  ['31977', BAHIA_STATE_SCHEMAS.DNS_POLICY_STATE],
  ['31978', BAHIA_STATE_SCHEMAS.DNS_BACKEND_STATE],
  ['31980', BAHIA_STATE_SCHEMAS.ML_MODEL_REGISTRY],
  ['31981', BAHIA_STATE_SCHEMAS.ML_MODEL_VERSION_REGISTRY],
  ['31982', BAHIA_STATE_SCHEMAS.ML_DATASET_REGISTRY],
  ['31983', BAHIA_STATE_SCHEMAS.ML_RECIPE_REGISTRY],
  ['31984', BAHIA_STATE_SCHEMAS.ML_RECIPE_RUN_STATE],
  ['31985', BAHIA_STATE_SCHEMAS.ML_INFERENCE_ENDPOINT_REGISTRY],
  ['31986', BAHIA_STATE_SCHEMAS.ML_INFERENCE_ENDPOINT_STATE],
  ['31987', BAHIA_STATE_SCHEMAS.ML_EVALUATION_EXPERIMENT_STATE],
  ['31988', BAHIA_STATE_SCHEMAS.ML_ARTIFACT_PROVENANCE_GRAPH],
  ['31989', BAHIA_STATE_SCHEMAS.ML_RUNTIME_CAPABILITY_PROFILE],
  ['31990', BAHIA_STATE_SCHEMAS.ASSISTANT_SESSION],
  ['31991', BAHIA_STATE_SCHEMAS.BACKUP_DEFINITION_REGISTRY],
  ['31992', BAHIA_STATE_SCHEMAS.BACKUP_POLICY_REGISTRY],
  ['31993', BAHIA_STATE_SCHEMAS.BACKUP_REPOSITORY_REGISTRY],
  ['31994', BAHIA_STATE_SCHEMAS.BACKUP_RETENTION_REGISTRY],
  ['31995', BAHIA_STATE_SCHEMAS.BACKUP_RECIPE_REGISTRY],
  ['31996', BAHIA_STATE_SCHEMAS.BACKUP_RUN_STATE],
  ['31997', BAHIA_STATE_SCHEMAS.BACKUP_VERIFICATION_STATE],
  ['31998', BAHIA_STATE_SCHEMAS.BACKUP_RESTORE_STATE],
  ['31999', BAHIA_STATE_SCHEMAS.BACKUP_RUNTIME_OBSERVATION_STATE],
]);

function semanticRoute(event) {
  if (event?.kind === CASCADIA_CONTROLPLANE_STATE) {
    const schema = eventSchema(event);
    if (schema !== CP_STATE_SCHEMA) return schema;
    return legacyKindSchemaRoutes.get(eventLegacyKind(event)) || schema;
  }
  return event?.kind;
}

export function resetEventRouting() {
  replaceableEvents.clear();
  seenEventIds.clear();
}

const handlers = new Map([
  [BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY, applyServiceEvent],
  [BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY, applyEnvironmentEvent],
  [BAHIA_STATE_SCHEMAS.SERVICE_STATE, deploymentApplicators.serviceState],
  [BAHIA_STATE_SCHEMAS.LLM_ROUTE_REGISTRY, deploymentApplicators.llmRoute],
  [BAHIA_STATE_SCHEMAS.LLM_ROUTE_STATE, deploymentApplicators.llmRouteState],
  [BAHIA_STATE_SCHEMAS.ARTIFACT_REGISTRY, deploymentApplicators.artifact],
  [BAHIA_STATE_SCHEMAS.BUILD_REGISTRY, deploymentApplicators.build],
  [BAHIA_STATE_SCHEMAS.DEPLOYMENT_INTENT_REGISTRY, deploymentApplicators.intent],
  [BAHIA_STATE_SCHEMAS.DEPLOYMENT_RUN_REGISTRY, deploymentApplicators.run],
  [BAHIA_STATE_SCHEMAS.POLICY_REGISTRY, deploymentApplicators.policy],
  [BAHIA_STATE_SCHEMAS.PACKAGE_REPOSITORY_REGISTRY, deploymentApplicators.packageRepository],
  [BAHIA_STATE_SCHEMAS.PACKAGE_ARTIFACT_REGISTRY, deploymentApplicators.packageArtifact],
  [BAHIA_STATE_SCHEMAS.PACKAGE_PROMOTION_REGISTRY, deploymentApplicators.packagePromotion],
  [LOOM_WORKER_ADVERTISEMENT, applyWorkerEvent],
  [LOOM_JOB_REQUEST, applyLoomJobRequestEvent],
  [LOOM_JOB_STATUS_UPDATE, applyLoomJobStatusEvent],
  [LOOM_JOB_RESULT, applyLoomJobResultEvent],
  ...OPERATION_REQUEST_KINDS.map((kind) => [kind, applyOperationRequestEvent]),
  ...OPERATION_STATUS_KINDS.map((kind) => [kind, applyOperationStatusEvent]),
  ...OPERATION_RESULT_KINDS.map((kind) => [kind, applyOperationResultEvent]),
  [HIVE_CI_OPERATION_KINDS[0], applyHiveCIWorkflowRunEvent],
  [HIVE_CI_OPERATION_KINDS[1], applyHiveCIWorkflowResultEvent],
  [BAHIA_STATE_SCHEMAS.WORKER_STATE, applyWorkerStateEvent],
  [BAHIA_STATE_SCHEMAS.WORKER_ASSIGNMENT_STATE, workerApplicators.assignment],
  [BAHIA_STATE_SCHEMAS.WORKER_DRAIN_STATUS, workerApplicators.drainStatus],
  [BAHIA_STATE_SCHEMAS.WORKER_ELIGIBILITY_PREVIEW, workerApplicators.eligibilityPreview],
  [BAHIA_STATE_SCHEMAS.WORKER_CLEANUP_EXECUTION, workerApplicators.cleanupExecution],
  [BAHIA_STATE_SCHEMAS.BACKUP_DEFINITION_REGISTRY, backupApplicators.definition],
  [BAHIA_STATE_SCHEMAS.BACKUP_POLICY_REGISTRY, backupApplicators.policy],
  [BAHIA_STATE_SCHEMAS.BACKUP_REPOSITORY_REGISTRY, backupApplicators.repository],
  [BAHIA_STATE_SCHEMAS.BACKUP_RETENTION_REGISTRY, backupApplicators.retention],
  [BAHIA_STATE_SCHEMAS.BACKUP_RECIPE_REGISTRY, backupApplicators.recipe],
  [BAHIA_STATE_SCHEMAS.BACKUP_RUN_STATE, backupApplicators.run],
  [BAHIA_STATE_SCHEMAS.BACKUP_VERIFICATION_STATE, backupApplicators.verification],
  [BAHIA_STATE_SCHEMAS.BACKUP_RESTORE_STATE, backupApplicators.restore],
  [BAHIA_STATE_SCHEMAS.BACKUP_RUNTIME_OBSERVATION_STATE, backupApplicators.runtimeObservation],
  ...BACKUP_ATTESTATION_KINDS.map((kind) => [kind, applyBackupAttestationEvent]),
  [BAHIA_STATE_SCHEMAS.ML_MODEL_REGISTRY, mlApplicators.model],
  [BAHIA_STATE_SCHEMAS.ML_MODEL_VERSION_REGISTRY, mlApplicators.modelVersion],
  [BAHIA_STATE_SCHEMAS.ML_INFERENCE_ENDPOINT_REGISTRY, mlApplicators.endpoint],
  [BAHIA_STATE_SCHEMAS.ML_INFERENCE_ENDPOINT_STATE, mlApplicators.endpointState],
  [30078, (event, replaceableEvents) => {
    const sbomChanged = applySBOMReferenceEvent(event);
    const activityChanged = applyActivityEvent(event);
    return sbomChanged || activityChanged;
  }],
  [30004, (event, replaceableEvents) => {
    const sbomChanged = applySBOMAvailabilityEvent(event);
    const activityChanged = applyActivityEvent(event);
    return sbomChanged || activityChanged;
  }]
]);

export function applyControlplaneEvent(event) {
  if (!event?.id || typeof event.kind !== 'number') return false;
  if (!shouldAcceptControlplaneEvent(event)) return false;
  if (seenEventIds.has(event.id)) return false;
  seenEventIds.add(event.id);

  const route = semanticRoute(event);
  const handler = handlers.get(route);
  const changed = handler
    ? handler(event, replaceableEvents)
    : (ACTIVITY_KINDS.includes(event.kind) ? applyActivityEvent(event) : false);

  if (changed) {
    controlplaneConnection.lastEventAt = new Date().toISOString();
    refreshCollections();
    schedulePersistCachedCollections();
  }
  return changed;
}

export const controlplaneEventRouting = Object.freeze({
  schemas: BAHIA_STATE_SCHEMAS,
  routeFor: (event) => ({ domain: eventDomain(event), schema: eventSchema(event) })
});
