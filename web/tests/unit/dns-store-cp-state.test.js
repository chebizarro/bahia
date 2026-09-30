import { describe, it, expect, beforeEach, vi } from 'vitest';

// Producer contract: internal/adapters/nostr/projector.go controlStateEnvelope
// publishes every DNS family (live and tombstone) on kind 30900 with
// d / domain=dns / schema=bahia.cp-state.v1 / legacy_kind / deleted tags.
// The literals below pin that wire contract rather than the web constants.

const nostrMock = vi.hoisted(() => ({
  setRelays: vi.fn(),
  connect: vi.fn(),
  queryUntilEose: vi.fn(),
  subscribeWithRecovery: vi.fn(),
  retryRelay: vi.fn()
}));

vi.mock('../../src/lib/stores/system.svelte.js', () => ({ loadSystemInfo: vi.fn() }));
vi.mock('../../src/lib/stores/controlplane.svelte.js', () => ({ bootstrapControlplane: vi.fn() }));
vi.mock('../../src/lib/nostr/dns-controlplane.js', () => ({
  startDNSCommand: vi.fn(),
  dnsResultIsFailure: vi.fn(() => false),
  DNS_COMMANDS: { ZONE_CREATE: 'zone_create', POLICY_APPLY: 'policy_apply', RECORD_OVERRIDE: 'record_override', DRIFT_REMEDIATE: 'drift_remediate' }
}));
vi.mock('../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../src/lib/nostr/client.js');
  return { ...actual, nostr: nostrMock };
});

const SERVICE = 'b'.repeat(64);
const POLICY_ID = '0b0e8f8e-4f55-4d0c-9d8e-2f0c7b1a9e11';

function cpStateEvent({ id, legacyKind, d, deleted = false, created_at, tags = [], content, pubkey = SERVICE }) {
  return {
    id,
    kind: 30900,
    pubkey,
    created_at,
    tags: [
      ['d', d],
      ['domain', 'dns'],
      ['schema', 'bahia.cp-state.v1'],
      ['legacy_kind', String(legacyKind)],
      ['deleted', String(deleted)],
      ...tags
    ],
    content: JSON.stringify(content)
  };
}

// One entry per DNS family, shaped like the projector's publishDNS* and
// publishDNS*Tombstone functions.
const FAMILIES = [
  {
    name: 'zone',
    collection: 'zones',
    d: 'zone:prod.example',
    entityId: 'zone:prod.example',
    live: (version) => ({
      legacyKind: 31975,
      tags: [['zone', 'prod.example'], ['backend', 'coredns'], ['visibility', 'public'], ['t', 'dns-zone'], ['t', 'bahia']],
      content: { name: 'prod.example', visibility: 'public', backend_ref: `coredns-${version}`, ttl: 60, deleted: false }
    }),
    tombstone: () => ({
      legacyKind: 31975,
      tags: [['zone', 'prod.example'], ['backend', 'coredns'], ['visibility', 'public'], ['t', 'dns-zone'], ['t', 'bahia']],
      content: { name: 'prod.example', visibility: 'public', backend_ref: 'coredns', deleted: true }
    }),
    versionOf: (entity) => entity.backend.replace('coredns-', '')
  },
  {
    name: 'endpoint',
    collection: 'endpoints',
    d: 'endpoint:service:api:prod',
    entityId: 'endpoint:service:api:prod',
    live: (version) => ({
      legacyKind: 31976,
      tags: [['family', 'service'], ['health', 'healthy'], ['dns', 'api.prod.example'], ['addr', `10.0.1.${version}`], ['t', 'dns-endpoint'], ['t', 'bahia'], ['environment', 'prod'], ['service', 'api']],
      content: { family: 'service', name: 'api', environment: 'prod', fqdn: 'api.prod.example', coordinate: 'endpoint:service:api:prod', address: `10.0.1.${version}`, health: 'healthy' }
    }),
    tombstone: () => ({
      legacyKind: 31976,
      tags: [['t', 'dns-endpoint'], ['t', 'bahia'], ['dns', 'api.prod.example']],
      content: { deleted: true, coordinate: 'endpoint:service:api:prod', fqdn: 'api.prod.example' }
    }),
    versionOf: (entity) => entity.address.replace('10.0.1.', '')
  },
  {
    name: 'policy',
    collection: 'policies',
    d: `dnspolicy:${POLICY_ID}`,
    entityId: POLICY_ID,
    live: (version) => ({
      legacyKind: 31977,
      tags: [['policy', POLICY_ID], ['enabled', 'true'], ['t', 'dns-policy'], ['t', 'bahia']],
      content: { id: POLICY_ID, name: `internal-only-${version}`, rules: [], enabled: true, deleted: false }
    }),
    tombstone: () => ({
      legacyKind: 31977,
      tags: [['policy', POLICY_ID], ['enabled', 'true'], ['t', 'dns-policy'], ['t', 'bahia']],
      content: { id: POLICY_ID, name: 'internal-only', enabled: true, deleted: true }
    }),
    versionOf: (entity) => entity.name.replace('internal-only-', '')
  },
  {
    name: 'backend',
    collection: 'backends',
    d: 'dnsbackend:cloudflare',
    entityId: 'cloudflare',
    live: (version) => ({
      legacyKind: 31978,
      tags: [['backend', 'cloudflare'], ['type', 'cloudflare'], ['health', `healthy-${version}`], ['t', 'dns-backend'], ['t', 'bahia']],
      content: { ref: 'cloudflare', type: 'cloudflare', health: `healthy-${version}`, zones: [], deleted: false }
    }),
    tombstone: () => ({
      legacyKind: 31978,
      tags: [['backend', 'cloudflare'], ['type', 'cloudflare'], ['health', 'healthy'], ['t', 'dns-backend'], ['t', 'bahia']],
      content: { ref: 'cloudflare', type: 'cloudflare', health: 'healthy', deleted: true }
    }),
    versionOf: (entity) => entity.health.replace('healthy-', '')
  }
];

function liveEvent(family, { id, created_at, version }) {
  return cpStateEvent({ id, d: family.d, created_at, ...family.live(version) });
}

function tombstoneEvent(family, { id, created_at }) {
  return cpStateEvent({ id, d: family.d, created_at, deleted: true, ...family.tombstone() });
}

describe('DNS store consumes canonical 30900 DNS state (bahia.cp-state.v1)', () => {
  let store;
  let callbacks;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    global.fetch = vi.fn(async () => ({ ok: true, json: async () => ({ supported_nips: [1, 11] }) }));
    nostrMock.connect.mockResolvedValue({ connected: 1, total: 1, failed: 0, connecting: 0 });
    nostrMock.subscribeWithRecovery.mockReturnValue(vi.fn());
    store = await import('../../src/lib/stores/dns.svelte.js');
    store.resetDnsReadModels();
    const result = await store.connect('ws://relay.test', SERVICE);
    expect(result.ok).toBe(true);
    callbacks = nostrMock.subscribeWithRecovery.mock.calls[0][1];
  });

  it('subscribes to the dns domain without pinning a per-family schema the producer never sets', () => {
    const [readModelFilter] = nostrMock.subscribeWithRecovery.mock.calls[0][0];
    expect(readModelFilter).toEqual({ kinds: [30900], '#t': ['dns-zone', 'dns-endpoint', 'dns-policy', 'dns-backend'], limit: 5000, authors: [SERVICE] });
  });

  it.each(FAMILIES)('applies live and tombstone $name events from the subscription', (family) => {
    const entities = () => store.dnsState[family.collection];

    callbacks.onEvent(liveEvent(family, { id: `${family.name}-v1`, created_at: 100, version: 1 }), 'ws://relay.test');
    callbacks.onEose('ws://relay.test');
    expect(store.dnsState.connection.status).toBe('live');
    expect(entities()).toHaveLength(1);
    expect(entities()[0]).toMatchObject({ id: family.entityId, d: family.d, nostr_event_id: `${family.name}-v1` });
    expect(family.versionOf(entities()[0])).toBe('1');

    // Live update after EOSE replaces the entity on the same coordinate.
    callbacks.onEvent(liveEvent(family, { id: `${family.name}-v2`, created_at: 110, version: 2 }), 'ws://relay.test');
    expect(entities()).toHaveLength(1);
    expect(family.versionOf(entities()[0])).toBe('2');

    // Live tombstone removes it.
    callbacks.onEvent(tombstoneEvent(family, { id: `${family.name}-deleted`, created_at: 120 }), 'ws://relay.test');
    expect(entities()).toEqual([]);

    // A live record older than the tombstone (late relay) must not resurrect it.
    callbacks.onEvent(liveEvent(family, { id: `${family.name}-late`, created_at: 115, version: 3 }), 'ws://relay.test');
    expect(entities()).toEqual([]);

    // A newer live record after the tombstone re-creates it.
    callbacks.onEvent(liveEvent(family, { id: `${family.name}-v4`, created_at: 130, version: 4 }), 'ws://relay.test');
    expect(entities()).toHaveLength(1);
    expect(family.versionOf(entities()[0])).toBe('4');
  });

  it.each(FAMILIES)('keeps the newest $name event when an older one arrives later', (family) => {
    const entities = () => store.dnsState[family.collection];

    expect(store.applyDNSReadModelEvent(liveEvent(family, { id: `${family.name}-new`, created_at: 200, version: 7 }))).toBe(true);
    expect(store.applyDNSReadModelEvent(liveEvent(family, { id: `${family.name}-old`, created_at: 150, version: 5 }))).toBe(false);
    expect(family.versionOf(entities()[0])).toBe('7');

    // A tombstone older than the live record is ignored too.
    expect(store.applyDNSReadModelEvent(tombstoneEvent(family, { id: `${family.name}-old-delete`, created_at: 180 }))).toBe(false);
    expect(entities()).toHaveLength(1);
    expect(family.versionOf(entities()[0])).toBe('7');
  });

  it.each(FAMILIES)('breaks created_at ties on $name by lowest event id', (family) => {
    const entities = () => store.dnsState[family.collection];

    expect(store.applyDNSReadModelEvent(liveEvent(family, { id: 'f'.repeat(64), created_at: 300, version: 8 }))).toBe(true);
    expect(store.applyDNSReadModelEvent(liveEvent(family, { id: 'a'.repeat(64), created_at: 300, version: 9 }))).toBe(true);
    expect(store.applyDNSReadModelEvent(liveEvent(family, { id: 'c'.repeat(64), created_at: 300, version: 10 }))).toBe(false);
    expect(family.versionOf(entities()[0])).toBe('9');
  });

  it('backfills all four families before EOSE and reports the dashboard active', () => {
    expect(store.dnsState.availability).toBe('loading');
    for (const family of FAMILIES) {
      callbacks.onEvent(liveEvent(family, { id: `${family.name}-backfill`, created_at: 100, version: 1 }), 'ws://relay.test');
    }
    callbacks.onEose('ws://relay.test');

    expect(store.dnsState.availability).toBe('active');
    for (const family of FAMILIES) {
      expect(store.dnsState[family.collection]).toEqual([expect.objectContaining({ id: family.entityId })]);
    }
  });

  it('rejects cp-state events whose legacy_kind is not a DNS family', () => {
    const serviceState = cpStateEvent({ id: 'not-dns', legacyKind: 31961, d: 'svc-1:env-1', created_at: 100, content: { status: 'running' } });

    expect(store.applyDNSReadModelEvent(serviceState)).toBe(false);
    expect(store.dnsState.error.subscription).toBe('Unsupported DNS read-model schema bahia.cp-state.v1 (legacy_kind 31961)');
    for (const family of FAMILIES) expect(store.dnsState[family.collection]).toEqual([]);
  });

  it('rejects DNS state from a pubkey other than the Bahia service', () => {
    const [zone] = FAMILIES;
    const forged = cpStateEvent({ id: 'forged', d: zone.d, created_at: 100, pubkey: 'c'.repeat(64), ...zone.live(1) });

    expect(store.applyDNSReadModelEvent(forged)).toBe(false);
    expect(store.dnsState.zones).toEqual([]);
  });
});
