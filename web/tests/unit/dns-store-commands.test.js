import { describe, it, expect, beforeEach, vi } from 'vitest';

const bootstrapMock = vi.hoisted(() => vi.fn());
const dnsCommandMock = vi.hoisted(() => ({
  startDNSCommand: vi.fn(),
  dnsResultIsFailure: vi.fn((result) => ['error', 'failed', 'rejected'].includes(String(result?.status || '').toLowerCase())),
  DNS_COMMANDS: {
    ZONE_CREATE: 'zone_create',
    POLICY_APPLY: 'policy_apply',
    RECORD_OVERRIDE: 'record_override',
    DRIFT_REMEDIATE: 'drift_remediate'
  }
}));

vi.mock('../../src/lib/stores/controlplane.svelte.js', () => ({
  bootstrapControlplane: bootstrapMock
}));

vi.mock('../../src/lib/nostr/dns-controlplane.js', () => dnsCommandMock);

describe('DNS command store APIs', () => {
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    bootstrapMock.mockResolvedValue({ ok: true });
    dnsCommandMock.dnsResultIsFailure.mockImplementation((result) => ['error', 'failed', 'rejected'].includes(String(result?.status || '').toLowerCase()));
    dnsCommandMock.startDNSCommand.mockResolvedValue({
      requestEventId: 'req-1',
      ok: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      acceptedRelays: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      rejectedRelays: [],
      result: Promise.resolve({ id: 'result-1', status: 'success', content: { message: 'done' } })
    });
    store = await import('../../src/lib/stores/dns.svelte.js');
    store.resetDnsReadModels();
    store.resetDnsCommandRuns();
  });

  it('starts all four DNS Nostr commands through the run tracker', async () => {
    await store.createDNSZone({ name: 'prod.example' });
    await store.applyDNSPolicy({ name: 'internal-only' });
    await store.overrideDNSRecord({ zone_name: 'prod.example', record_name: 'api', record_type: 'A', value: '192.0.2.10', ttl: 60, reason: 'incident' });
    await store.remediateDNSDrift({ zone: 'prod.example' });

    expect(dnsCommandMock.startDNSCommand).toHaveBeenNthCalledWith(1, expect.objectContaining({ command: 'zone_create', payload: { name: 'prod.example' } }));
    expect(dnsCommandMock.startDNSCommand).toHaveBeenNthCalledWith(2, expect.objectContaining({ command: 'policy_apply' }));
    expect(dnsCommandMock.startDNSCommand).toHaveBeenNthCalledWith(3, expect.objectContaining({ command: 'record_override' }));
    expect(dnsCommandMock.startDNSCommand).toHaveBeenNthCalledWith(4, expect.objectContaining({ command: 'drift_remediate' }));
    expect(store.dnsState.commandRuns).toHaveLength(4);
    expect(store.dnsState.commandRuns[0]).toMatchObject({ command: 'drift_remediate', phase: 'completed', requestEventId: 'req-1' });
  });

  it('tracks publish OK, status events, and terminal result payloads', async () => {
    let statusCallback;
    let resolveResult;
    const resultPromise = new Promise((resolve) => {
      resolveResult = resolve;
    });
    dnsCommandMock.startDNSCommand.mockImplementation(async ({ onStatus }) => {
      statusCallback = onStatus;
      return {
        requestEventId: 'req-2',
        ok: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
        acceptedRelays: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
        rejectedRelays: [{ relay: 'ws://relay-2.test', sent: true, accepted: false, message: 'duplicate' }],
        result: resultPromise
      };
    });

    const run = await store.createDNSZone({ name: 'prod.example' });
    const trackedResult = run.result;
    statusCallback({ id: 'status-1', status: 'processing', step: 'reconciling' });
    resolveResult({ id: 'result-2', status: 'success', content: { zone: 'prod.example' } });
    await expect(trackedResult).resolves.toMatchObject({ id: 'result-2', status: 'success' });

    expect(run.publishOk).toHaveLength(1);
    expect(run.acceptedRelays).toHaveLength(1);
    expect(run.rejectedRelays).toHaveLength(1);
    expect(run.statusEvents).toEqual([{ id: 'status-1', status: 'processing', step: 'reconciling' }]);
    expect(run.phase).toBe('completed');
    expect(run.result).toMatchObject({ content: { zone: 'prod.example' } });
  });

  it('records rejected publish failures in commandRuns', async () => {
    dnsCommandMock.startDNSCommand.mockRejectedValueOnce(new Error('Nostr request publish rejected: auth-required'));

    await expect(store.remediateDNSDrift({ zone: 'prod.example' })).rejects.toThrow('auth-required');

    expect(store.dnsState.commandRuns).toHaveLength(1);
    expect(store.dnsState.commandRuns[0]).toMatchObject({
      command: 'drift_remediate',
      phase: 'rejected',
      error: 'Nostr request publish rejected: auth-required'
    });
  });

  it('records CLOSED/AUTH result subscription errors', async () => {
    dnsCommandMock.startDNSCommand.mockResolvedValueOnce({
      requestEventId: 'req-closed',
      ok: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      acceptedRelays: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      rejectedRelays: [],
      result: Promise.reject(new Error('Nostr result subscription auth closure: ws://relay.test: auth-required'))
    });

    const run = await store.applyDNSPolicy({ name: 'internal-only' });
    await expect(run.result).rejects.toThrow('auth-required');

    expect(run.phase).toBe('error');
    expect(run.error).toContain('auth-required');
  });

  it('builds narrow DNS read-model relay filters scoped to the Bahia service pubkey', () => {
    expect(store.dnsReadModelFilters('b'.repeat(64))).toEqual([
      { kinds: [31975, 31976, 31977, 31978], '#t': ['bahia'], limit: 5000, authors: ['b'.repeat(64)] }
    ]);
  });

  it('upserts DNS read-model EVENTs and removes tombstones without REST fetches', () => {
    store.dnsState.connection.servicePubkey = 'b'.repeat(64);

    const endpoint = {
      id: 'event-endpoint-1',
      kind: 31976,
      pubkey: 'b'.repeat(64),
      created_at: 100,
      tags: [['d', 'endpoint:service:api:prod'], ['t', 'dns-endpoint'], ['t', 'bahia'], ['dns', 'api.prod.example'], ['addr', '10.0.1.44'], ['env', 'prod'], ['health', 'healthy'], ['runtime', 'docker'], ['capability', 'http']],
      content: JSON.stringify({ service: 'svc-api', route: 'api', env: 'prod', proto: 'https', addr: '10.0.1.44', port: 8443, runtime: 'docker', health: 'healthy', capabilities: ['http'] })
    };

    expect(store.applyDNSReadModelEvent(endpoint)).toBe(true);
    expect(store.dnsState.endpoints).toEqual([
      expect.objectContaining({
        id: 'endpoint:service:api:prod',
        fqdn: 'api.prod.example',
        address: '10.0.1.44',
        environment: 'prod',
        capabilities: ['http']
      })
    ]);

    const tombstone = {
      ...endpoint,
      id: 'event-endpoint-2',
      created_at: 101,
      tags: [['d', 'endpoint:service:api:prod'], ['deleted', 'true'], ['t', 'dns-endpoint'], ['t', 'bahia'], ['dns', 'api.prod.example']],
      content: JSON.stringify({ deleted: true, coordinate: 'endpoint:service:api:prod', fqdn: 'api.prod.example' })
    };

    expect(store.applyDNSReadModelEvent(tombstone)).toBe(true);
    expect(store.dnsState.endpoints).toEqual([]);
  });

  it('rejects malformed DNS read-model content before mutating state', () => {
    store.dnsState.connection.servicePubkey = 'b'.repeat(64);

    const malformed = {
      id: 'event-bad-json',
      kind: 31975,
      pubkey: 'b'.repeat(64),
      created_at: 100,
      tags: [['d', 'zone:prod.example'], ['t', 'dns-zone'], ['t', 'bahia']],
      content: '{not-json}'
    };

    expect(store.applyDNSReadModelEvent(malformed)).toBe(false);
    expect(store.dnsState.zones).toEqual([]);
    expect(store.dnsState.error.subscription).toContain('invalid DNS read-model JSON content');
  });
});
