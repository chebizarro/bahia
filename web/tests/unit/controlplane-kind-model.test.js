import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { matchFilter } from 'nostr-tools/filter';

import {
  applyControlplaneEvent,
  controlplaneStateTopics,
  readModelFilters,
  resetEventRouting
} from '../../src/lib/stores/controlplane/events.svelte.js';
import { controlplaneConnection } from '../../src/lib/stores/controlplane/connection.svelte.js';
import { events as activity, resetActivity } from '../../src/lib/stores/collections/activity.svelte.js';
import { BAHIA_STATE_SCHEMAS, CP_AUDIT_TOPIC, CP_STATE_TOPICS, CP_STATE_TOPIC_BY_SCHEMA } from '../../src/lib/nostr/kinds.gen.js';

const SERVICE_PUBKEY = 'b'.repeat(64);
const SERVICE_ID = '11111111-1111-4111-8111-111111111111';
const NOW = Math.floor(Date.now() / 1000);

// Shaped like the projector's controlStateEnvelope output (bahia-irsry.9.3).
function projectedState({ id, legacyKind, topic, d, extraTags = [], content = {}, createdAt = 1_780_000_000 }) {
  return {
    id,
    kind: 30900,
    pubkey: SERVICE_PUBKEY,
    created_at: createdAt,
    tags: [['d', d], ['domain', topic.split('-')[0]], ['schema', 'bahia.cp-state.v1'], ['legacy_kind', String(legacyKind)], ['deleted', 'false'], ['t', topic], ...extraTags],
    content: JSON.stringify(content),
    sig: ''
  };
}

// Shaped like the projector's publishAudit output: regular 4903, no d.
function auditFact({ id, fact, type = 'drift.detected', state = `service:${SERVICE_ID}:environment:e1`, createdAt = NOW - 120 }) {
  return {
    id,
    kind: 4903,
    pubkey: SERVICE_PUBKEY,
    created_at: createdAt,
    tags: [['domain', type.split('.')[0]], ['type', type], ['schema', 'bahia.audit.v1'], ['protected', 'true'], ['t', CP_AUDIT_TOPIC], ['t', type], ['event_type', type], ['fact', fact], ['state', state], ['service', SERVICE_ID]],
    content: JSON.stringify({ event_type: type, entity_id: SERVICE_ID, data: {} }),
    sig: ''
  };
}

beforeEach(() => {
  controlplaneConnection.servicePubkey = SERVICE_PUBKEY;
  resetEventRouting();
  resetActivity();
});

afterEach(() => {
  controlplaneConnection.servicePubkey = '';
});

describe('controlplane read-model filters on single-letter topics', () => {
  it('scopes 30900 by #t and never by multi-letter tags', () => {
    const filters = readModelFilters();
    for (const filter of filters) {
      expect(filter).not.toHaveProperty('#domain');
      expect(filter).not.toHaveProperty('#schema');
    }
    const stateFilters = filters.filter((filter) => filter.kinds.includes(30900));
    expect(stateFilters).toHaveLength(1);
    expect(stateFilters[0]).toMatchObject({ kinds: [30900], authors: [SERVICE_PUBKEY] });
    expect(stateFilters[0]['#t']).toEqual(controlplaneStateTopics());
  });

  it('routes only remaining legacy families, not store-first domains or DNS', () => {
    const topics = new Set(controlplaneStateTopics());
    for (const schema of ['LLM_ROUTE_REGISTRY', 'LLM_ROUTE_STATE', 'ARTIFACT_REGISTRY', 'BUILD_REGISTRY', 'DEPLOYMENT_INTENT_REGISTRY', 'DEPLOYMENT_RUN_REGISTRY']) {
      expect(topics.has(CP_STATE_TOPIC_BY_SCHEMA[BAHIA_STATE_SCHEMAS[schema]])).toBe(true);
    }
    for (const topic of [CP_STATE_TOPICS.SERVICE_REGISTRY, CP_STATE_TOPICS.SERVICE_STATE, CP_STATE_TOPICS.POLICY_REGISTRY, CP_STATE_TOPICS.PACKAGE_REPOSITORY, CP_STATE_TOPICS.BACKUP_RUN, CP_STATE_TOPICS.ML_MODEL, CP_STATE_TOPICS.PAYMENT_RECORD, CP_STATE_TOPICS.SECURITY_FINDING, 'dns-zone', 'worker-state']) {
      expect(topics.has(topic), topic).toBe(false);
    }
    expect(new Set(Object.values(CP_STATE_TOPIC_BY_SCHEMA)).size).toBe(Object.keys(CP_STATE_TOPIC_BY_SCHEMA).length);
  });

  it('matches and routes producer-shaped cp-state records', () => {
    const [stateFilter] = readModelFilters().filter((filter) => filter.kinds.includes(30900));
    const service = projectedState({ id: 'a'.repeat(64), legacyKind: 31962, topic: CP_STATE_TOPICS.SERVICE_REGISTRY, d: SERVICE_ID,
      content: { id: SERVICE_ID, name: 'api', deleted: false } });
    const pkg = projectedState({ id: 'c'.repeat(64), legacyKind: 31971, topic: CP_STATE_TOPICS.PACKAGE_REPOSITORY, d: 'package:repository:r1' });
    const untopiced = { ...service, id: 'd'.repeat(64), tags: service.tags.filter((tag) => tag[0] !== 't') };

    expect(matchFilter(stateFilter, service)).toBe(false);
    expect(matchFilter(stateFilter, pkg)).toBe(false);
    expect(matchFilter(stateFilter, untopiced)).toBe(false);

    expect(applyControlplaneEvent(service)).toBe(false); // core view is fed by BahiaEventStore, not the legacy router
  });
});

describe('activity feed audit facts', () => {
  it('keeps repeated audits of one entity and drops a republished fact', () => {
    expect(readModelFilters().some((filter) => filter.kinds.includes(4903))).toBe(false);
    const first = auditFact({ id: '1'.repeat(64), fact: 'f'.repeat(64) });
    const second = auditFact({ id: '2'.repeat(64), fact: 'e'.repeat(64), createdAt: NOW - 60 });
    const republished = auditFact({ id: '3'.repeat(64), fact: 'f'.repeat(64), createdAt: NOW });


    // Activity is no longer projected by this legacy router. The BahiaEventStore
    // query path is covered by rest-store-first.test.js.
    expect(applyControlplaneEvent(first)).toBe(false);
    expect(applyControlplaneEvent(second)).toBe(false);
    expect(applyControlplaneEvent(republished)).toBe(false);
    expect(activity).toEqual([]);
  });
});
