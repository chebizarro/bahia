import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { base64Decode, decryptConfidentialContent } from '../../src/lib/nostr/confidential.js';

const fixture = JSON.parse(readFileSync(join(process.cwd(), 'tests/fixtures/paysec-confidential.json'), 'utf8'));
const servicePubkey = 'a'.repeat(64);
const mock = vi.hoisted(() => {
  const events = new Map();
  const listeners = new Set();
  let key = null;
  const matches = (event, filter) => (!filter.kinds || filter.kinds.includes(event.kind)) &&
    (!filter.authors || filter.authors.includes(event.pubkey)) &&
    (!filter['#t'] || event.tags.some(([name, value]) => name === 't' && filter['#t'].includes(value)));
  return {
    events, listeners, get key() { return key; }, set key(value) { key = value; },
    store: {
      ingest(event) {
        const coordinate = event.tags.find(([name]) => name === 'd')?.[1];
        const key = coordinate ? `${event.kind}:${event.pubkey}:${coordinate}` : event.id;
        const previous = events.get(key);
        if (previous && (previous.created_at > event.created_at ||
          (previous.created_at === event.created_at && previous.id <= event.id))) return false;
        events.set(key, event);
        for (const listener of listeners) listener();
        return true;
      },
      query(filter) { return [...events.values()].filter((event) => matches(event, filter)); }
    }
  };
});
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getEventStore: () => mock.store,
  getServicePubkey: () => 'a'.repeat(64),
  onStoreRefresh: (cb) => { mock.listeners.add(cb); return () => mock.listeners.delete(cb); }
}));
vi.mock('../../src/lib/stores/auth-roles.svelte.js', () => ({
  contentKeyFor: () => mock.key,
  onContentKeyChange: () => () => {}
}));

function event({ kind = 30900, topic, d, content = {}, created_at = 1, id, tags = [] }) {
  return { id: id || `${topic}-${d}-${created_at}`, pubkey: servicePubkey, kind, created_at,
    tags: [['t', topic], ['d', d], ['deleted', 'false'], ...tags],
    content: typeof content === 'string' ? content : JSON.stringify(content) };
}
function encryptedEvent(record, created_at = 1) {
  return event({ topic: record.topic, d: record.d_tag, content: record.encrypted_content,
    created_at, tags: [['legacy_kind', String(record.legacy_kind)]] });
}

beforeEach(() => { mock.events.clear(); mock.listeners.clear(); mock.key = null; });

describe('W2-S3 store queries', () => {
  it('projects ML and backup latest winners and tombstones by t topic', async () => {
    const ml = await import('../../src/lib/stores/collections/ml.svelte.js');
    const backup = await import('../../src/lib/stores/collections/backup.svelte.js');
    mock.store.ingest(event({ topic: 'ml-model', d: 'model-a', content: { id: 'model-a', name: 'Old' } }));
    mock.store.ingest(event({ topic: 'ml-model', d: 'model-a', content: { id: 'model-a', name: 'New' }, created_at: 2 }));
    mock.store.ingest(event({ topic: 'backup-repository', d: 'repo-a', content: { id: 'repo-a', name: 'Repo' } }));
    ml.refreshML(); backup.refreshBackup();
    expect(ml.mlModels.map((row) => row.name)).toEqual(['New']);
    expect(backup.backupRepositories.map((row) => row.id)).toEqual(['repo-a']);
    mock.store.ingest(event({ topic: 'ml-model', d: 'model-a', created_at: 3, tags: [['deleted', 'true']] }));
    mock.store.ingest(event({ topic: 'backup-repository', d: 'repo-a', created_at: 3, tags: [['deleted', 'true']] }));
    ml.refreshML(); backup.refreshBackup();
    expect(ml.mlModels).toHaveLength(0);
    expect(backup.backupRepositories).toHaveLength(0);
  });

  it('routes SBOM reference and availability by kind plus t, and excludes tombstones', async () => {
    const sbom = await import('../../src/lib/stores/collections/sbom.svelte.js');
    mock.store.ingest(event({ kind: 30078, topic: 'sbom-reference', d: 'ref-a', tags: [['artifact', 'art-a']] }));
    mock.store.ingest(event({ kind: 30004, topic: 'sbom-availability', d: 'avail-a', tags: [['artifact', 'art-a']], content: { entries: ['x'] } }));
    mock.store.ingest(event({ kind: 30078, topic: 'security-findings', d: 'unrelated', tags: [['artifact', 'art-a']] }));
    sbom.refreshSBOM();
    expect(sbom.getSBOMRefsForArtifact('art-a')).toHaveLength(2);
    mock.store.ingest(event({ kind: 30078, topic: 'sbom-reference', d: 'ref-a', created_at: 2, tags: [['artifact', 'art-a'], ['deleted', 'true']] }));
    sbom.refreshSBOM();
    expect(sbom.getSBOMRefsForArtifact('art-a')).toHaveLength(1);
  });

  it('derives activity from store and deduplicates audit fact ids', async () => {
    const activity = await import('../../src/lib/stores/collections/activity.svelte.js');
    mock.store.ingest(event({ kind: 4903, topic: 'cp-audit', d: 'one', id: 'audit-1', tags: [['fact', 'fact-a']] }));
    mock.store.ingest(event({ kind: 4903, topic: 'cp-audit', d: 'two', id: 'audit-2', created_at: 2, tags: [['fact', 'fact-a']] }));
    mock.store.ingest(event({ kind: 4903, topic: 'not-cp-audit', d: 'other', id: 'audit-other' }));
    activity.refreshActivity();
    expect(activity.events.filter((row) => row.fact === 'fact-a')).toHaveLength(1);
    expect(activity.events).toHaveLength(1);
  });

  it('replaces and removes addressable activity status records', async () => {
    const activity = await import('../../src/lib/stores/collections/activity.svelte.js');
    mock.store.ingest(event({ kind: 30315, topic: 'intent-status', d: 'intent-a',
      content: { event_type: 'llm.pending' } }));
    mock.store.ingest(event({ kind: 30315, topic: 'intent-status', d: 'intent-a', created_at: 2,
      content: { event_type: 'llm.accepted' } }));
    activity.refreshActivity();
    expect(activity.events.map((row) => row.type)).toEqual(['llm.accepted']);
    mock.store.ingest(event({ kind: 30315, topic: 'intent-status', d: 'intent-a', created_at: 3,
      tags: [['deleted', 'true']] }));
    activity.refreshActivity();
    expect(activity.events).toHaveLength(0);
  });

  it('decrypts Go-generated payment/finding/detail records and reassembles chunks', async () => {
    mock.key = { orgID: 'fleet', version: 3, key: base64Decode(fixture.ock.key_b64) };
    for (const record of fixture.records) {
      const plaintext = decryptConfidentialContent(mock.key, record.encrypted_content,
        { legacyKind: record.legacy_kind, dTag: record.d_tag, topic: record.topic });
      expect(JSON.parse(plaintext)).toEqual(record.payload);
      mock.store.ingest(encryptedEvent(record));
    }
    const payments = await import('../../src/lib/stores/payments.svelte.js');
    const security = await import('../../src/lib/stores/security.svelte.js');
    expect(payments.loadPaymentHistory({ worker: 'worker-a' })[0].amount_sats).toBe(42);
    expect(security.listSecurityFindings({ run_id: 'run-a' })[0].details).toBe('first second');
    expect(security.listSecuritySchedules()[0].interval_seconds).toBe(86400);
    mock.store.ingest(encryptedEvent(fixture.records[0], 2));
    expect(payments.loadPaymentHistory({ worker: 'worker-a' }).map((row) => row.nostr_created_at)).toEqual([2]);
    expect(mock.store.ingest(encryptedEvent(fixture.records[0], 1))).toBe(false);
    mock.store.ingest(event({ topic: 'payment-record', d: fixture.records[0].d_tag, created_at: 2,
      tags: [['legacy_kind', '32011'], ['deleted', 'true']] }));
    expect(payments.loadPaymentHistory({ worker: 'worker-a' })).toHaveLength(1);
    mock.store.ingest(event({ topic: 'payment-record', d: fixture.records[0].d_tag, created_at: 3,
      tags: [['legacy_kind', '32011'], ['deleted', 'true']] }));
    expect(payments.loadPaymentHistory({ worker: 'worker-a' })).toHaveLength(0);
    mock.store.ingest(event({ topic: 'security-finding-detail', d: 'security:finding-detail:hash-a', created_at: 2,
      tags: [['legacy_kind', '32014'], ['deleted', 'true']] }));
    expect(security.listSecurityFindings({ run_id: 'run-a' })[0].details).toBe('');
    mock.store.ingest(event({ topic: 'security-finding', d: fixture.records[1].d_tag, created_at: 3,
      tags: [['legacy_kind', '32012'], ['deleted', 'true']] }));
    mock.store.ingest(event({ topic: 'security-schedule', d: fixture.records[2].d_tag, created_at: 3,
      tags: [['legacy_kind', '32013'], ['deleted', 'true']] }));
    expect(security.listSecurityFindings({ run_id: 'run-a' })).toHaveLength(0);
    expect(security.listSecuritySchedules()).toHaveLength(0);
  });

  it('shows ciphertext as not readable, rather than an error, without fleet OCK', async () => {
    mock.store.ingest(encryptedEvent(fixture.records[0]));
    mock.store.ingest(encryptedEvent(fixture.records[1]));
    const payments = await import('../../src/lib/stores/payments.svelte.js');
    const security = await import('../../src/lib/stores/security.svelte.js');
    expect(payments.loadPaymentHistory({ worker: 'worker-a' })).toEqual([]);
    expect(payments.paymentHistoryState.unreadable).toBe(true);
    expect(payments.paymentHistoryState.error).toBeNull();
    expect(security.listSecurityFindings()).toEqual([]);
    expect(security.securityState.unreadable).toBe(true);
    expect(security.securityState.findingsError).toBeNull();
  });

  it('ignores stale extra detail parts while requiring every current chunk', async () => {
    const { reassembleFindingDetails } = await import('../../src/lib/stores/collections/confidential-records.js');
    const current = [
      { finding_key_hash: 'hash-a', part_index: 0, total_parts: 2, details: 'first ', nostr_created_at: 2 },
      { finding_key_hash: 'hash-a', part_index: 1, total_parts: 2, details: 'second', nostr_created_at: 2 }
    ];
    expect(reassembleFindingDetails([current[0]])).toEqual(new Map());
    expect(reassembleFindingDetails([...current,
      { finding_key_hash: 'hash-a', part_index: 3, total_parts: 4, details: 'stale', nostr_created_at: 1 }
    ]).get('hash-a')).toBe('first second');
    expect(reassembleFindingDetails(current, [
      { dTag: 'security:finding-detail:hash-a', created_at: 3 }
    ])).toEqual(new Map());
    expect(reassembleFindingDetails(current, [
      { dTag: 'security:finding-detail:hash-a', created_at: 1 }
    ]).get('hash-a')).toBe('first second');
  });
});
