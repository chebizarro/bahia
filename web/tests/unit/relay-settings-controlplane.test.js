import { describe, it, expect, beforeEach, vi } from 'vitest';
import { matchFilter } from 'nostr-tools/filter';
import { RELAY_SETTINGS_TOPIC } from '../../src/lib/nostr/kinds.gen.js';

const submitSensitiveIntentMock = vi.hoisted(() => vi.fn(async request => ({ intentId: request.intentId, pending: true })));
const queryMock = vi.hoisted(() => vi.fn(() => []));
vi.mock('../../src/lib/stores/sensitive-intents.svelte.js', () => ({ submitSensitiveIntent: submitSensitiveIntentMock }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getEventStore: () => ({ query: queryMock }), getServicePubkey: () => 'b'.repeat(64)
}));

describe('relay settings control-plane helpers', () => {
  let relaySettings;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    queryMock.mockReturnValue([]);
    relaySettings = await import('../../src/lib/nostr/relay-settings-controlplane.js');
  });

  it('normalizes relay policy payloads by purpose', () => {
    expect(relaySettings.buildRelayPolicyPayload({
      browser_relays: ['wss://browser.example', 'wss://browser.example, wss://browser-2.example'],
      contextvm_relays: ['wss://contextvm.example'],
      service_relays: ['wss://service.example'],
      nip34_relays: ['wss://nip34.example'],
      trusted_relay_monitor_pubkeys: ['A'.repeat(64), 'not-a-key'],
      dm_relay_lists: [{ enabled: true, feature: 'Notifications', identity: 'Service', relays: ['wss://dm.example'] }],
      relay_administration: { enabled: true, targets: [{ ref: 'sidecar', relay_url: 'wss://sidecar.example', authorization: 'Bahia_Owned', administrator_pubkeys: ['b'.repeat(64)] }] }
    })).toMatchObject({
      schema: 'bahia.relay-settings.v1',
      browser_relays: ['wss://browser.example', 'wss://browser-2.example'],
      contextvm_relays: ['wss://contextvm.example'],
      service_relays: ['wss://service.example'],
      nip34_relays: ['wss://nip34.example'],
      trusted_relay_monitor_pubkeys: ['a'.repeat(64)],
      dm_relay_lists: [{ enabled: true, feature: 'notifications', identity: 'service', relays: ['wss://dm.example'] }],
      relay_administration: { enabled: true, targets: [{ ref: 'sidecar', relay_url: 'wss://sidecar.example', authorization: 'bahia_owned', administrator_pubkeys: ['b'.repeat(64)] }] }
    });
  });

  it('submits a NIP-59-protected relay policy intent with audited preconditions', async () => {
    await relaySettings.applyRelayPolicy({
      policy: { browser_relays: ['wss://replacement.example'] },
      expectedProjection: { availability: 'available', event_id: 'a'.repeat(64), hash: 'b'.repeat(64) },
      replacementConfirmation: { confirmed: true, previous_truth_state: 'unavailable',
        reason_code: 'relay_hydration_unavailable', change_reference: 'INC-42' }
    });
    const request = submitSensitiveIntentMock.mock.calls[0][0];
    expect(request).toMatchObject({ domain: 'relay', op: 'policy-set', coordinate: 'relay-settings:operator',
      content: { schema: 'bahia.relay-settings.v1', browser_relays: ['wss://replacement.example'],
        intent_id: request.intentId,
        expected_projection: { availability: 'available', event_id: 'a'.repeat(64), hash: 'b'.repeat(64) },
        replacement_confirmation: { confirmed: true, change_reference: 'INC-42' } } });
  });

  it('normalizes projection truth states without collapsing absence into empty policy', () => {
    expect(relaySettings.normalizeRelayPolicyProjectionResponse({
      status: 'unavailable',
      truth_state: 'unavailable',
      canonical_policy: null,
      server_projection: { availability: 'unavailable', freshness: 'unavailable' }
    })).toMatchObject({ truthState: 'unavailable', policy: null });

    expect(relaySettings.normalizeRelayPolicyProjectionResponse({
      status: 'never-configured',
      truth_state: 'never-configured',
      canonical_policy: null,
      server_projection: { availability: 'never-configured', freshness: 'not-applicable' }
    })).toMatchObject({ truthState: 'never-configured', policy: null });

    expect(relaySettings.normalizeRelayPolicyProjectionResponse({
      status: 'ok',
      truth_state: 'loaded-stale',
      canonical_policy: { browser_relays: ['wss://cached.example'] },
      server_projection: {
        event_id: 'a'.repeat(64),
        event_created_at: '2026-08-03T00:00:00Z',
        hash: 'b'.repeat(64),
        source_relay: 'wss://relay.example/path?credential=redacted',
        last_sync_at: '2026-08-03T01:02:03Z',
        freshness: 'stale'
      }
    })).toEqual({
      truthState: 'loaded-stale',
      policy: expect.objectContaining({ browser_relays: ['wss://cached.example'] }),
      provenance: {
        event_id: 'a'.repeat(64),
        event_created_at: '2026-08-03T00:00:00Z',
        hash: 'b'.repeat(64),
        source_relay: 'wss://relay.example/path',
        last_sync_at: '2026-08-03T01:02:03Z',
        freshness: 'stale',
        source: ''
      }
    });

    expect(relaySettings.normalizeRelayPolicyProjectionResponse({
      status: 'ok',
      truth_state: 'intentionally-empty',
      canonical_policy: {},
      server_projection: { freshness: 'fresh' }
    })).toMatchObject({ truthState: 'intentionally-empty', policy: expect.any(Object) });
  });

  it('classifies validated live signed-empty separately from unavailable', () => {
    const live = relaySettings.liveRelayPolicyTruth({}, {
      event: { id: 'c'.repeat(64) },
      relay: 'wss://live.example/path?ignored=value',
      receivedAt: '2026-08-03T02:03:04Z'
    });
    expect(live).toMatchObject({
      truthState: 'intentionally-empty',
      provenance: {
        event_id: 'c'.repeat(64),
        source_relay: 'wss://live.example/path',
        last_sync_at: '2026-08-03T02:03:04Z',
        freshness: 'live'
      }
    });
  });

  it('orders cached and live candidates by replaceable-event semantics', () => {
    const newer = { provenance: { event_created_at: '2026-08-03T00:00:02Z', event_id: 'b'.repeat(64) } };
    const older = { provenance: { event_created_at: '2026-08-03T00:00:01Z', event_id: 'a'.repeat(64) } };
    const equalTimeLowerID = { provenance: { event_created_at: '2026-08-03T00:00:02Z', event_id: 'a'.repeat(64) } };

    expect(relaySettings.compareRelayPolicyTruthCandidates(newer, older)).toBe(1);
    expect(relaySettings.compareRelayPolicyTruthCandidates(older, newer)).toBe(-1);
    expect(relaySettings.compareRelayPolicyTruthCandidates(equalTimeLowerID, newer)).toBe(1);
  });

  it('builds a scoped canonical relay-settings read-model filter', () => {
    // Relays index single-letter tags only (bahia-irsry.37): the exact
    // coordinate is named by #d; domain and schema are checked locally.
    expect(relaySettings.relayPolicyReadModelFilter({ servicePubkey: 'A'.repeat(64), since: 123 })).toEqual({
      kinds: [30900],
      '#d': ['relay-settings:operator'],
      authors: ['a'.repeat(64)],
      since: 123,
      limit: 10
    });
  });

  it('matches producer-shaped and pre-topic relay-settings state with the #d REQ', () => {
    const servicePubkey = 'd'.repeat(64);
    const filter = relaySettings.relayPolicyReadModelFilter({ servicePubkey });
    for (const key of Object.keys(filter).filter((name) => name.startsWith('#'))) {
      expect(key).toHaveLength(2);
    }
    const content = JSON.stringify({ schema: 'bahia.relay-settings.v1', browser_relays: ['wss://browser.example'], updated_at: '2026-09-30T00:00:00Z' });
    // Tags as internal/controlplane relaySettingsStateTags stamps them.
    const produced = {
      id: 'e'.repeat(64),
      kind: 30900,
      pubkey: servicePubkey,
      created_at: 200,
      tags: [['d', 'relay-settings:operator'], ['domain', 'relay-settings'], ['entity', 'relay-policy'], ['schema', 'bahia.relay-settings.v1'], ['t', RELAY_SETTINGS_TOPIC], ['status', 'applied'], ['p', 'f'.repeat(64)]],
      content
    };
    const pretopic = { ...produced, id: 'f'.repeat(64), created_at: 100, tags: produced.tags.filter((tag) => tag[0] !== 't') };
    for (const event of [produced, pretopic]) {
      expect(matchFilter(filter, event)).toBe(true);
      expect(relaySettings.parseRelayPolicyStateEvent(event, { servicePubkey })).not.toBeNull();
    }
  });

  it('parses only trusted canonical relay-settings state events', () => {
    const servicePubkey = 'b'.repeat(64);
    const event = {
      id: 'evt-1',
      kind: 30900,
      pubkey: servicePubkey,
      created_at: 100,
      tags: [['d', 'relay-settings:operator'], ['domain', 'relay-settings'], ['schema', 'bahia.relay-settings.v1']],
      content: JSON.stringify({
        schema: 'bahia.relay-settings.v1',
        browser_relays: ['wss://browser.example'],
        contextvm_relays: ['wss://contextvm.example'],
        service_relays: ['wss://service.example'],
        updated_at: '2026-06-07T00:00:00Z'
      })
    };

    expect(relaySettings.parseRelayPolicyStateEvent(event, { servicePubkey })).toMatchObject({
      schema: 'bahia.relay-settings.v1',
      browser_relays: ['wss://browser.example'],
      contextvm_relays: ['wss://contextvm.example'],
      service_relays: ['wss://service.example'],
      nip34_relays: [],
      updated_at: '2026-06-07T00:00:00Z'
    });
    expect(relaySettings.parseRelayPolicyStateEvent({ ...event, pubkey: 'c'.repeat(64) }, { servicePubkey })).toBeNull();
    expect(relaySettings.parseRelayPolicyStateEvent({ ...event, tags: [['d', 'wrong'], ['domain', 'relay-settings'], ['schema', 'bahia.relay-settings.v1']] }, { servicePubkey })).toBeNull();
  });

  it('accepts valid canonical states with no browser/contextvm/service relay topology', () => {
    const servicePubkey = 'e'.repeat(64);
    const event = relaySettingsStateEvent({
      id: 'evt-dm-only',
      servicePubkey,
      createdAt: 101,
      browserRelays: [],
      content: {
        schema: 'bahia.relay-settings.v1',
        browser_relays: [],
        contextvm_relays: [],
        service_relays: [],
        dm_relay_lists: [{ enabled: true, feature: 'notifications', identity: 'service', relays: ['wss://dm.example'] }],
        relay_administration: { enabled: true, targets: [{ ref: 'sidecar', relay_url: 'wss://sidecar.example', authorization: 'bahia_owned', administrator_pubkeys: ['f'.repeat(64)] }] }
      }
    });

    expect(relaySettings.parseRelayPolicyStateEvent(event, { servicePubkey })).toMatchObject({
      browser_relays: [],
      contextvm_relays: [],
      service_relays: [],
      dm_relay_lists: [{ enabled: true, feature: 'notifications', identity: 'service', relays: ['wss://dm.example'] }],
      relay_administration: { enabled: true, targets: [{ ref: 'sidecar', relay_url: 'wss://sidecar.example', authorization: 'bahia_owned', administrator_pubkeys: ['f'.repeat(64)] }] }
    });
  });

  it('tie-breaks equal created_at replaceable state by lowest event id', () => {
    const servicePubkey = 'd'.repeat(64);
    const higherId = relaySettingsStateEvent({ id: 'bbbb', servicePubkey, createdAt: 100, browserRelays: ['wss://higher-id.example'] });
    const lowerId = relaySettingsStateEvent({ id: 'aaaa', servicePubkey, createdAt: 100, browserRelays: ['wss://lower-id.example'] });
    const states = [];
    const fakeClient = {
      subscribeOnRelays: vi.fn((_relays, _filters, handlers) => {
        handlers.onEvent(higherId, 'wss://relay.example');
        handlers.onEvent(lowerId, 'wss://relay.example');
        return () => {};
      })
    };

    relaySettings.subscribeRelayPolicyReadModel({
      client: fakeClient,
      relays: ['wss://relay.example'],
      servicePubkey,
      onState: (state) => states.push(state)
    });

    expect(states.map((state) => state.browser_relays[0])).toEqual(['wss://higher-id.example', 'wss://lower-id.example']);
  });

  it('subscribes to relay-settings state, applies latest replaceable event, and forwards EOSE/CLOSED/AUTH', () => {
    const servicePubkey = 'd'.repeat(64);
    const older = relaySettingsStateEvent({ id: 'evt-1', servicePubkey, createdAt: 100, browserRelays: ['wss://old.example'] });
    const newer = relaySettingsStateEvent({ id: 'evt-2', servicePubkey, createdAt: 101, browserRelays: ['wss://new.example'] });
    const states = [];
    const eose = vi.fn();
    const closed = vi.fn();
    const auth = vi.fn();
    let unsubscribed = false;
    const fakeClient = {
      subscribeOnRelays: vi.fn((relays, filters, handlers) => {
        handlers.onEvent(newer, 'wss://relay.example');
        handlers.onEvent(older, 'wss://relay.example');
        handlers.onEose('wss://relay.example');
        handlers.onClosed('auth-required: sign', 'wss://relay.example', { authRequired: true });
        handlers.onAuth('auth-required: sign', 'wss://relay.example');
        return () => { unsubscribed = true; };
      })
    };

    const unsubscribe = relaySettings.subscribeRelayPolicyReadModel({
      client: fakeClient,
      relays: ['wss://relay.example'],
      servicePubkey,
      onState: (state) => states.push(state),
      onEose: eose,
      onClosed: closed,
      onAuth: auth
    });

    expect(fakeClient.subscribeOnRelays).toHaveBeenCalledWith(
      ['wss://relay.example'],
      [expect.objectContaining({ kinds: [30900], authors: [servicePubkey], '#d': ['relay-settings:operator'] })],
      expect.objectContaining({ onEvent: expect.any(Function), onEose: eose, onClosed: closed, onAuth: auth })
    );
    expect(states).toHaveLength(1);
    expect(states[0].browser_relays).toEqual(['wss://new.example']);
    expect(eose).toHaveBeenCalledWith('wss://relay.example');
    expect(closed).toHaveBeenCalledWith('auth-required: sign', 'wss://relay.example', { authRequired: true });
    expect(auth).toHaveBeenCalledWith('auth-required: sign', 'wss://relay.example');
    unsubscribe();
    expect(unsubscribed).toBe(true);
  });

  it('hydrates policy only from the trusted relay-settings cp-state family in the store', async () => {
    const event = relaySettingsStateEvent({ id: 'event-a', servicePubkey: 'b'.repeat(64),
      createdAt: 100, browserRelays: ['wss://new.example'] });
    queryMock.mockReturnValue([event]);
    expect(await relaySettings.getRelayPolicy()).toMatchObject({
      truth_state: 'loaded-cached', canonical_policy: { browser_relays: ['wss://new.example'] },
      server_projection: { event_id: 'event-a' }
    });
    expect(queryMock).toHaveBeenCalledWith(expect.objectContaining({
      kinds: [30900], authors: ['b'.repeat(64)], '#d': ['relay-settings:operator']
    }));
    queryMock.mockReturnValue([]);
    expect(await relaySettings.getRelayPolicy()).toMatchObject({ truth_state: 'loading', canonical_policy: null });
  });

});

function relaySettingsStateEvent({ id, servicePubkey, createdAt, browserRelays, content }) {
  return {
    id,
    kind: 30900,
    pubkey: servicePubkey,
    created_at: createdAt,
    tags: [['d', 'relay-settings:operator'], ['domain', 'relay-settings'], ['schema', 'bahia.relay-settings.v1']],
    content: JSON.stringify(content || {
      schema: 'bahia.relay-settings.v1',
      browser_relays: browserRelays,
      contextvm_relays: [],
      service_relays: []
    })
  };
}

describe('createProjectionHydrationGuard', () => {
  let guard;

  beforeEach(async () => {
    const mod = await import('../../src/lib/nostr/relay-settings-controlplane.js');
    guard = mod.createProjectionHydrationGuard();
  });

  it('allows the first acquire for a key', () => {
    expect(guard.acquire('key-A')).toBe(true);
    expect(guard.currentKey).toBe('key-A');
  });

  it('blocks duplicate acquire for the same key (two triggers, one request)', () => {
    expect(guard.acquire('key-A')).toBe(true);
    expect(guard.acquire('key-A')).toBe(false);
  });

  it('allows a new key after reset (key change triggers new request)', () => {
    expect(guard.acquire('key-A')).toBe(true);
    guard.reset();
    expect(guard.currentKey).toBe('');
    expect(guard.acquire('key-B')).toBe(true);
    expect(guard.currentKey).toBe('key-B');
  });

  it('allows retry after release (error recovery)', () => {
    expect(guard.acquire('key-A')).toBe(true);
    guard.release('key-A');
    expect(guard.acquire('key-A')).toBe(true);
  });

  it('ignores release with a mismatched key', () => {
    expect(guard.acquire('key-A')).toBe(true);
    guard.release('key-B');
    expect(guard.acquire('key-A')).toBe(false);
    expect(guard.currentKey).toBe('key-A');
  });

  it('rejects empty or falsy keys', () => {
    expect(guard.acquire('')).toBe(false);
    expect(guard.acquire(null)).toBe(false);
    expect(guard.acquire(undefined)).toBe(false);
    expect(guard.currentKey).toBe('');
  });

  it('allows a different key without reset (supersedes previous)', () => {
    expect(guard.acquire('key-A')).toBe(true);
    expect(guard.acquire('key-B')).toBe(true);
    expect(guard.currentKey).toBe('key-B');
    // key-A is no longer guarded; key-B is
    expect(guard.acquire('key-B')).toBe(false);
    expect(guard.acquire('key-A')).toBe(true);
    expect(guard.currentKey).toBe('key-A');
  });
});
