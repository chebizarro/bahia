import { beforeEach, describe, expect, it, vi } from 'vitest';

describe('browser-local relay override storage', () => {
  beforeEach(() => {
    localStorage.clear();
    vi.resetModules();
  });

  it('migrates the legacy array shape without losing compatible relay values', async () => {
    localStorage.setItem('bahia_nostr_relays', JSON.stringify([
      'wss://relay-one.example',
      'wss://relay-two.example/path'
    ]));

    const relaySettings = await import('../../src/lib/nostr/subscriptions.js');
    expect(relaySettings.getConfiguredRelays()).toEqual([
      'wss://relay-one.example/',
      'wss://relay-two.example/path'
    ]);

    expect(JSON.parse(localStorage.getItem('bahia_nostr_relays'))).toEqual({
      schema: 'bahia.browser-relay-override.v2',
      scope: 'browser-local-noncanonical',
      relays: ['wss://relay-one.example/', 'wss://relay-two.example/path']
    });
  });

  it('preserves compatible override values from an older object schema', async () => {
    localStorage.setItem('bahia_nostr_relays', JSON.stringify({
      schema: 'bahia.browser-relay-override.v1',
      relays: ['ws://local-relay.test']
    }));

    const relaySettings = await import('../../src/lib/nostr/subscriptions.js');
    expect(relaySettings.getConfiguredRelays()).toEqual(['ws://local-relay.test/']);
    expect(JSON.parse(localStorage.getItem('bahia_nostr_relays'))).toMatchObject({
      schema: relaySettings.RELAY_OVERRIDE_STORAGE_SCHEMA,
      scope: 'browser-local-noncanonical',
      relays: ['ws://local-relay.test/']
    });
  });

  it('scrubs unsafe values already present in the current envelope', async () => {
    localStorage.setItem('bahia_nostr_relays', JSON.stringify({
      schema: 'bahia.browser-relay-override.v2',
      scope: 'browser-local-noncanonical',
      relays: ['wss://safe.example', 'wss://operator:secret@unsafe.example']
    }));

    const relaySettings = await import('../../src/lib/nostr/subscriptions.js');
    expect(relaySettings.getConfiguredRelays()).toEqual(['wss://safe.example/']);
    const stored = localStorage.getItem('bahia_nostr_relays');
    expect(stored).not.toContain('secret');
    expect(JSON.parse(stored).relays).toEqual(['wss://safe.example/']);
  });

  it('replaces malformed stored values with a safe explicit empty override', async () => {
    localStorage.setItem('bahia_nostr_relays', '{"credential":"secret"');

    const relaySettings = await import('../../src/lib/nostr/subscriptions.js');
    expect(relaySettings.getConfiguredRelays()).toEqual([]);
    const stored = localStorage.getItem('bahia_nostr_relays');
    expect(stored).not.toContain('secret');
    expect(JSON.parse(stored)).toMatchObject({
      schema: relaySettings.RELAY_OVERRIDE_STORAGE_SCHEMA,
      scope: 'browser-local-noncanonical',
      relays: []
    });
  });

  it('never persists credential-bearing local relay URLs', async () => {
    const relaySettings = await import('../../src/lib/nostr/subscriptions.js');
    relaySettings.saveRelayConfig([
      'wss://safe.example',
      'wss://operator:secret@unsafe.example',
      'wss://unsafe.example/path?token=secret',
      'wss://unsafe.example/path#secret'
    ]);

    const stored = JSON.parse(localStorage.getItem('bahia_nostr_relays'));
    expect(stored.relays).toEqual(['wss://safe.example/']);
    expect(JSON.stringify(stored)).not.toContain('secret');
  });
});
