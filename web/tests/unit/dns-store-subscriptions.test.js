import { describe, it, expect, beforeEach, vi } from 'vitest';

const systemInfoMock = vi.hoisted(() => ({
  loadSystemInfo: vi.fn()
}));

const controlplaneMock = vi.hoisted(() => ({
  bootstrapControlplane: vi.fn()
}));

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

const nostrMock = vi.hoisted(() => ({
  setRelays: vi.fn(),
  connect: vi.fn(),
  queryUntilEose: vi.fn(),
  subscribe: vi.fn(),
  retryRelay: vi.fn()
}));

vi.mock('../../src/lib/stores/system.svelte.js', () => ({
  loadSystemInfo: systemInfoMock.loadSystemInfo
}));

vi.mock('../../src/lib/stores/controlplane.svelte.js', () => ({
  bootstrapControlplane: controlplaneMock.bootstrapControlplane
}));

vi.mock('../../src/lib/nostr/dns-controlplane.js', () => dnsCommandMock);

vi.mock('../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../src/lib/nostr/client.js');
  return {
    ...actual,
    nostr: nostrMock
  };
});

function event({ id, kind = 31976, pubkey = 'b'.repeat(64), created_at = 100, tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: typeof content === 'string' ? content : JSON.stringify(content)
  };
}

function endpointEvent(overrides = {}) {
  return event({
    id: overrides.id || 'endpoint-1',
    kind: 31976,
    tags: overrides.tags || [['d', 'endpoint:service:api:prod'], ['family', 'service'], ['env', 'prod'], ['dns', 'api.prod.example'], ['addr', '10.0.1.44'], ['health', 'healthy'], ['runtime', 'docker'], ['t', 'dns-endpoint'], ['t', 'bahia']],
    content: { coordinate: 'endpoint:service:api:prod', service: 'svc-api', route: 'api', env: 'prod', fqdn: 'api.prod.example', addr: '10.0.1.44', health: 'healthy', runtime: 'docker', capabilities: ['http'], ...overrides.content },
    ...overrides
  });
}

function zoneEvent(overrides = {}) {
  return event({
    id: overrides.id || 'zone-1',
    kind: 31975,
    tags: overrides.tags || [['d', 'zone:prod.example'], ['zone', 'prod.example'], ['backend', 'coredns'], ['t', 'dns-zone'], ['t', 'bahia']],
    content: { name: 'prod.example', visibility: 'public', backend_ref: 'coredns', ...overrides.content },
    ...overrides
  });
}

describe('DNS dashboard Nostr subscription store', () => {
  let store;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    global.fetch = vi.fn(async () => ({ ok: true, json: async () => ({ supported_nips: [1, 11, 42] }) }));
    nostrMock.connect.mockResolvedValue({ connected: 1, total: 1, failed: 0, connecting: 0, relays: [{ url: 'ws://localhost:10547/relay', status: 'connected' }] });
    nostrMock.subscribe.mockReturnValue(vi.fn());
    systemInfoMock.loadSystemInfo.mockResolvedValue({
      nostr: {
        browser_relays: ['http://localhost:10547/relay'],
        service_pubkey: 'b'.repeat(64)
      }
    });
    store = await import('../../src/lib/stores/dns.svelte.js');
    store.resetDnsReadModels();
  });

  it('connects with NIP-11 metadata, opens a long-lived scoped REQ, and does not fetch DNS REST data', async () => {
    const result = await store.connect('http://localhost:10547/relay', 'b'.repeat(64));

    expect(result.ok).toBe(true);
    expect(global.fetch).toHaveBeenCalledWith('http://localhost:10547/relay', { headers: { Accept: 'application/nostr+json' } });
    expect(global.fetch).not.toHaveBeenCalledWith(expect.stringContaining('/api/v1/dns'), expect.anything());
    expect(nostrMock.setRelays).toHaveBeenCalledWith(['ws://localhost:10547/relay'], false);
    expect(nostrMock.connect).toHaveBeenCalledWith(['ws://localhost:10547/relay'], { force: true });
    expect(nostrMock.queryUntilEose).not.toHaveBeenCalled();
    expect(nostrMock.subscribe).toHaveBeenCalledWith([
      { kinds: [31975, 31976, 31977, 31978], '#t': ['bahia'], limit: 5000, authors: ['b'.repeat(64)] }
    ], expect.objectContaining({ onEvent: expect.any(Function), onEose: expect.any(Function), onClosed: expect.any(Function), onAuth: expect.any(Function) }));
  });

  it('updates reactive DNS state from live EVENT callbacks and marks EOSE catch-up complete', async () => {
    await store.connect('ws://localhost:10547/relay', 'b'.repeat(64));
    const callbacks = nostrMock.subscribe.mock.calls[0][1];

    callbacks.onEvent(zoneEvent(), 'relay-a');
    callbacks.onEvent(endpointEvent(), 'relay-a');
    callbacks.onEose('relay-a');

    expect(store.dnsState.zones).toEqual([expect.objectContaining({ name: 'prod.example', backend: 'coredns' })]);
    expect(store.dnsState.endpoints).toEqual([expect.objectContaining({ id: 'endpoint:service:api:prod', fqdn: 'api.prod.example', address: '10.0.1.44' })]);
    expect(store.dnsState.loading.subscription).toBe(false);
    expect(store.dnsState.connection.status).toBe('live');
    expect(store.dnsState.connection.eoseRelays).toEqual(['relay-a']);
  });

  it('dedupes parameterized replaceable DNS events and applies tombstones', () => {
    store.dnsState.connection.servicePubkey = 'b'.repeat(64);

    const active = endpointEvent({ id: 'active', created_at: 100, content: { fqdn: 'old.prod.example' } });
    const newer = endpointEvent({ id: 'newer', created_at: 120, content: { fqdn: 'new.prod.example' } });
    const stale = endpointEvent({ id: 'stale', created_at: 110, content: { fqdn: 'stale.prod.example' } });
    const tombstone = endpointEvent({
      id: 'deleted',
      created_at: 130,
      tags: [['d', 'endpoint:service:api:prod'], ['deleted', 'true'], ['t', 'dns-endpoint'], ['t', 'bahia']],
      content: { deleted: true, coordinate: 'endpoint:service:api:prod' }
    });

    expect(store.applyDNSReadModelEvent(active)).toBe(true);
    expect(store.applyDNSReadModelEvent(newer)).toBe(true);
    expect(store.applyDNSReadModelEvent(stale)).toBe(false);
    expect(store.dnsState.endpoints).toEqual([expect.objectContaining({ fqdn: 'new.prod.example' })]);
    expect(store.applyDNSReadModelEvent(tombstone)).toBe(true);
    expect(store.dnsState.endpoints).toEqual([]);
  });

  it('surfaces CLOSED and AUTH subscription failures without hiding them behind refresh', async () => {
    await store.connect('ws://localhost:10547/relay', 'b'.repeat(64));
    const callbacks = nostrMock.subscribe.mock.calls[0][1];

    callbacks.onClosed('rate-limited', 'relay-a', { terminal: true, source: 'closed' });
    expect(store.dnsState.error.subscription).toContain('rate-limited');
    expect(store.dnsState.connection.status).toBe('degraded');
    expect(store.dnsState.connection.lastClosed).toMatchObject({ reason: 'rate-limited', relay: 'relay-a', terminal: true });

    callbacks.onAuth('auth-required', 'relay-b');
    expect(store.dnsState.error.subscription).toContain('requires AUTH');
    expect(store.dnsState.connection.status).toBe('auth-required');
  });
});
