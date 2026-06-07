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
      trusted_relay_monitor_pubkeys: ['A'.repeat(64), 'not-a-key'],
      dm_relay_lists: [{ enabled: true, feature: 'Notifications', identity: 'Service', relays: ['wss://dm.example'] }],
      relay_administration: { enabled: true, targets: [{ ref: 'sidecar', relay_url: 'wss://sidecar.example', authorization: 'Bahia_Owned', administrator_pubkeys: ['b'.repeat(64)] }] }
    })).toMatchObject({
      schema: 'bahia.relay-settings.v1',
      browser_relays: ['wss://browser.example', 'wss://browser-2.example'],
      contextvm_relays: ['wss://contextvm.example'],
      service_relays: ['wss://service.example'],
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
});
