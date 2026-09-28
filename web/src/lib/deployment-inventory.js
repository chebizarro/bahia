// Deployment inventory protocol (bahia.deployment-inventory.v1).
//
// Bahia publishes the inventory as signed canonical control-state events
// (kind 30900, domain=deployment-inventory): one complete snapshot per
// environment plus redacted per-target runtime scan aggregates. This module
// is the browser's trust boundary for those events. It accepts only events
// from trusted service pubkeys with valid NIP-01 ids and signatures, applies
// addressable ordering (newest created_at, then lowest id) and tombstones,
// rejects malformed payloads without clobbering the last valid snapshot, and
// re-verifies cached events before showing them.
import { CAS_CONTROL_STATE } from './nostr/kinds.gen.js';
import { validateInboundNostrEvent } from './nostr/validation.js';
import { isReplaceableTombstone, shouldAcceptReplaceableEvent } from './nostr/replaceable.js';
import { getDTag, getTagValue } from './nostr/tags.js';

export const DEPLOYMENT_INVENTORY_KIND = CAS_CONTROL_STATE;
export const DEPLOYMENT_INVENTORY_DOMAIN = 'deployment-inventory';
export const DEPLOYMENT_INVENTORY_SCHEMA = 'bahia.deployment-inventory.v1';
export const ENVIRONMENT_INVENTORY_ENTITY = 'environment-inventory';
export const TARGET_SCAN_ENTITY = 'runtime-target-scan';
export const DEPLOYMENT_INVENTORY_CACHE_SCHEMA = 'bahia.deployment-inventory.cache.v1';

const ENVIRONMENT_D_PREFIX = `${DEPLOYMENT_INVENTORY_DOMAIN}:environment:`;
const TARGET_SCAN_D_PREFIX = `${DEPLOYMENT_INVENTORY_DOMAIN}:target-scan:`;
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
const COVERAGE = new Set(['observed', 'desired_only', 'observed_only', 'unknown']);
const INSTANCE_COVERAGE = new Set(['supervised', 'not_supervised']);
const SCAN_STATES = new Set(['complete', 'unavailable']);
const MAX_REJECTIONS = 50;

function isObject(value) {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function str(value) {
  return typeof value === 'string' ? value.trim() : '';
}

function isTimestamp(value) {
  return typeof value === 'string' && value !== '' && Number.isFinite(Date.parse(value));
}

function isCount(value) {
  return Number.isInteger(value) && value >= 0;
}

function coordinateKey(event) {
  return `${event.kind}:${event.pubkey}:${getDTag(event)}`;
}

export function deploymentInventoryFilters(trustedPubkeys) {
  const authors = normalizeTrusted(trustedPubkeys);
  if (authors.length === 0) throw new Error('Deployment inventory requires at least one trusted service pubkey');
  return [{
    kinds: [DEPLOYMENT_INVENTORY_KIND],
    authors,
    '#domain': [DEPLOYMENT_INVENTORY_DOMAIN]
  }];
}

function normalizeTrusted(trustedPubkeys) {
  return Array.from(new Set((trustedPubkeys || []).map((key) => str(key).toLowerCase()).filter(Boolean)));
}

export function createDeploymentInventoryState() {
  return { entries: new Map(), rejected: [] };
}

function reject(state, event, reason) {
  state.rejected.push({ id: str(event?.id), reason });
  if (state.rejected.length > MAX_REJECTIONS) state.rejected.splice(0, state.rejected.length - MAX_REJECTIONS);
  return { accepted: false, reason };
}

function validateEnvironmentPayload(payload, dTag) {
  if (payload.entity !== ENVIRONMENT_INVENTORY_ENTITY) return 'entity mismatch';
  if (payload.complete !== true) return 'environment inventory must be a complete snapshot';
  const environmentID = str(payload.environment?.id);
  if (!UUID.test(environmentID) || dTag !== `${ENVIRONMENT_D_PREFIX}${environmentID}`) return 'environment coordinate mismatch';
  if (!isCount(payload.freshness?.stale_after_seconds) || payload.freshness.stale_after_seconds === 0) return 'freshness budget missing';
  if (!INSTANCE_COVERAGE.has(payload.instance_coverage)) return 'instance coverage missing';
  if (!Array.isArray(payload.deployments)) return 'deployments must be an array';
  for (const row of payload.deployments) {
    if (!isObject(row) || !str(row.key) || !UUID.test(str(row.service?.id)) || !str(row.deployment_unit?.key)) return 'deployment identity missing';
    if (!COVERAGE.has(row.coverage) || !str(row.drift_status)) return 'deployment coverage missing';
    if (row.observed != null && (!isObject(row.observed) || !str(row.observed.observation_id) || !str(row.observed.health) || !isTimestamp(row.observed.observed_at))) return 'observation malformed';
    if (row.desired != null && !isObject(row.desired)) return 'desired state malformed';
    if (!Array.isArray(row.instances) || row.instances.some((instance) => !isObject(instance) || !str(instance.target) || !str(instance.status))) return 'instances malformed';
  }
  return '';
}

function validateTargetScanPayload(payload, dTag) {
  if (payload.entity !== TARGET_SCAN_ENTITY) return 'entity mismatch';
  const environment = str(payload.environment);
  const target = str(payload.target);
  if (!environment || !target || dTag !== `${TARGET_SCAN_D_PREFIX}${environment}:${target}`) return 'target coordinate mismatch';
  if (!SCAN_STATES.has(payload.scan_state) || !isTimestamp(payload.scanned_at)) return 'scan state malformed';
  if (!isCount(payload.freshness?.stale_after_seconds) || payload.freshness.stale_after_seconds === 0) return 'freshness budget missing';
  if (payload.scan_state === 'complete') {
    const counts = payload.counts;
    if (!isObject(counts) || !isCount(counts.total) || !isCount(counts.managed) || !isCount(counts.unmanaged) || counts.total !== counts.managed + counts.unmanaged) return 'scan counts malformed';
  } else if (payload.counts != null) {
    return 'unavailable scan must not claim counts';
  }
  return '';
}

function parsePayload(event) {
  try {
    const payload = JSON.parse(event.content);
    return isObject(payload) ? payload : null;
  } catch {
    return null;
  }
}

/**
 * Ingest one relay or cache event. Returns { accepted, reason, deleted }.
 * `validate` performs NIP-01 id and Schnorr signature verification.
 */
export async function ingestDeploymentInventoryEvent(state, event, {
  trustedPubkeys = [],
  source = 'relay',
  validate = validateInboundNostrEvent,
  nowSeconds
} = {}) {
  if (!isObject(event)) return reject(state, event, 'not an event');
  if (event.kind !== DEPLOYMENT_INVENTORY_KIND) return reject(state, event, 'unexpected kind');
  const trusted = new Set(normalizeTrusted(trustedPubkeys));
  if (!trusted.has(str(event.pubkey).toLowerCase())) return reject(state, event, 'untrusted author');
  if (getTagValue(event, 'domain') !== DEPLOYMENT_INVENTORY_DOMAIN || getTagValue(event, 'schema') !== DEPLOYMENT_INVENTORY_SCHEMA) {
    return reject(state, event, 'unexpected domain or schema');
  }
  const dTag = getDTag(event);
  const entity = dTag.startsWith(ENVIRONMENT_D_PREFIX)
    ? ENVIRONMENT_INVENTORY_ENTITY
    : dTag.startsWith(TARGET_SCAN_D_PREFIX) ? TARGET_SCAN_ENTITY : '';
  if (!entity || getTagValue(event, 'entity') !== entity) return reject(state, event, 'unknown inventory coordinate');
  try {
    await validate(event, nowSeconds === undefined ? undefined : { now: nowSeconds });
  } catch (error) {
    return reject(state, event, `invalid event: ${error?.message || error}`);
  }

  const key = coordinateKey(event);
  const existing = state.entries.get(key);
  if (existing && existing.event.id === event.id) {
    if (source === 'relay' && !existing.confirmed) {
      existing.confirmed = true;
      existing.unconfirmed = false;
      return { accepted: true, reason: 'confirmed', deleted: existing.deleted };
    }
    return { accepted: false, reason: 'duplicate', deleted: existing.deleted };
  }
  if (!shouldAcceptReplaceableEvent(existing?.event, event)) {
    return { accepted: false, reason: 'superseded', deleted: false };
  }

  const deleted = isReplaceableTombstone(event);
  let payload = null;
  if (!deleted) {
    payload = parsePayload(event);
    if (!payload || payload.schema !== DEPLOYMENT_INVENTORY_SCHEMA) return reject(state, event, 'malformed payload');
    const problem = entity === ENVIRONMENT_INVENTORY_ENTITY
      ? validateEnvironmentPayload(payload, dTag)
      : validateTargetScanPayload(payload, dTag);
    if (problem) return reject(state, event, `malformed payload: ${problem}`);
  }
  state.entries.set(key, {
    event,
    entity,
    payload,
    deleted,
    source,
    confirmed: source === 'relay',
    unconfirmed: false
  });
  return { accepted: true, reason: deleted ? 'tombstone' : 'accepted', deleted };
}

/** After relays reach EOSE, cached entries no relay re-delivered are flagged. */
export function markUnconfirmedCacheEntries(state) {
  let count = 0;
  for (const entry of state.entries.values()) {
    if (entry.source === 'cache' && !entry.confirmed) {
      entry.unconfirmed = true;
      count += 1;
    }
  }
  return count;
}

export function serializeDeploymentInventoryCache(state, { cachedAt = Date.now() } = {}) {
  return JSON.stringify({
    schema: DEPLOYMENT_INVENTORY_CACHE_SCHEMA,
    cachedAt,
    events: Array.from(state.entries.values()).map((entry) => entry.event)
  });
}

/**
 * Restore cached raw events. Cache storage is untrusted: every event is
 * re-verified (signature, id, trusted author, schema) exactly like a relay
 * event and marked as cached until a relay re-delivers it.
 */
export async function restoreDeploymentInventoryCache(state, raw, options = {}) {
  let parsed;
  try {
    parsed = JSON.parse(raw || '');
  } catch {
    return { restored: 0, rejected: 0 };
  }
  if (!isObject(parsed) || parsed.schema !== DEPLOYMENT_INVENTORY_CACHE_SCHEMA || !Array.isArray(parsed.events)) {
    return { restored: 0, rejected: 0 };
  }
  let restored = 0;
  let rejected = 0;
  for (const event of parsed.events) {
    const result = await ingestDeploymentInventoryEvent(state, event, { ...options, source: 'cache' });
    if (result.accepted) restored += 1;
    else if (result.reason !== 'superseded' && result.reason !== 'duplicate') rejected += 1;
  }
  return { restored, rejected };
}

function ageSeconds(timestamp, nowMs) {
  const at = Date.parse(timestamp || '');
  return Number.isFinite(at) ? Math.max(0, Math.floor((nowMs - at) / 1000)) : null;
}

function provenance(entry) {
  if (entry.confirmed) return 'relay';
  return entry.unconfirmed ? 'cache_unconfirmed' : 'cache';
}

function deploymentRow(row, staleAfter, nowMs) {
  const observed = isObject(row.observed) ? row.observed : null;
  const desired = isObject(row.desired) ? row.desired : null;
  const observedAge = observed ? ageSeconds(observed.observed_at, nowMs) : null;
  const stale = observedAge !== null && observedAge > staleAfter;
  const health = observed ? str(observed.health) : '';
  const badges = [];
  if (row.coverage === 'desired_only') badges.push('desired_only');
  if (row.coverage === 'observed_only') badges.push('observed_only');
  if (row.coverage === 'unknown') badges.push('unknown');
  if (stale) badges.push('stale');
  if (health && health !== 'healthy') badges.push(health);
  if (observed && !row.drift_evaluated) badges.push('drift_pending');
  else if (str(row.drift_status) && row.drift_status !== 'in_sync') badges.push(row.drift_status);
  if (row.reconcile?.failure_reason) badges.push('reconcile_failing');
  const instances = (row.instances || []).map((instance) => {
    const age = ageSeconds(instance.observed_at, nowMs);
    return {
      target: str(instance.target),
      supervisor: str(instance.supervisor),
      status: str(instance.status),
      observedAt: str(instance.observed_at),
      ageSeconds: age,
      stale: age !== null && age > staleAfter
    };
  });
  return {
    key: str(row.key),
    service: str(row.service?.name) || str(row.service?.id),
    serviceId: str(row.service?.id),
    unit: str(row.deployment_unit?.display_name) || str(row.deployment_unit?.key),
    unitImplicit: row.deployment_unit?.implicit === true,
    ownership: str(row.deployment_unit?.ownership_mode),
    target: str(row.deployment_unit?.target) || str(row.runtime?.target),
    runtimeType: str(row.runtime?.type),
    runtimeTarget: str(row.runtime?.target),
    desiredRef: desired ? str(desired.image_ref) || str(desired.desired_hash) : '',
    desiredTag: desired ? str(desired.image_tag) : '',
    desiredImmutable: desired?.immutable === true,
    observedRef: observed ? [str(observed.image_repo), str(observed.image_digest)].filter(Boolean).join('@') : '',
    observedVersion: observed ? str(observed.version) : '',
    observationSource: observed ? str(observed.source) : '',
    observedAt: observed ? str(observed.observed_at) : '',
    ageSeconds: observedAge,
    health: health || 'not_observed',
    drift: str(row.drift_status),
    driftEvaluated: row.drift_evaluated === true,
    coverage: row.coverage,
    stale,
    reconcileFailure: str(row.reconcile?.failure_reason),
    badges,
    instances
  };
}

function targetScanRow(entry, nowMs) {
  const payload = entry.payload;
  const staleAfter = payload.freshness.stale_after_seconds;
  const age = ageSeconds(payload.scanned_at, nowMs);
  const stale = age !== null && age > staleAfter;
  // Only aggregate, allowlisted fields are surfaced; per-instance data is
  // never part of the public scan protocol and is ignored if present.
  const counts = payload.scan_state === 'complete'
    ? { total: payload.counts.total, managed: payload.counts.managed, unmanaged: payload.counts.unmanaged }
    : null;
  return {
    environment: str(payload.environment),
    target: str(payload.target),
    endpointRef: str(payload.endpoint_ref),
    scanState: payload.scan_state,
    state: payload.scan_state === 'unavailable' ? 'unavailable' : stale ? 'stale' : 'complete',
    scannedAt: str(payload.scanned_at),
    ageSeconds: age,
    stale,
    counts,
    provenance: provenance(entry)
  };
}

/** Derive the render model. `nowMs` drives freshness only; it never gates events. */
export function buildDeploymentInventoryView(state, { nowMs = Date.now() } = {}) {
  const environments = new Map();
  const ensureEnvironment = (name) => {
    if (!environments.has(name)) {
      environments.set(name, { name, id: '', inventory: null, targetScans: [] });
    }
    return environments.get(name);
  };
  for (const entry of state.entries.values()) {
    if (entry.deleted || !entry.payload) continue;
    if (entry.entity === ENVIRONMENT_INVENTORY_ENTITY) {
      const payload = entry.payload;
      const name = str(payload.environment.name) || str(payload.environment.id);
      const environment = ensureEnvironment(name);
      const staleAfter = payload.freshness.stale_after_seconds;
      environment.id = str(payload.environment.id);
      environment.inventory = {
        eventId: entry.event.id,
        publishedAt: new Date(Number(entry.event.created_at) * 1000).toISOString(),
        provenance: provenance(entry),
        staleAfterSeconds: staleAfter,
        instanceCoverage: payload.instance_coverage,
        deployments: payload.deployments
          .map((row) => deploymentRow(row, staleAfter, nowMs))
          .sort((a, b) => a.service.localeCompare(b.service, undefined, { sensitivity: 'base' }) || a.key.localeCompare(b.key))
      };
    } else if (entry.entity === TARGET_SCAN_ENTITY) {
      const scan = targetScanRow(entry, nowMs);
      ensureEnvironment(scan.environment).targetScans.push(scan);
    }
  }
  const list = Array.from(environments.values())
    .map((environment) => ({
      ...environment,
      targetScans: environment.targetScans.sort((a, b) => a.target.localeCompare(b.target))
    }))
    .sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
  return {
    environments: list,
    deploymentCount: list.reduce((total, environment) => total + (environment.inventory?.deployments.length || 0), 0),
    rejectedCount: state.rejected.length
  };
}
