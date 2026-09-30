// Producer-shaped control-plane read-model fixtures for e2e mock relays
// (bahia-irsry.37). They mirror what the daemon publishes: canonical kind 30900
// records in the projector's envelope (internal/adapters/nostr
// controlStateEnvelope) or the worker-state publisher's envelope
// (internal/controlplane workerCPStateEnvelope), including the single-letter
// "t" topic the web REQs on. The mock relay signs every fixture with the e2e
// test keyring on delivery (helpers.js signE2EEvent), so fixtures are unsigned
// templates here.
import {
  BAHIA_CP_STATE_SCHEMA,
  BAHIA_STATE_SCHEMAS,
  CASCADIA_CONTROLPLANE_STATE,
  CP_STATE_TOPIC_BY_SCHEMA,
  WORKER_STATE_CATALOG_KIND,
  WORKER_STATE_D_PREFIX,
  WORKER_STATE_DOMAIN,
  WORKER_STATE_TOPIC
} from '../../src/lib/nostr/kinds.gen.js';
import { CP_STATE_SCHEMA_BY_LEGACY_KIND } from '../../src/lib/nostr/cp-state.js';
import { E2E_SERVICE_PUBKEY } from './helpers.js';

function legacyKindForSchema(schema) {
  const entry = Object.entries(CP_STATE_SCHEMA_BY_LEGACY_KIND).find(([, value]) => value === schema);
  if (!entry) throw new Error(`no cp-state legacy_kind routes to ${schema}`);
  return entry[0];
}

/**
 * One projector cp-state record: d, domain, schema=bahia.cp-state.v1,
 * legacy_kind, deleted and the family's t topic, then the family's own tags.
 */
export function cpStateFixture({ id, schema, domain, d, content, tags = [], createdAt, pubkey = E2E_SERVICE_PUBKEY }) {
  const topic = CP_STATE_TOPIC_BY_SCHEMA[schema];
  if (!topic) throw new Error(`no cp-state topic for ${schema}`);
  return {
    id,
    kind: CASCADIA_CONTROLPLANE_STATE,
    pubkey,
    created_at: createdAt ?? Math.floor(Date.now() / 1000),
    tags: [
      ['d', d],
      ['domain', domain],
      ['schema', BAHIA_CP_STATE_SCHEMA],
      ['legacy_kind', legacyKindForSchema(schema)],
      ['deleted', 'false'],
      ['t', topic],
      ...tags
    ],
    content: JSON.stringify({ ...content, deleted: false })
  };
}

/**
 * One worker-state record as internal/controlplane WorkerStatePublisher emits
 * it: d=worker:state:<pubkey> in domain worker, legacy_kind 32000,
 * t=worker-state, worker/status/scheduling_state tags, and the full worker
 * (domain.Worker JSON, keyed "pubkey") as content.
 */
export function workerStateFixture(worker, { id, createdAt, pubkey = E2E_SERVICE_PUBKEY, tags = [] } = {}) {
  const schedulingState = worker.scheduling_state || 'active';
  return {
    id,
    kind: CASCADIA_CONTROLPLANE_STATE,
    pubkey,
    created_at: createdAt ?? Math.floor(Date.now() / 1000),
    tags: [
      ['d', `${WORKER_STATE_D_PREFIX}${worker.pubkey}`],
      ['domain', WORKER_STATE_DOMAIN],
      ['schema', BAHIA_CP_STATE_SCHEMA],
      ['legacy_kind', String(WORKER_STATE_CATALOG_KIND)],
      ['deleted', 'false'],
      ['t', WORKER_STATE_TOPIC],
      ['worker', worker.pubkey],
      ['status', worker.status || ''],
      ['scheduling_state', schedulingState],
      ...tags
    ],
    content: JSON.stringify({ ...worker, scheduling_state: schedulingState, deleted: false })
  };
}

export { BAHIA_STATE_SCHEMAS };
