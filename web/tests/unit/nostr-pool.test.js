import { describe, it, expect, vi } from 'vitest';
import { get } from 'svelte/store';
import { createNostrPoolClient } from '../../src/lib/nostr/pool.js';

function createRelay(url, { connected = true } = {}) {
  return { url, connected };
}

function createPool(relays = []) {
  const relayMap = new Map(relays.map((relay) => [relay.url, relay]));
  return {
    ensureRelay: vi.fn(async (url) => {
      if (!relayMap.has(url)) relayMap.set(url, createRelay(url));
      return relayMap.get(url);
    }),
    listConnectionStatus: vi.fn(() => new Map(Array.from(relayMap.entries()).map(([url, relay]) => [url, relay.connected]))),
    close: vi.fn(),
    destroy: vi.fn()
  };
}

describe('PoolBackedClient connect lifecycle', () => {
  it('reuses an in-flight eager connection for the same relay set', async () => {
    const relay = createRelay('wss://relay.example', { connected: false });
    const pool = createPool([relay]);
    let resolveConnection;
    pool.ensureRelay.mockImplementationOnce(() => new Promise((resolve) => {
      resolveConnection = () => {
        relay.connected = true;
        resolve(relay);
      };
    }));
    const client = createNostrPoolClient({ relays: ['wss://relay.example'], pool });

    const first = client.connect(['wss://relay.example'], { force: true });
    const second = client.connect(['wss://relay.example'], { force: true });

    expect(get(client.connectionStatus)).toEqual({
      'wss://relay.example': 'connecting'
    });
    await Promise.resolve();
    expect(pool.ensureRelay).toHaveBeenCalledTimes(1);

    resolveConnection();
    await expect(Promise.all([first, second])).resolves.toEqual([
      {
        total: 1,
        connected: 1,
        failed: 0,
        connecting: 0,
        relays: [{ url: 'wss://relay.example', status: 'connected' }]
      },
      {
        total: 1,
        connected: 1,
        failed: 0,
        connecting: 0,
        relays: [{ url: 'wss://relay.example', status: 'connected' }]
      }
    ]);
    expect(pool.ensureRelay).toHaveBeenCalledTimes(1);
  });

  it('destroys a pool only once when disconnect is repeated', () => {
    const pool = createPool();
    const client = createNostrPoolClient({ pool });

    client.disconnect();
    client.disconnect();

    expect(pool.destroy).toHaveBeenCalledTimes(1);
  });
});
