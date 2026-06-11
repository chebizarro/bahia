import { describe, it, expect, beforeEach, vi } from 'vitest';

const requestEncryptedResultMock = vi.hoisted(() => vi.fn());

vi.mock('../../src/lib/nostr/encrypted-controlplane.js', () => ({
  requestEncryptedResult: requestEncryptedResultMock
}));

describe('relay settings control-plane helpers', () => {
  let relaySettings;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    requestEncryptedResultMock.mockResolvedValue({ requestEventId: 'req-1', acceptedRelays: [], rejectedRelays: [], result: { status: 'accepted' } });
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

  it('sends policy changes through encrypted ContextVM', async () => {
    await relaySettings.applyRelayPolicy({ policy: { browser_relays: ['wss://browser.example'] } });
    expect(requestEncryptedResultMock).toHaveBeenCalledWith({
      operation: 'settings/relay-policy.apply',
      payload: expect.objectContaining({ browser_relays: ['wss://browser.example'] }),
      tags: [['domain', 'relay-settings'], ['action', 'relay_policy_apply']],
      signal: undefined
    });
  });

  it('sends NIP-86 admin calls through the relay-admin ContextVM method', async () => {
    await relaySettings.callRelayAdmin({ targetRef: 'sidecar', method: 'supportedmethods' });
    expect(requestEncryptedResultMock).toHaveBeenCalledWith({
      operation: 'settings/relay-admin.call',
      payload: { target_ref: 'sidecar', method: 'supportedmethods', params: [] },
      tags: [['domain', 'relay-settings'], ['action', 'relay_admin_call'], ['target', 'sidecar']],
      signal: undefined
    });
  });

  it('builds a scoped canonical relay-settings read-model filter', () => {
    expect(relaySettings.relayPolicyReadModelFilter({ servicePubkey: 'A'.repeat(64), since: 123 })).toEqual({
      kinds: [30900],
      '#d': ['relay-settings:operator'],
      '#domain': ['relay-settings'],
      '#schema': ['bahia.relay-settings.v1'],
      authors: ['a'.repeat(64)],
      since: 123,
      limit: 10
    });
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
