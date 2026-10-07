// Producer-shaped control-plane fixtures for the e2e mock relays. They mirror
// what the daemon publishes, so a mock cannot drift from the
// wire contract the web REQs on:
//
//   - cp-state records: canonical kind 30900 in the projector's envelope
//     (internal/adapters/nostr controlStateEnvelope; the worker publisher's
//     workerCPStateEnvelope is the same shape): d, domain, schema=
//     bahia.cp-state.v1, legacy_kind, deleted and the family's single-letter
//     "t" topic, then the family's own tags. Worker families address records
//     under their per-family d prefix (kinds CPStateFamily.WorkerDTag).
//   - audit facts: regular kind 4903 (internal/adapters/nostr publishAudit):
//     no d, t=cp-audit plus t=<event type>, a deterministic fact id and the
//     audited entity's cp-state coordinate in "state".
//
// The contract table is generated from web/src/lib/nostr/kinds.gen.js, and the
// builders are pure functions of (contract, options), so the same code builds
// fixtures in Node specs and inside in-page harnesses (installE2EMocks exposes
// them as window.__bahiaE2EFixtures). The mock relay signs every fixture with
// the e2e test keyring on delivery, so fixtures are unsigned templates.
import { createHash } from 'node:crypto';
import {
  BAHIA_AUDIT_SCHEMA,
  BAHIA_CP_STATE_SCHEMA,
  BAHIA_STATE_SCHEMAS,
  CASCADIA_AUDIT,
  CASCADIA_CONTROLPLANE_STATE,
  CP_AUDIT_TAG_FACT,
  CP_AUDIT_TAG_STATE,
  CP_AUDIT_TOPIC,
  CP_STATE_TOPIC_BY_SCHEMA,
  WORKER_ASSIGNMENT_STATE_D_PREFIX,
  WORKER_CLEANUP_EXECUTION_D_PREFIX,
  WORKER_DRAIN_STATUS_D_PREFIX,
  WORKER_ELIGIBILITY_PREVIEW_D_PREFIX,
  WORKER_STATE_D_PREFIX
} from '../../src/lib/nostr/kinds.gen.js';
import { CP_STATE_SCHEMA_BY_LEGACY_KIND } from '../../src/lib/nostr/cp-state.js';
import { E2E_SERVICE_PUBKEY } from './e2e-keyring.js';

const WORKER_D_PREFIX_BY_SCHEMA = {
  [BAHIA_STATE_SCHEMAS.WORKER_STATE]: WORKER_STATE_D_PREFIX,
  [BAHIA_STATE_SCHEMAS.WORKER_ASSIGNMENT_STATE]: WORKER_ASSIGNMENT_STATE_D_PREFIX,
  [BAHIA_STATE_SCHEMAS.WORKER_DRAIN_STATUS]: WORKER_DRAIN_STATUS_D_PREFIX,
  [BAHIA_STATE_SCHEMAS.WORKER_ELIGIBILITY_PREVIEW]: WORKER_ELIGIBILITY_PREVIEW_D_PREFIX,
  [BAHIA_STATE_SCHEMAS.WORKER_CLEANUP_EXECUTION]: WORKER_CLEANUP_EXECUTION_D_PREFIX
};

function buildFamilies() {
  const families = {};
  for (const [legacyKind, schema] of Object.entries(CP_STATE_SCHEMA_BY_LEGACY_KIND)) {
    const topic = CP_STATE_TOPIC_BY_SCHEMA[schema];
    // Families without a topic are not projected on the cp-state envelope.
    if (!topic) continue;
    families[schema] = {
      legacyKind,
      topic,
      // The projector's family table pairs each topic "<domain>-<entity>" with
      // its domain; no domain contains a hyphen.
      domain: topic.split('-')[0],
      dPrefix: WORKER_D_PREFIX_BY_SCHEMA[schema] || ''
    };
  }
  return families;
}

/** The wire contract the builders encode, as plain JSON (also shipped into pages). */
export const CP_STATE_FIXTURE_CONTRACT = Object.freeze({
  stateKind: CASCADIA_CONTROLPLANE_STATE,
  stateSchema: BAHIA_CP_STATE_SCHEMA,
  families: buildFamilies(),
  auditKind: CASCADIA_AUDIT,
  auditSchema: BAHIA_AUDIT_SCHEMA,
  auditTopic: CP_AUDIT_TOPIC,
  auditTagState: CP_AUDIT_TAG_STATE,
  auditTagFact: CP_AUDIT_TAG_FACT
});

// The builders below run both in Node and, serialized, in the page: they must
// stay self-contained (no imports or module-scope references).

/**
 * One cp-state record. `d` is the record id the producer keys it by (for
 * worker families, the id after the family's d prefix, which is added here).
 */
export function buildCpStateEvent(contract, { schema, d, content = {}, tags = [], deleted = false, id, pubkey, createdAt }) {
  const family = contract.families[schema];
  if (!family) throw new Error(`no producer cp-state family for schema ${schema}`);
  const recordId = String(d ?? '');
  const coordinate = family.dPrefix && !recordId.startsWith(family.dPrefix) ? `${family.dPrefix}${recordId}` : recordId;
  return {
    id: id || `cp-state-${family.topic}-${coordinate}`,
    kind: contract.stateKind,
    pubkey,
    created_at: createdAt ?? Math.floor(Date.now() / 1000),
    tags: [
      ['d', coordinate],
      ['domain', family.domain],
      ['schema', contract.stateSchema],
      ['legacy_kind', family.legacyKind],
      ['deleted', String(Boolean(deleted))],
      ['t', family.topic],
      ...tags
    ],
    content: typeof content === 'string' ? content : JSON.stringify({ ...content, deleted: Boolean(deleted) })
  };
}

/**
 * One audit fact as the projector publishes it. `fact` defaults to a
 * deterministic id of (type, entity, content); Node callers get the
 * producer's sha256 through cpAuditFixture.
 */
export function buildCpAuditEvent(contract, { type, entityId = '', data = {}, state = '', sourceEventId = '', tags = [], fact, id, pubkey, createdAt }) {
  if (!type) throw new Error('audit fixtures need an event type');
  const content = JSON.stringify({ data, entity_id: entityId, event_type: type });
  let factId = fact;
  if (!factId) {
    // FNV-1a over the same inputs as auditFactID; stable, not cryptographic.
    let hash = 0x811c9dc5;
    for (const char of `${type}\u0000${entityId}\u0000${content}`) {
      hash ^= char.codePointAt(0);
      hash = Math.imul(hash, 0x01000193) >>> 0;
    }
    factId = hash.toString(16).padStart(8, '0').repeat(8);
  }
  const dot = type.indexOf('.');
  const auditTags = [
    ['domain', dot > 0 ? type.slice(0, dot) : 'control_plane'],
    ['type', type],
    ['schema', contract.auditSchema],
    ['protected', 'true'],
    ['t', contract.auditTopic],
    ['t', type],
    ['event_type', type],
    [contract.auditTagFact, factId]
  ];
  if (state) auditTags.push([contract.auditTagState, state]);
  if (sourceEventId) auditTags.push(['e', sourceEventId]);
  return {
    id: id || `cp-audit-${factId.slice(0, 16)}`,
    kind: contract.auditKind,
    pubkey,
    created_at: createdAt ?? Math.floor(Date.now() / 1000),
    tags: [...auditTags, ...tags],
    content
  };
}

/** One cp-state record authored by the e2e service identity (Node side). */
export function cpStateFixture({ pubkey = E2E_SERVICE_PUBKEY, ...options }) {
  return buildCpStateEvent(CP_STATE_FIXTURE_CONTRACT, { ...options, pubkey });
}

/** OCK-encrypted cp-state families have no plaintext content schema. */
export function confidentialCpStateFixture({ d, topic, legacyKind, content, createdAt, pubkey = E2E_SERVICE_PUBKEY }) {
  return {
    id: `cp-state-${topic}-${d}`,
    kind: CASCADIA_CONTROLPLANE_STATE,
    pubkey,
    created_at: createdAt ?? Math.floor(Date.now() / 1000),
    tags: [
      ['d', d], ['domain', topic.split('-')[0]], ['schema', BAHIA_CP_STATE_SCHEMA],
      ['legacy_kind', String(legacyKind)], ['deleted', 'false'], ['t', topic]
    ],
    content
  };
}

/**
 * The service-signed SoulFactory runtime policy as OperationalViewPublisher
 * emits it: the enabled runtimes plus the controller and pinned runtime keys
 * the browser may trust for Souls and kind:30317 capabilities.
 */
export function soulRuntimePolicyFixture({
  agentRuntimes = ['openclaw'], controllerPubkeys = [], runtimePubkeys = {}, createdAt, pubkey = E2E_SERVICE_PUBKEY
} = {}) {
  return {
    id: 'cp-state-soul-factory-runtime-policy',
    kind: CASCADIA_CONTROLPLANE_STATE,
    pubkey,
    created_at: createdAt ?? Math.floor(Date.now() / 1000),
    tags: [
      ['d', 'soul-factory:runtime-policy'], ['domain', 'soul-factory'], ['schema', BAHIA_CP_STATE_SCHEMA],
      ['legacy_kind', '32042'], ['deleted', 'false'], ['t', 'soul-factory-runtime-policy']
    ],
    content: JSON.stringify({
      agent_runtimes: agentRuntimes,
      ...(controllerPubkeys.length ? { controller_pubkeys: controllerPubkeys } : {}),
      ...(Object.keys(runtimePubkeys).length ? { runtime_pubkeys: runtimePubkeys } : {})
    })
  };
}

/** One audit fact authored by the e2e service identity, with the producer's sha256 fact id. */
export function cpAuditFixture({ pubkey = E2E_SERVICE_PUBKEY, fact, ...options }) {
  const { type, entityId = '', data = {} } = options;
  const content = JSON.stringify({ data, entity_id: entityId, event_type: type });
  const factId = fact || createHash('sha256').update(`${type}\u0000${entityId}\u0000${content}`).digest('hex');
  return buildCpAuditEvent(CP_STATE_FIXTURE_CONTRACT, { ...options, fact: factId, pubkey });
}

/**
 * One worker-state record as internal/controlplane WorkerStatePublisher emits
 * it: d=worker:state:<pubkey>, legacy_kind 32000, t=worker-state,
 * worker/status/scheduling_state tags, and the full worker (domain.Worker
 * JSON, keyed "pubkey") as content.
 */
export function workerStateFixture(worker, { id, createdAt, pubkey = E2E_SERVICE_PUBKEY, tags = [] } = {}) {
  const schedulingState = worker.scheduling_state || 'active';
  return cpStateFixture({
    id,
    createdAt,
    pubkey,
    schema: BAHIA_STATE_SCHEMAS.WORKER_STATE,
    d: worker.pubkey,
    tags: [
      ['worker', worker.pubkey],
      ['status', worker.status || ''],
      ['scheduling_state', schedulingState],
      ...tags
    ],
    content: { ...worker, scheduling_state: schedulingState }
  });
}

/**
 * Init script that exposes the same builders in the page as
 * window.__bahiaE2EFixtures.{cpState, cpAudit}(options), for harnesses that
 * project records from inside the browser. Callers pass the author pubkey.
 */
export function cpStateFixtureBrowserScript() {
  return `(() => {
  const contract = ${JSON.stringify(CP_STATE_FIXTURE_CONTRACT)};
  const buildState = ${buildCpStateEvent.toString()};
  const buildAudit = ${buildCpAuditEvent.toString()};
  window.__bahiaE2EFixtures = Object.freeze({
    contract,
    cpState: (options) => buildState(contract, options),
    cpAudit: (options) => buildAudit(contract, options)
  });
})();`;
}

export { BAHIA_STATE_SCHEMAS };
