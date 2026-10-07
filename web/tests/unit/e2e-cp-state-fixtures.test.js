// @vitest-environment node
// Guards the e2e mock relays against wire-contract drift: the
// shared fixture builders must emit what the producers emit and what the web
// consumers route, and no e2e mock may hand-roll a cp-state record.
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  BAHIA_CP_STATE_SCHEMA,
  CASCADIA_AUDIT,
  CASCADIA_CONTROLPLANE_STATE,
  CP_AUDIT_TOPIC,
  CP_STATE_TOPIC_BY_SCHEMA
} from '../../src/lib/nostr/kinds.gen.js';
import { controlStateSchema, workerRecordId } from '../../src/lib/nostr/cp-state.js';
import {
  CP_STATE_FIXTURE_CONTRACT,
  cpAuditFixture,
  cpStateFixture,
  cpStateFixtureBrowserScript
} from '../e2e/cp-state-fixtures.js';

const tagValues = (event, name) => event.tags.filter((tag) => tag[0] === name).map((tag) => tag[1]);

describe('e2e cp-state fixtures', () => {
  it('cover every cp-state family the web REQs by topic', () => {
    expect(Object.keys(CP_STATE_FIXTURE_CONTRACT.families).sort()).toEqual(Object.keys(CP_STATE_TOPIC_BY_SCHEMA).sort());
  });

  it.each(Object.keys(CP_STATE_TOPIC_BY_SCHEMA))('builds %s on the producer envelope', (schema) => {
    const event = cpStateFixture({ schema, d: 'record-1', content: { id: 'record-1' } });
    const topic = CP_STATE_TOPIC_BY_SCHEMA[schema];
    expect(event.kind).toBe(CASCADIA_CONTROLPLANE_STATE);
    // Envelope order of internal/adapters/nostr controlStateEnvelope.
    expect(event.tags.slice(0, 6).map((tag) => tag[0])).toEqual(['d', 'domain', 'schema', 'legacy_kind', 'deleted', 't']);
    expect(tagValues(event, 'schema')).toEqual([BAHIA_CP_STATE_SCHEMA]);
    expect(tagValues(event, 't')).toEqual([topic]);
    expect(topic.startsWith(`${tagValues(event, 'domain')[0]}-`)).toBe(true);
    // The web resolves the family back through legacy_kind.
    expect(controlStateSchema(event)).toBe(schema);
    if (schema.startsWith('bahia.state.worker')) {
      expect(workerRecordId(event)).toBe('record-1');
    } else {
      expect(tagValues(event, 'd')).toEqual(['record-1']);
    }
  });

  it('marks tombstones on the same coordinate', () => {
    const schema = Object.keys(CP_STATE_TOPIC_BY_SCHEMA)[0];
    const live = cpStateFixture({ schema, d: 'x', content: {} });
    const tombstone = cpStateFixture({ schema, d: 'x', deleted: true, content: {} });
    expect(tagValues(tombstone, 'd')).toEqual(tagValues(live, 'd'));
    expect(tagValues(tombstone, 'deleted')).toEqual(['true']);
    expect(JSON.parse(tombstone.content).deleted).toBe(true);
  });

  it('builds audit facts as regular 4903 events without a d', () => {
    const event = cpAuditFixture({ type: 'service.created', entityId: 'svc-1', state: 'svc-1', data: { name: 'api' } });
    expect(event.kind).toBe(CASCADIA_AUDIT);
    expect(tagValues(event, 'd')).toEqual([]);
    expect(tagValues(event, 't')).toEqual([CP_AUDIT_TOPIC, 'service.created']);
    expect(tagValues(event, 'fact')[0]).toMatch(/^[0-9a-f]{64}$/);
    expect(tagValues(event, 'state')).toEqual(['svc-1']);
    expect(JSON.parse(event.content)).toEqual({ data: { name: 'api' }, entity_id: 'svc-1', event_type: 'service.created' });
  });

  it('exposes the same builders to in-page harnesses', () => {
    const page = {};
    new Function('window', cpStateFixtureBrowserScript())(page);
    const options = { schema: 'bahia.registry.policy.v1', d: 'policy-1', content: { id: 'policy-1' }, pubkey: 'a'.repeat(64), createdAt: 1 };
    expect(page.__bahiaE2EFixtures.cpState(options)).toEqual(cpStateFixture(options));
  });
});

// Hand-rolled cp-state tags drift from the #t contract: a family schema in
// the schema tag, a legacy_kind, or the retired domain=controlplane envelope
// (no producer stamps it).
const HAND_ROLLED_CP_STATE = /\[\s*'schema'\s*,\s*'bahia\.(?:registry|state)\.[^']+'\s*\]|\[\s*'legacy_kind'\s*,|\[\s*'domain'\s*,\s*'controlplane'\s*\]/;

function e2eSources(dir) {
  return readdirSync(dir).flatMap((name) => {
    const path = join(dir, name);
    if (statSync(path).isDirectory()) return e2eSources(path);
    return path.endsWith('.js') ? [path] : [];
  });
}

describe('e2e mocks', () => {
  it('build cp-state records only through cp-state-fixtures.js', () => {
    const root = join(import.meta.dirname, '../e2e');
    const offenders = e2eSources(root)
      .filter((path) => !path.endsWith('cp-state-fixtures.js'))
      .flatMap((path) => readFileSync(path, 'utf8').split('\n')
        .map((line, index) => ({ line, at: `${relative(root, path)}:${index + 1}` }))
        .filter(({ line }) => HAND_ROLLED_CP_STATE.test(line))
        .map(({ at }) => at));
    expect(offenders).toEqual([]);
  });
});
