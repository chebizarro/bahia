import { describe, expect, it, vi } from 'vitest';

import {
  createFleetConfigStore,
  emptyFleetConfigDocument,
  validateFleetConfigDocument
} from '../../src/lib/stores/fleet-config.svelte.js';

describe('fleet config store', () => {
  it('signs kind 31953, verifies relay acceptance, and updates the replaceable view', async () => {
    const auth = { status: 'authenticated', pubkey: 'a'.repeat(64) };
    const publish = vi.fn(async () => [
      { relay: 'wss://one.example', accepted: false, message: 'rate-limited' },
      { relay: 'wss://two.example', accepted: true, message: 'saved' }
    ]);
    const sign = vi.fn(async (event) => ({ ...event, id: 'signed-fleet-event', sig: 'f'.repeat(128) }));
    const store = createFleetConfigStore({
      client: { publish, subscribe: vi.fn() },
      auth,
      loginFn: vi.fn(),
      sign,
      now: () => 1715700000
    });
    const document = emptyFleetConfigDocument();
    document.template.logging = { level: 'info' };
    document.defaults = {
      model: 'provider/fleet-model',
      bindings: ['slack:ops'],
      required_plugins: ['nostr=npm:openclaw-nostr@1.0.0']
    };

    const result = await store.publish(document);

    expect(sign).toHaveBeenCalledWith(expect.objectContaining({
      kind: 31953,
      created_at: 1715700000,
      pubkey: auth.pubkey,
      tags: expect.arrayContaining([
        ['d', 'soulfactory-fleet-config/v1'],
        ['schema', 'soulfactory-fleet-config/v1']
      ])
    }));
    expect(JSON.parse(sign.mock.calls[0][0].content)).toEqual(document);
    expect(publish).toHaveBeenCalledWith(expect.objectContaining({ id: 'signed-fleet-event' }));
    expect(result.publishResults).toHaveLength(2);
    expect(store.state.event.id).toBe('signed-fleet-event');
    expect(store.state.document.template.logging.level).toBe('info');
  });

  it('bumps the timestamp when republishing so retry creates a new fleet revision', async () => {
    const sign = vi.fn(async (event) => ({ ...event, id: `revision-${event.created_at}` }));
    const store = createFleetConfigStore({
      client: {
        publish: vi.fn(async () => [{ relay: 'wss://one.example', accepted: true, message: 'saved' }]),
        subscribe: vi.fn()
      },
      auth: { status: 'authenticated', pubkey: 'd'.repeat(64) },
      sign,
      now: () => 1715700000
    });

    await store.publish(emptyFleetConfigDocument());
    await store.publish(emptyFleetConfigDocument());

    expect(sign.mock.calls.map(([event]) => event.created_at)).toEqual([1715700000, 1715700001]);
    expect(store.state.event.id).toBe('revision-1715700001');
  });

  it('rejects publishing when every relay returns OK false', async () => {
    const store = createFleetConfigStore({
      client: {
        publish: vi.fn(async () => [{ relay: 'wss://one.example', accepted: false, message: 'blocked' }]),
        subscribe: vi.fn()
      },
      auth: { status: 'authenticated', pubkey: 'b'.repeat(64) },
      sign: vi.fn(async (event) => ({ ...event, id: 'rejected-event' }))
    });

    await expect(store.publish(emptyFleetConfigDocument())).rejects.toThrow('not accepted by any relay');
    expect(store.state.event).toBeNull();
  });

  it('uses an exact author and d-tag subscription filter and returns cleanup', () => {
    const cleanup = vi.fn();
    const subscribe = vi.fn(() => cleanup);
    const auth = { status: 'authenticated', pubkey: 'c'.repeat(64) };
    const store = createFleetConfigStore({
      client: { subscribe, publish: vi.fn() },
      auth,
      sign: vi.fn()
    });

    expect(store.subscribe()).toBe(cleanup);
    expect(subscribe).toHaveBeenCalledWith([{
      kinds: [31953],
      authors: [auth.pubkey],
      '#d': ['soulfactory-fleet-config/v1'],
      limit: 10
    }], expect.objectContaining({
      onEvent: expect.any(Function),
      onEose: expect.any(Function),
      onClosed: expect.any(Function)
    }));
  });

  it('clears stale state when the authenticated operator changes', () => {
    const subscriptions = [];
    const auth = { status: 'authenticated', pubkey: 'a'.repeat(64) };
    const subscribe = vi.fn((filters, handlers) => {
      subscriptions.push({ filters, handlers });
      return vi.fn();
    });
    const store = createFleetConfigStore({
      client: { subscribe, publish: vi.fn() },
      auth,
      sign: vi.fn()
    });
    const document = emptyFleetConfigDocument();
    document.defaults.model = 'provider/operator-a';

    store.subscribe();
    subscriptions[0].handlers.onEvent({
      id: 'z-event',
      kind: 31953,
      pubkey: auth.pubkey,
      created_at: 200,
      tags: [['d', 'soulfactory-fleet-config/v1'], ['schema', 'soulfactory-fleet-config/v1']],
      content: JSON.stringify(document)
    });
    expect(store.state.document.defaults.model).toBe('provider/operator-a');

    auth.pubkey = 'b'.repeat(64);
    store.subscribe();
    expect(store.state.event).toBeNull();
    expect(store.state.document).toBeNull();

    const nextDocument = emptyFleetConfigDocument();
    nextDocument.defaults.model = 'provider/operator-b';
    subscriptions[1].handlers.onEvent({
      id: 'a-event',
      kind: 31953,
      pubkey: auth.pubkey,
      created_at: 100,
      tags: [['d', 'soulfactory-fleet-config/v1'], ['schema', 'soulfactory-fleet-config/v1']],
      content: JSON.stringify(nextDocument)
    });
    expect(store.state.document.defaults.model).toBe('provider/operator-b');
  });

  it('rejects unknown sections and literal secret values', () => {
    const document = emptyFleetConfigDocument();
    document.template.identity = {};
    document.template.gateway = { auth: { token: 'literal-token' } };

    const result = validateFleetConfigDocument(document);
    expect(result.valid).toBe(false);
    expect(result.errors.join(' ')).toContain('not allowed');
    expect(result.errors.join(' ')).toContain('${VAR}');
  });
});
