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

function event({ id, kind = 30900, pubkey = 'b'.repeat(64), created_at = 100, tags = [], content = {} }) {
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
    kind: 30900,
    tags: overrides.tags || [['domain', 'dns'], ['schema', 'bahia.state.dns-endpoint.v1'], ['d', 'endpoint:service:api:prod'], ['family', 'service'], ['env', 'prod'], ['dns', 'api.prod.example'], ['addr', '10.0.1.44'], ['health', 'healthy'], ['runtime', 'docker']],
    content: { coordinate: 'endpoint:service:api:prod', service: 'svc-api', route: 'api', env: 'prod', fqdn: 'api.prod.example', addr: '10.0.1.44', health: 'healthy', runtime: 'docker', capabilities: ['http'], ...overrides.content },
    ...overrides
  });
}

function zoneEvent(overrides = {}) {
  return event({
    id: overrides.id || 'zone-1',
    kind: 30900,
    tags: overrides.tags || [['domain', 'dns'], ['schema', 'bahia.state.dns-zone.v1'], ['d', 'zone:prod.example'], ['zone', 'prod.example'], ['backend', 'coredns']],
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
    expect(nostrMock.subscribe).toHaveBeenCalledTimes(1);
    expect(nostrMock.subscribe).toHaveBeenCalledWith([
      { kinds: [30900], '#domain': ['dns'], '#schema': ['bahia.state.dns-zone.v1', 'bahia.state.dns-endpoint.v1', 'bahia.state.dns-policy.v1', 'bahia.state.dns-backend.v1'], limit: 5000, authors: ['b'.repeat(64)] }
    ], expect.objectContaining({ onEvent: expect.any(Function), onEose: expect.any(Function), onClosed: expect.any(Function), onAuth: expect.any(Function) }));
  });

  it('keeps NIP-11 unavailable, malformed, and limiting metadata advisory', async () => {
    global.fetch = vi.fn(async (url) => {
      if (String(url).includes('missing')) return { ok: false, status: 503, json: async () => ({}) };
      if (String(url).includes('malformed')) return { ok: true, json: async () => [] };
      return {
        ok: true,
        json: async () => ({
          name: 'limited',
          supported_nips: [1, 11, 42],
          limitation: { auth_required: true, payment_required: true, restricted_writes: true, max_limit: 25 }
        })
      };
    });

    const relays = ['wss://missing.example', 'wss://malformed.example', 'wss://limited.example'];
    const result = await store.connect(relays, 'b'.repeat(64));

    expect(result.ok).toBe(true);
    expect(store.dnsState.connection.relays).toEqual(relays);
    expect(store.dnsState.connection.servicePubkey).toBe('b'.repeat(64));
    expect(nostrMock.setRelays).toHaveBeenCalledWith(relays, false);
    expect(store.dnsState.connection.relayHealth['wss://missing.example']).toContain('metadata-unavailable');
    expect(store.dnsState.connection.relayHealth['wss://malformed.example']).toContain('metadata-malformed');
    expect(store.dnsState.connection.relayHealth['wss://limited.example']).toBe('metadata-limited');
    expect(store.dnsState.connection.metadata['wss://limited.example']).toMatchObject({
      supported_nips: [1, 11, 42],
      advisory_limitations: { auth_required: true, payment_required: true, restricted_writes: true, max_limit: 25 }
    });
  });

  it('subscribes to NIP-66 monitor events only for configured trusted monitor pubkeys', async () => {
    const monitor = 'a'.repeat(64);
    const result = await store.connect('wss://relay.example', 'b'.repeat(64), { trustedRelayMonitorPubkeys: [monitor] });

    expect(result.ok).toBe(true);
    expect(nostrMock.subscribe).toHaveBeenCalledTimes(2);
    expect(nostrMock.subscribe.mock.calls[1][0]).toEqual([
      { kinds: [10166], authors: [monitor], limit: 1 },
      { kinds: [30166], authors: [monitor], '#d': ['wss://relay.example'], limit: 1 }
    ]);
  });

  it('ingests only trusted NIP-66 monitor events as advisory relay health metadata', () => {
    const trustedMonitor = 'a'.repeat(64);
    const untrustedMonitor = 'c'.repeat(64);
    store.dnsState.connection.relays = ['wss://relay.example'];
    store.dnsState.connection.servicePubkey = 'b'.repeat(64);
    store.dnsState.connection.trustedRelayMonitors = [trustedMonitor];

    const untrusted = event({
      id: 'untrusted-monitor-event',
      kind: 30166,
      pubkey: untrustedMonitor,
      tags: [['d', 'wss://relay.example'], ['R', 'auth'], ['N', '42']],
      content: { name: 'untrusted report' }
    });
    expect(store.applyRelayMonitorEvent(untrusted)).toBe(false);
    expect(store.dnsState.connection.relayHealth).toEqual({});

    const announcement = event({
      id: 'trusted-monitor-announcement',
      kind: 10166,
      pubkey: trustedMonitor,
      created_at: 120,
      tags: [['frequency', '3600'], ['c', 'open'], ['c', 'nip11'], ['timeout', 'open', '5000']],
      content: {}
    });
    expect(store.applyRelayMonitorEvent(announcement)).toBe(true);
    expect(store.dnsState.connection.relayMonitorAnnouncements[trustedMonitor]).toMatchObject({
      pubkey: trustedMonitor,
      frequency: '3600',
      checks: ['open', 'nip11']
    });
    const staleAnnouncement = event({
      id: 'stale-monitor-announcement',
      kind: 10166,
      pubkey: trustedMonitor,
      created_at: 110,
      tags: [['frequency', '60'], ['c', 'open']],
      content: {}
    });
    expect(store.applyRelayMonitorEvent(staleAnnouncement)).toBe(false);
    expect(store.dnsState.connection.relayMonitorAnnouncements[trustedMonitor].frequency).toBe('3600');

    const trustedDiscovery = event({
      id: 'trusted-monitor-discovery',
      kind: 30166,
      pubkey: trustedMonitor,
      created_at: 130,
      tags: [['d', 'wss://relay.example'], ['R', 'auth'], ['R', 'payment'], ['R', '!pow'], ['N', '11'], ['N', '42'], ['rtt-open', '123'], ['n', 'clearnet']],
      content: { name: 'relay' }
    });
    expect(store.applyRelayMonitorEvent(trustedDiscovery)).toBe(true);
    expect(store.dnsState.connection.relays).toEqual(['wss://relay.example']);
    expect(store.dnsState.connection.servicePubkey).toBe('b'.repeat(64));
    expect(store.dnsState.connection.relayHealth['wss://relay.example']).toBe('monitor-limited: auth,payment');
    expect(store.dnsState.connection.relayMonitorMetadata['wss://relay.example']).toMatchObject({
      relay: 'wss://relay.example',
      monitor_pubkey: trustedMonitor,
      requirements: ['auth', 'payment', '!pow'],
      warnings: ['auth', 'payment'],
      supported_nips: [11, 42],
      rtt: { 'rtt-open': 123 },
      advisory_only: true
    });
    const staleDiscovery = event({
      id: 'stale-monitor-discovery',
      kind: 30166,
      pubkey: trustedMonitor,
      created_at: 125,
      tags: [['d', 'wss://relay.example'], ['R', '!auth'], ['N', '11']],
      content: { name: 'stale relay' }
    });
    expect(store.applyRelayMonitorEvent(staleDiscovery)).toBe(false);
    expect(store.dnsState.connection.relayHealth['wss://relay.example']).toBe('monitor-limited: auth,payment');

    const unknownRelay = event({
      id: 'trusted-monitor-unknown-relay',
      kind: 30166,
      pubkey: trustedMonitor,
      tags: [['d', 'wss://unknown.example'], ['R', 'auth']],
      content: {}
    });
    expect(store.applyRelayMonitorEvent(unknownRelay)).toBe(false);
    expect(store.dnsState.connection.relays).toEqual(['wss://relay.example']);
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
      tags: [['domain', 'dns'], ['schema', 'bahia.state.dns-endpoint.v1'], ['d', 'endpoint:service:api:prod'], ['deleted', 'true']],
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
