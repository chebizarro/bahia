import {
  BAHIA_AUDIT_KINDS,
  BAHIA_CP_STATE_SCHEMA,
  BAHIA_READ_MODEL_KINDS,
  BAHIA_SBOM_KINDS,
  BAHIA_STATE_SCHEMAS,
  BAHIA_STATUS_KINDS,
  CASCADIA_CONTROLPLANE_STATE,
  CP_STATE_TOPIC_BY_SCHEMA,
  SBOM_AVAILABILITY_LIST,
  SBOM_REFERENCE,
  parseJsonContent
} from '../../nostr/client.js';
import { CP_STATE_SCHEMA_BY_LEGACY_KIND } from '../../nostr/cp-state.js';
import { controlplaneConnection } from './connection.svelte.js';
import { deploymentApplicators } from '../collections/deployments.svelte.js';
import {
  BACKUP_ATTESTATION_KINDS,
  applyBackupAttestationEvent,
  backupApplicators
} from '../collections/backup.svelte.js';
import { mlApplicators } from '../collections/ml.svelte.js';
import { applyActivityEvent } from '../collections/activity.svelte.js';
import {
  CANONICAL_OPERATION_KINDS,
} from '../collections/operations.svelte.js';
import { applySBOMReferenceEvent, applySBOMAvailabilityEvent } from '../collections/sbom.svelte.js';
import {
  readCachedControlplaneEvents,
  recordPersistedEvent,
  refreshCollections,
  scheduleRefreshCollections,
  schedulePersistCachedCollections
} from '../collections/index.svelte.js';

const ACTIVITY_BACKFILL_LIMIT = 100;
const READ_MODEL_LIMIT = 1000;
const ACTIVITY_BACKFILL_SECONDS = 7 * 24 * 60 * 60;
const OPERATION_BACKFILL_SECONDS = 7 * 24 * 60 * 60;
const OPERATION_LIMIT = 1000;
const CANONICAL_READ_MODEL_KINDS = BAHIA_READ_MODEL_KINDS;
const NON_STATE_READ_MODEL_KINDS = CANONICAL_READ_MODEL_KINDS.filter((kind) => kind !== CASCADIA_CONTROLPLANE_STATE);
const ACTIVITY_KINDS = [...BAHIA_AUDIT_KINDS, ...BAHIA_STATUS_KINDS, ...BAHIA_SBOM_KINDS];

const replaceableEvents = new Map();
const seenEventIds = new Set();

function canonicalAuthorFilter() {
  const servicePubkey = controlplaneConnection.servicePubkey;
  return servicePubkey ? { authors: [servicePubkey] } : {};
}

// The t topics of the cp-state families this store routes. 30900 carries every
// Bahia state family, so the REQ is scoped by the single-letter topic the
// producers stamp (relays index single-letter tags only; audit A-27), never by
// #domain/#schema.
export function controlplaneStateTopics() {
  const topics = new Set();
  for (const route of [
    ...handlers.keys(),
    BAHIA_STATE_SCHEMAS.SERVICE_REGISTRY,
    BAHIA_STATE_SCHEMAS.ENVIRONMENT_REGISTRY,
    BAHIA_STATE_SCHEMAS.SERVICE_STATE,
    BAHIA_STATE_SCHEMAS.POLICY_REGISTRY,
    BAHIA_STATE_SCHEMAS.PACKAGE_REPOSITORY_REGISTRY,
    BAHIA_STATE_SCHEMAS.PACKAGE_ARTIFACT_REGISTRY,
    BAHIA_STATE_SCHEMAS.PACKAGE_PROMOTION_REGISTRY
  ]) {
    const topic = CP_STATE_TOPIC_BY_SCHEMA[route];
    if (topic) topics.add(topic);
  }
  return [...topics].sort();
}

export function readModelFilters() {
  const authorFilter = canonicalAuthorFilter();
  return [
    { kinds: [CASCADIA_CONTROLPLANE_STATE], '#t': controlplaneStateTopics(), limit: READ_MODEL_LIMIT, ...authorFilter },
    { kinds: NON_STATE_READ_MODEL_KINDS, limit: READ_MODEL_LIMIT, ...authorFilter },
    {
      kinds: BACKUP_ATTESTATION_KINDS,
      since: Math.floor(Date.now() / 1000) - OPERATION_BACKFILL_SECONDS,
      limit: OPERATION_LIMIT,
      ...authorFilter
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

// legacy_kind → family schema, generated from kinds.gen.js (nostr/cp-state.js).
// DNS routes resolve to the DNS family schemas but deliberately have no
// handler here: /dns state is owned by stores/dns.svelte.js, which keeps its
// own topic-scoped subscription and applies the same resolution.
const legacyKindSchemaRoutes = new Map(Object.entries(CP_STATE_SCHEMA_BY_LEGACY_KIND));

function semanticRoute(event) {
  if (event?.kind === CASCADIA_CONTROLPLANE_STATE) {
    const schema = eventSchema(event);
    if (schema !== BAHIA_CP_STATE_SCHEMA) return schema;
    return legacyKindSchemaRoutes.get(eventLegacyKind(event)) || schema;
  }
  return event?.kind;
}

export function resetEventRouting() {
  replaceableEvents.clear();
  seenEventIds.clear();
}

const handlers = new Map([
  [BAHIA_STATE_SCHEMAS.LLM_ROUTE_REGISTRY, deploymentApplicators.llmRoute],
  [BAHIA_STATE_SCHEMAS.LLM_ROUTE_STATE, deploymentApplicators.llmRouteState],
  [BAHIA_STATE_SCHEMAS.ARTIFACT_REGISTRY, deploymentApplicators.artifact],
  [BAHIA_STATE_SCHEMAS.BUILD_REGISTRY, deploymentApplicators.build],
  [BAHIA_STATE_SCHEMAS.DEPLOYMENT_INTENT_REGISTRY, deploymentApplicators.intent],
  [BAHIA_STATE_SCHEMAS.DEPLOYMENT_RUN_REGISTRY, deploymentApplicators.run],
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

// Routes whose events feed a persisted (cached) collection. The cache stores
// these raw events so hydration can replay them through applyControlplaneEvent.
const PERSISTED_ROUTE_COLLECTIONS = new Map([
  [BAHIA_STATE_SCHEMAS.LLM_ROUTE_REGISTRY, 'llmRoutes'],
  [BAHIA_STATE_SCHEMAS.ARTIFACT_REGISTRY, 'artifacts'],
  [BAHIA_STATE_SCHEMAS.DEPLOYMENT_INTENT_REGISTRY, 'deploymentIntents'],
  [BAHIA_STATE_SCHEMAS.BACKUP_REPOSITORY_REGISTRY, 'backupRepositories'],
  [BAHIA_STATE_SCHEMAS.BACKUP_POLICY_REGISTRY, 'backupPolicies'],
  [BAHIA_STATE_SCHEMAS.BACKUP_RECIPE_REGISTRY, 'backupRecipes'],
  [BAHIA_STATE_SCHEMAS.BACKUP_DEFINITION_REGISTRY, 'backupDefinitions'],
  [BAHIA_STATE_SCHEMAS.ML_MODEL_REGISTRY, 'mlModels'],
  [BAHIA_STATE_SCHEMAS.ML_MODEL_VERSION_REGISTRY, 'mlModelVersions'],
  [BAHIA_STATE_SCHEMAS.ML_INFERENCE_ENDPOINT_REGISTRY, 'mlEndpoints'],
  [SBOM_REFERENCE, 'sbomRefs'],
  [SBOM_AVAILABILITY_LIST, 'sbomAvailability']
]);

export const persistedRouteCollections = Object.freeze(Array.from(new Set(PERSISTED_ROUTE_COLLECTIONS.values())));

/**
 * Apply one relay (or cached) event to the backing Maps.
 *
 * - `deferRefresh`: coalesce the collection rebuild into one batched refresh
 *   (used by the streaming subscription; avoids O(n^2) catch-up rebuilds).
 * - `fromCache`: the event is being replayed from the local cache; skip the
 *   per-event refresh, persist and liveness side effects.
 */
export function applyControlplaneEvent(event, { deferRefresh = false, fromCache = false } = {}) {
  if (!event?.id || typeof event.kind !== 'number') return false;
  if (!shouldAcceptControlplaneEvent(event)) return false;
  if (seenEventIds.has(event.id)) return false;
  seenEventIds.add(event.id);

  const route = semanticRoute(event);
  const persistedCollection = PERSISTED_ROUTE_COLLECTIONS.get(route);
  if (persistedCollection) recordPersistedEvent(persistedCollection, event);

  const handler = handlers.get(route);
  const changed = handler
    ? handler(event, replaceableEvents)
    : (ACTIVITY_KINDS.includes(event.kind) ? applyActivityEvent(event) : false);

  if (changed && !fromCache) {
    controlplaneConnection.lastEventAt = new Date().toISOString();
    if (deferRefresh) scheduleRefreshCollections();
    else refreshCollections();
    schedulePersistCachedCollections();
  }
  return changed;
}

/**
 * Hydrate collections from the local event cache by replaying cached events
 * through applyControlplaneEvent, then rebuild once. Because the backing Maps
 * and replaceable index are populated, later relay events merge by the same
 * coordinates and newer-wins rules instead of wiping hydrated state.
 */
export async function hydrateCachedControlplane(options = {}) {
  const cachedEvents = await readCachedControlplaneEvents(options);
  if (cachedEvents.length === 0) return false;

  cachedEvents.sort((left, right) => Number(left.created_at || 0) - Number(right.created_at || 0));
  let hydrated = false;
  for (const event of cachedEvents) {
    if (applyControlplaneEvent(event, { fromCache: true })) hydrated = true;
  }
  refreshCollections();
  return hydrated;
}

export const controlplaneEventRouting = Object.freeze({
  schemas: BAHIA_STATE_SCHEMAS,
  routeFor: (event) => ({ domain: eventDomain(event), schema: eventSchema(event) })
});
