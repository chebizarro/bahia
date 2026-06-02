import {
  BAHIA_AUDIT_KINDS,
  BAHIA_READ_MODEL_KINDS,
  BAHIA_SBOM_KINDS,
  BAHIA_STATE_SCHEMAS,
  BAHIA_STATUS_KINDS,
  CASCADIA_CONTROLPLANE_STATE,
  LOOM_WORKER_ADVERTISEMENT,
  parseJsonContent
} from '../../nostr/client.js';
import { controlplaneConnection } from './connection.svelte.js';
import { applyServiceEvent } from '../collections/services.svelte.js';
import { applyEnvironmentEvent } from '../collections/environments.svelte.js';
import { deploymentApplicators } from '../collections/deployments.svelte.js';
import {
  applyWorkerEvent,
  applyWorkerStateEvent,
  workerApplicators
} from '../collections/workers.svelte.js';
import { backupApplicators } from '../collections/backup.svelte.js';
import { mlApplicators } from '../collections/ml.svelte.js';
import { applyActivityEvent } from '../collections/activity.svelte.js';
import { refreshCollections, schedulePersistCachedCollections } from '../collections/index.svelte.js';

const ACTIVITY_BACKFILL_LIMIT = 100;
const READ_MODEL_LIMIT = 1000;
const ACTIVITY_BACKFILL_SECONDS = 7 * 24 * 60 * 60;
const CANONICAL_READ_MODEL_KINDS = BAHIA_READ_MODEL_KINDS;
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
    { kinds: [LOOM_WORKER_ADVERTISEMENT], limit: READ_MODEL_LIMIT },
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

function semanticRoute(event) {
  if (event?.kind === CASCADIA_CONTROLPLANE_STATE) return eventSchema(event);
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
  [BAHIA_STATE_SCHEMAS.WORKER_STATE, applyWorkerStateEvent],
  [BAHIA_STATE_SCHEMAS.WORKER_ASSIGNMENT_STATE, workerApplicators.assignment],
  [BAHIA_STATE_SCHEMAS.WORKER_DRAIN_STATUS, workerApplicators.drainStatus],
  [BAHIA_STATE_SCHEMAS.WORKER_ELIGIBILITY_PREVIEW, workerApplicators.eligibilityPreview],
  [BAHIA_STATE_SCHEMAS.BACKUP_DEFINITION_REGISTRY, backupApplicators.definition],
  [BAHIA_STATE_SCHEMAS.BACKUP_POLICY_REGISTRY, backupApplicators.policy],
  [BAHIA_STATE_SCHEMAS.BACKUP_REPOSITORY_REGISTRY, backupApplicators.repository],
  [BAHIA_STATE_SCHEMAS.BACKUP_RETENTION_REGISTRY, backupApplicators.retention],
  [BAHIA_STATE_SCHEMAS.BACKUP_RECIPE_REGISTRY, backupApplicators.recipe],
  [BAHIA_STATE_SCHEMAS.BACKUP_RUN_STATE, backupApplicators.run],
  [BAHIA_STATE_SCHEMAS.BACKUP_VERIFICATION_STATE, backupApplicators.verification],
  [BAHIA_STATE_SCHEMAS.BACKUP_RESTORE_STATE, backupApplicators.restore],
  [BAHIA_STATE_SCHEMAS.BACKUP_RUNTIME_OBSERVATION_STATE, backupApplicators.runtimeObservation],
  [BAHIA_STATE_SCHEMAS.ML_MODEL_REGISTRY, mlApplicators.model],
  [BAHIA_STATE_SCHEMAS.ML_MODEL_VERSION_REGISTRY, mlApplicators.modelVersion],
  [BAHIA_STATE_SCHEMAS.ML_INFERENCE_ENDPOINT_REGISTRY, mlApplicators.endpoint],
  [BAHIA_STATE_SCHEMAS.ML_INFERENCE_ENDPOINT_STATE, mlApplicators.endpointState]
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
    : applyActivityEvent(event);

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
