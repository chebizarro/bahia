import { describe, it, expect, beforeEach } from 'vitest';
import { finalizeEvent, generateSecretKey, getPublicKey } from 'nostr-tools';
import {
  DEPLOYMENT_INVENTORY_DOMAIN,
  DEPLOYMENT_INVENTORY_KIND,
  DEPLOYMENT_INVENTORY_SCHEMA,
  ENVIRONMENT_INVENTORY_ENTITY,
  TARGET_SCAN_ENTITY,
  buildDeploymentInventoryView,
  createDeploymentInventoryState,
  deploymentInventoryFilters,
  ingestDeploymentInventoryEvent,
  markUnconfirmedCacheEntries,
  restoreDeploymentInventoryCache,
  serializeDeploymentInventoryCache
} from '../../src/lib/deployment-inventory.js';
import {
  DEPLOYMENT_INVENTORY_CACHE_KEY,
  deploymentInventory,
  deploymentInventoryView,
  resetDeploymentInventoryStore,
  startDeploymentInventory,
  stopDeploymentInventory,
  whenDeploymentInventoryIdle
} from '../../src/lib/stores/deployment-inventory.svelte.js';

const SERVICE_KEY = generateSecretKey();
const SERVICE_PUBKEY = getPublicKey(SERVICE_KEY);
const ROGUE_KEY = generateSecretKey();
const PROD_ID = '11111111-1111-4111-8111-111111111111';
const STAGING_ID = '22222222-2222-4222-8222-222222222222';
const BAHIA_ID = '33333333-3333-4333-8333-333333333333';
const RELAY_ID = '44444444-4444-4444-8444-444444444444';
const WEB_ID = '55555555-5555-4555-8555-555555555555';
const NOW_MS = Date.now();
const NOW_S = Math.floor(NOW_MS / 1000);
const iso = (secondsAgo) => new Date(NOW_MS - secondsAgo * 1000).toISOString();
const digest = (ch) => `sha256:${ch.repeat(64)}`;
const trusted = { trustedPubkeys: [SERVICE_PUBKEY] };

function deployment(overrides = {}) {
  return {
    key: `${BAHIA_ID}:edge-01`,
    service: { id: BAHIA_ID, name: 'bahia' },
    deployment_unit: { id: '66666666-6666-4666-8666-666666666666', key: 'edge-01', target: 'edge-01-docker', ownership_mode: 'bahia_managed', implicit: false },
    runtime: { type: 'compose', target: 'bahia' },
    desired: { artifact_id: '77777777-7777-4777-8777-777777777777', image_ref: `ghcr.io/openagentsinc/bahia@${digest('a')}`, image_tag: 'v1.2.3', immutable: true },
    observed: { observation_id: '88888888-8888-4888-8888-888888888888', image_repo: 'ghcr.io/openagentsinc/bahia', image_digest: digest('a'), version: '1.2.3', health: 'healthy', source: 'compose', observed_at: iso(30) },
    coverage: 'observed',
    drift_status: 'in_sync',
    drift_evaluated: true,
    instances: [
      { target: 'bahia-1', supervisor: 'compose', status: 'healthy', observed_at: iso(30) },
      { target: 'bahia-2', supervisor: 'compose', status: 'restart_loop', observed_at: iso(30) }
    ],
    ...overrides
  };
}

function environmentPayload(id, name, deployments) {
  return {
    schema: DEPLOYMENT_INVENTORY_SCHEMA,
    entity: ENVIRONMENT_INVENTORY_ENTITY,
    complete: true,
    environment: { id, name },
    freshness: { stale_after_seconds: 720 },
    instance_coverage: 'supervised',
    summary: {},
    deployments
  };
}

// Events are round-tripped through JSON like relay and cache input, so
// nostr-tools' in-memory "already verified" marker cannot mask a forgery.
function signed(content, { key = SERVICE_KEY, d, entity = ENVIRONMENT_INVENTORY_ENTITY, createdAt = NOW_S, extraTags = [] } = {}) {
  return wire(finalizeEvent({
    kind: DEPLOYMENT_INVENTORY_KIND,
    created_at: createdAt,
    tags: [['d', d], ['domain', DEPLOYMENT_INVENTORY_DOMAIN], ['schema', DEPLOYMENT_INVENTORY_SCHEMA], ['entity', entity], ...extraTags],
    content: typeof content === 'string' ? content : JSON.stringify(content)
  }, key));
}

function wire(event) {
  return JSON.parse(JSON.stringify(event));
}

const envD = (id) => `deployment-inventory:environment:${id}`;
const scanD = (environment, target) => `deployment-inventory:target-scan:${environment}:${target}`;

function prodSnapshot(options = {}, deployments) {
  return signed(environmentPayload(PROD_ID, 'production', deployments || [
    deployment(),
    deployment({ key: `${RELAY_ID}:default`, service: { id: RELAY_ID, name: 'bahia-relay' }, deployment_unit: { key: 'default', implicit: true }, observed: { ...deployment().observed, health: 'stopped' }, drift_status: 'drifted', instances: [] }),
    deployment({ key: `${WEB_ID}:default`, service: { id: WEB_ID, name: 'bahia-web' }, deployment_unit: { key: 'default', implicit: true }, observed: undefined, coverage: 'desired_only', drift_status: 'unknown', drift_evaluated: false, instances: [] })
  ]), { d: envD(PROD_ID), ...options });
}

function scanEvent(payload, options = {}) {
  return signed({
    schema: DEPLOYMENT_INVENTORY_SCHEMA,
    entity: TARGET_SCAN_ENTITY,
    environment: 'production',
    target: 'edge-01',
    endpoint_ref: 'edge-01-docker',
    scan_state: 'complete',
    scanned_at: iso(60),
    freshness: { stale_after_seconds: 720 },
    counts: { total: 5, managed: 3, unmanaged: 2 },
    ...payload
  }, { d: scanD(payload?.environment || 'production', payload?.target || 'edge-01'), entity: TARGET_SCAN_ENTITY, ...options });
}

function productionRows(state, nowMs = NOW_MS) {
  const view = buildDeploymentInventoryView(state, { nowMs });
  return view.environments.find((environment) => environment.name === 'production')?.inventory?.deployments || [];
}

describe('deployment inventory protocol', () => {
  it('builds scoped relay filters for trusted authors only', () => {
    expect(deploymentInventoryFilters([SERVICE_PUBKEY])).toEqual([{ kinds: [30900], authors: [SERVICE_PUBKEY], '#domain': ['deployment-inventory'] }]);
    expect(() => deploymentInventoryFilters([])).toThrow(/trusted service pubkey/);
  });

  it('renders signed observed, stopped, desired-only, and multi-instance deployments per environment', async () => {
    const state = createDeploymentInventoryState();
    expect((await ingestDeploymentInventoryEvent(state, prodSnapshot(), trusted)).accepted).toBe(true);
    const staging = signed(environmentPayload(STAGING_ID, 'staging', [deployment({ observed: { ...deployment().observed, image_digest: digest('b'), version: '1.3.0-rc1' }, instances: [] })]), { d: envD(STAGING_ID) });
    expect((await ingestDeploymentInventoryEvent(state, staging, trusted)).accepted).toBe(true);

    const view = buildDeploymentInventoryView(state, { nowMs: NOW_MS });
    expect(view.environments.map((environment) => environment.name)).toEqual(['production', 'staging']);
    const [bahia, relay, web] = productionRows(state);
    expect(bahia).toMatchObject({ service: 'bahia', unit: 'edge-01', target: 'edge-01-docker', desiredRef: `ghcr.io/openagentsinc/bahia@${digest('a')}`, desiredImmutable: true, observedVersion: '1.2.3', health: 'healthy', drift: 'in_sync', stale: false, badges: [] });
    expect(bahia.instances.map((instance) => [instance.target, instance.status])).toEqual([['bahia-1', 'healthy'], ['bahia-2', 'restart_loop']]);
    expect(relay).toMatchObject({ service: 'bahia-relay', health: 'stopped', unitImplicit: true });
    expect(relay.badges).toEqual(expect.arrayContaining(['stopped', 'drifted']));
    expect(web).toMatchObject({ service: 'bahia-web', coverage: 'desired_only', health: 'not_observed', observedRef: '' });
    expect(web.badges).toContain('desired_only');
    const stagingRows = view.environments[1].inventory.deployments;
    expect(stagingRows).toHaveLength(1);
    expect(stagingRows[0].observedVersion).toBe('1.3.0-rc1');
    expect(view.environments[0].inventory.provenance).toBe('relay');
  });

  it('rejects untrusted authors even with valid signatures', async () => {
    const state = createDeploymentInventoryState();
    const result = await ingestDeploymentInventoryEvent(state, prodSnapshot({ key: ROGUE_KEY }), trusted);
    expect(result).toMatchObject({ accepted: false, reason: 'untrusted author' });
    expect(buildDeploymentInventoryView(state).environments).toEqual([]);
  });

  it('rejects events whose content or signature was tampered with', async () => {
    const state = createDeploymentInventoryState();
    const event = prodSnapshot();
    const tampered = { ...event, content: event.content.replace('in_sync', 'drifted') };
    expect((await ingestDeploymentInventoryEvent(state, tampered, trusted)).reason).toMatch(/invalid event: event id/);
    const forgedSig = { ...event, sig: event.sig.slice(0, -2) + (event.sig.endsWith('00') ? '11' : '00') };
    expect((await ingestDeploymentInventoryEvent(state, forgedSig, trusted)).reason).toMatch(/invalid event: event signature/);
    expect(state.entries.size).toBe(0);
  });

  it('applies NIP-01 addressable ordering: newer wins, older replays lose, ties keep the lowest id', async () => {
    const state = createDeploymentInventoryState();
    const older = prodSnapshot({ createdAt: NOW_S - 60 });
    const newer = prodSnapshot({ createdAt: NOW_S }, [deployment({ observed: { ...deployment().observed, image_digest: digest('7'), version: '1.1.9' } })]);
    await ingestDeploymentInventoryEvent(state, older, trusted);
    expect((await ingestDeploymentInventoryEvent(state, newer, trusted)).accepted).toBe(true);
    expect((await ingestDeploymentInventoryEvent(state, older, trusted)).reason).toBe('superseded');
    expect(productionRows(state)[0].observedVersion).toBe('1.1.9');

    const tieA = prodSnapshot({ createdAt: NOW_S + 1 }, [deployment({ observed: { ...deployment().observed, version: 'tie-a' } })]);
    const tieB = prodSnapshot({ createdAt: NOW_S + 1 }, [deployment({ observed: { ...deployment().observed, version: 'tie-b' } })]);
    const [low, high] = tieA.id < tieB.id ? [tieA, tieB] : [tieB, tieA];
    await ingestDeploymentInventoryEvent(state, high, trusted);
    await ingestDeploymentInventoryEvent(state, low, trusted);
    await ingestDeploymentInventoryEvent(state, high, trusted);
    expect(productionRows(state)[0].observedVersion).toBe(JSON.parse(low.content).deployments[0].observed.version);
  });

  it('removes tombstoned environments and ignores older snapshots replayed afterwards', async () => {
    const state = createDeploymentInventoryState();
    const snapshot = prodSnapshot({ createdAt: NOW_S - 10 });
    await ingestDeploymentInventoryEvent(state, snapshot, trusted);
    const tombstone = signed({ schema: DEPLOYMENT_INVENTORY_SCHEMA, entity: ENVIRONMENT_INVENTORY_ENTITY, deleted: true, environment: { id: PROD_ID } }, { d: envD(PROD_ID), createdAt: NOW_S, extraTags: [['deleted', 'true']] });
    expect(await ingestDeploymentInventoryEvent(state, tombstone, trusted)).toMatchObject({ accepted: true, deleted: true });
    expect(buildDeploymentInventoryView(state).environments).toEqual([]);
    expect((await ingestDeploymentInventoryEvent(state, snapshot, trusted)).reason).toBe('superseded');
    expect(buildDeploymentInventoryView(state).environments).toEqual([]);
  });

  it('rejects malformed payloads without clobbering the last valid snapshot', async () => {
    const state = createDeploymentInventoryState();
    await ingestDeploymentInventoryEvent(state, prodSnapshot({ createdAt: NOW_S - 30 }), trusted);
    const malformed = [
      signed('{not json', { d: envD(PROD_ID) }),
      signed({ ...environmentPayload(PROD_ID, 'production', []), schema: 'bahia.other.v1' }, { d: envD(PROD_ID) }),
      signed(environmentPayload(STAGING_ID, 'production', []), { d: envD(PROD_ID) }),
      signed({ ...environmentPayload(PROD_ID, 'production', []), complete: false }, { d: envD(PROD_ID) }),
      signed({ ...environmentPayload(PROD_ID, 'production', []), freshness: {} }, { d: envD(PROD_ID) }),
      signed(environmentPayload(PROD_ID, 'production', [{ ...deployment(), observed: { health: 'healthy' } }]), { d: envD(PROD_ID) }),
      signed(environmentPayload(PROD_ID, 'production', [{ ...deployment(), coverage: 'deployed' }]), { d: envD(PROD_ID) })
    ];
    for (const event of malformed) {
      expect((await ingestDeploymentInventoryEvent(state, event, trusted)).reason).toMatch(/malformed payload/);
    }
    expect(productionRows(state)).toHaveLength(3);
    expect(state.rejected).toHaveLength(malformed.length);
  });

  it('marks observations and instances stale from the published freshness budget', async () => {
    const state = createDeploymentInventoryState();
    await ingestDeploymentInventoryEvent(state, prodSnapshot(), trusted);
    expect(productionRows(state, NOW_MS)[0].stale).toBe(false);
    const later = NOW_MS + 721 * 1000;
    const [bahia] = productionRows(state, later);
    expect(bahia.stale).toBe(true);
    expect(bahia.badges).toContain('stale');
    expect(bahia.instances.every((instance) => instance.stale)).toBe(true);
  });

  it('shows only redacted unmanaged aggregates for runtime target scans', async () => {
    const state = createDeploymentInventoryState();
    const withSmuggledDetail = scanEvent({ containers: [{ container_id: 'aaaa1111', image: 'postgres', host: 'tcp://10.0.0.5:2376' }] });
    expect((await ingestDeploymentInventoryEvent(state, withSmuggledDetail, trusted)).accepted).toBe(true);
    const unavailable = scanEvent({ target: 'edge-02', endpoint_ref: undefined, scan_state: 'unavailable', counts: undefined });
    expect((await ingestDeploymentInventoryEvent(state, unavailable, trusted)).accepted).toBe(true);
    const claimsCounts = scanEvent({ target: 'edge-03', scan_state: 'unavailable' });
    expect((await ingestDeploymentInventoryEvent(state, claimsCounts, trusted)).reason).toMatch(/unavailable scan must not claim counts/);
    const inconsistent = scanEvent({ target: 'edge-04', counts: { total: 9, managed: 3, unmanaged: 2 } });
    expect((await ingestDeploymentInventoryEvent(state, inconsistent, trusted)).reason).toMatch(/scan counts malformed/);

    const view = buildDeploymentInventoryView(state, { nowMs: NOW_MS });
    const production = view.environments.find((environment) => environment.name === 'production');
    expect(production.inventory).toBeNull();
    expect(production.targetScans.map((scan) => [scan.target, scan.state, scan.counts])).toEqual([
      ['edge-01', 'complete', { total: 5, managed: 3, unmanaged: 2 }],
      ['edge-02', 'unavailable', null]
    ]);
    const rendered = JSON.stringify(view);
    for (const detail of ['aaaa1111', 'postgres', '10.0.0.5', 'containers']) {
      expect(rendered).not.toContain(detail);
    }
    expect(buildDeploymentInventoryView(state, { nowMs: NOW_MS + 3600 * 1000 }).environments[0].targetScans[0].state).toBe('stale');
  });

  it('re-verifies cached events and flags cache-only entries after relay EOSE', async () => {
    const source = createDeploymentInventoryState();
    await ingestDeploymentInventoryEvent(source, prodSnapshot(), trusted);
    await ingestDeploymentInventoryEvent(source, scanEvent({}), trusted);
    const raw = serializeDeploymentInventoryCache(source);

    const restoredState = createDeploymentInventoryState();
    expect(await restoreDeploymentInventoryCache(restoredState, raw, trusted)).toEqual({ restored: 2, rejected: 0 });
    expect(buildDeploymentInventoryView(restoredState, { nowMs: NOW_MS }).environments[0].inventory.provenance).toBe('cache');

    const cached = JSON.parse(raw);
    cached.events[0] = { ...cached.events[0], content: cached.events[0].content.replace('bahia-relay', 'evil-relay') };
    const tamperedState = createDeploymentInventoryState();
    expect(await restoreDeploymentInventoryCache(tamperedState, JSON.stringify(cached), trusted)).toEqual({ restored: 1, rejected: 1 });

    const rotatedTrust = createDeploymentInventoryState();
    expect(await restoreDeploymentInventoryCache(rotatedTrust, raw, { trustedPubkeys: [getPublicKey(ROGUE_KEY)] })).toEqual({ restored: 0, rejected: 2 });

    expect(markUnconfirmedCacheEntries(restoredState)).toBe(2);
    expect(buildDeploymentInventoryView(restoredState, { nowMs: NOW_MS }).environments[0].inventory.provenance).toBe('cache_unconfirmed');
    const redelivered = JSON.parse(raw).events.find((event) => event.tags.some((tag) => tag[0] === 'entity' && tag[1] === ENVIRONMENT_INVENTORY_ENTITY));
    expect((await ingestDeploymentInventoryEvent(restoredState, redelivered, trusted)).reason).toBe('confirmed');
    expect(buildDeploymentInventoryView(restoredState, { nowMs: NOW_MS }).environments[0].inventory.provenance).toBe('relay');
  });
});

function fakeRetainedClient() {
  const subscriptions = [];
  return {
    subscriptions,
    getRelays: () => ['wss://relay.test'],
    subscribeWithRecovery(filters, handlers) {
      const subscription = { filters, handlers, closed: false };
      subscriptions.push(subscription);
      return () => { subscription.closed = true; };
    }
  };
}

function memoryStorage(initial = {}) {
  const values = { ...initial };
  return {
    values,
    getItem: (key) => (key in values ? values[key] : null),
    setItem: (key, value) => { values[key] = String(value); }
  };
}

describe('deployment inventory store', () => {
  beforeEach(() => resetDeploymentInventoryStore());

  it('restores verified cache, confirms via relay, survives reconnect replays, and closes on stop', async () => {
    const seedState = createDeploymentInventoryState();
    const cachedSnapshot = prodSnapshot({ createdAt: NOW_S - 120 });
    await ingestDeploymentInventoryEvent(seedState, cachedSnapshot, trusted);
    const storage = memoryStorage({ [DEPLOYMENT_INVENTORY_CACHE_KEY]: serializeDeploymentInventoryCache(seedState) });
    const client = fakeRetainedClient();

    await startDeploymentInventory({ client, connect: async () => {}, seed: { service_pubkeys: [SERVICE_PUBKEY] }, storage });
    expect(deploymentInventory.status).toBe('cached');
    expect(deploymentInventoryView(NOW_MS).environments[0].inventory.provenance).toBe('cache');
    expect(client.subscriptions).toHaveLength(1);
    expect(client.subscriptions[0].filters).toEqual(deploymentInventoryFilters([SERVICE_PUBKEY]));
    const { handlers } = client.subscriptions[0];

    handlers.onEvent(cachedSnapshot, 'wss://relay.test');
    handlers.onEose('wss://relay.test');
    await whenDeploymentInventoryIdle();
    expect(deploymentInventory.status).toBe('ready');
    expect(deploymentInventory.caughtUp).toBe(true);
    expect(deploymentInventory.unconfirmedCount).toBe(0);
    expect(deploymentInventoryView(NOW_MS).environments[0].inventory.provenance).toBe('relay');

    handlers.onClosed('connection reset', 'wss://relay.test', { terminal: false, recovering: true, retryInMs: 500 });
    expect(deploymentInventory.reconnecting).toBe(true);
    expect(deploymentInventoryView(NOW_MS).environments[0].inventory.deployments).toHaveLength(3);

    // The recovered subscription replays the overlap second, then delivers
    // the rollback published while disconnected.
    const revision = deploymentInventory.revision;
    handlers.onEvent(cachedSnapshot, 'wss://relay.test');
    const rollback = prodSnapshot({ createdAt: NOW_S }, [deployment({ observed: { ...deployment().observed, image_digest: digest('6'), version: '1.2.2' }, drift_status: 'drifted' })]);
    handlers.onEvent(rollback, 'wss://relay.test');
    handlers.onEose('wss://relay.test');
    await whenDeploymentInventoryIdle();
    expect(deploymentInventory.reconnecting).toBe(false);
    expect(deploymentInventory.revision).toBeGreaterThan(revision);
    const rows = deploymentInventoryView(NOW_MS).environments[0].inventory.deployments;
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({ observedVersion: '1.2.2', drift: 'drifted' });
    expect(JSON.parse(storage.values[DEPLOYMENT_INVENTORY_CACHE_KEY]).events.map((event) => event.id)).toEqual([rollback.id]);

    handlers.onEvent(prodSnapshot({ key: ROGUE_KEY, createdAt: NOW_S + 5 }), 'wss://relay.test');
    await whenDeploymentInventoryIdle();
    expect(deploymentInventory.rejectedCount).toBe(1);
    expect(deploymentInventoryView(NOW_MS).environments[0].inventory.deployments[0].observedVersion).toBe('1.2.2');

    stopDeploymentInventory();
    expect(client.subscriptions[0].closed).toBe(true);
  });

  it('refuses to render inventory without a trusted service key', async () => {
    await startDeploymentInventory({ client: fakeRetainedClient(), connect: async () => {}, seed: { service_pubkeys: [] }, storage: memoryStorage() });
    expect(deploymentInventory.status).toBe('error');
    expect(deploymentInventory.error).toMatch(/trusted Bahia service pubkeys/);
  });

  it('closes a subscription that resolves after the page already stopped it', async () => {
    const client = fakeRetainedClient();
    let release;
    const gate = new Promise((resolve) => { release = resolve; });
    const started = startDeploymentInventory({ client, connect: () => gate, seed: { service_pubkeys: [SERVICE_PUBKEY] }, storage: memoryStorage() });
    stopDeploymentInventory();
    release();
    await started;
    expect(client.subscriptions).toHaveLength(1);
    expect(client.subscriptions[0].closed).toBe(true);
  });
});
