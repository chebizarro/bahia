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

  // A verified-store double: query() honours kinds, authors and #d.
  function eventStoreDouble(events) {
    return { query: vi.fn((filter) => events.filter((event) =>
      filter.kinds.includes(event.kind) && filter.authors.includes(event.pubkey) &&
      event.tags.some((tag) => tag[0] === 'd' && filter['#d'].includes(tag[1])))) };
  }
  const configEvent = (id, pubkey, createdAt, document) => ({
    id, kind: 31953, pubkey, created_at: createdAt,
    tags: [['d', 'soulfactory-fleet-config/v1'], ['schema', 'soulfactory-fleet-config/v1']],
    content: JSON.stringify(document)
  });

  it('renders the cached configuration at once from an exact author and d-tag store query, without a REQ', () => {
    const auth = { status: 'authenticated', pubkey: 'c'.repeat(64) };
    const cached = emptyFleetConfigDocument();
    cached.defaults.model = 'provider/cached';
    const forged = emptyFleetConfigDocument();
    forged.defaults.model = 'provider/forged';
    const events = [configEvent('cached', auth.pubkey, 100, cached), configEvent('forged', 'd'.repeat(64), 900, forged)];
    const eventStore = eventStoreDouble(events);
    const cleanup = vi.fn();
    let refresh;
    const subscribe = vi.fn();
    const store = createFleetConfigStore({
      client: { subscribe, publish: vi.fn() },
      auth,
      sign: vi.fn(),
      eventStore: () => eventStore,
      registerRefresh: (callback) => { refresh = callback; return cleanup; }
    });

    const unsubscribe = store.subscribe();
    expect(eventStore.query).toHaveBeenCalledWith({
      kinds: [31953],
      authors: [auth.pubkey],
      '#d': ['soulfactory-fleet-config/v1']
    });
    expect(subscribe).not.toHaveBeenCalled();
    expect(store.state.loading).toBe(false);
    expect(store.state.document.defaults.model).toBe('provider/cached');

    // A newer revision arriving in the store replaces it; an older one does not.
    const newer = emptyFleetConfigDocument();
    newer.defaults.model = 'provider/newer';
    const older = emptyFleetConfigDocument();
    older.defaults.model = 'provider/older';
    events.push(configEvent('newer', auth.pubkey, 200, newer), configEvent('older', auth.pubkey, 50, older));
    refresh();
    expect(store.state.document.defaults.model).toBe('provider/newer');
    expect(store.state.event.id).toBe('newer');
    unsubscribe();
    expect(cleanup).toHaveBeenCalledTimes(1);
  });

  // bahia-fbyo5: with the daemon's `operators:soul-factory` allowlist readable,
  // the newest configuration across the authorized operators is shown, as the
  // daemon applies it; without it only the signed-in key's configuration is.
  it('projects the newest configuration across the trusted operator set and re-projects when the allowlist changes', () => {
    const auth = { status: 'authenticated', pubkey: 'a'.repeat(64) };
    const other = 'b'.repeat(64);
    const mine = emptyFleetConfigDocument();
    mine.defaults.model = 'provider/mine';
    const theirs = emptyFleetConfigDocument();
    theirs.defaults.model = 'provider/theirs';
    const events = [configEvent('mine', auth.pubkey, 100, mine), configEvent('theirs', other, 200, theirs)];
    const eventStore = eventStoreDouble(events);
    let allowlist = null;
    let onAllowlistChange;
    const store = createFleetConfigStore({
      client: {}, auth, eventStore: () => eventStore, registerRefresh: () => () => {},
      trustedAuthors: (signedIn) => [signedIn, ...(allowlist || [])].sort(),
      registerAllowlistChange: (callback) => { onAllowlistChange = callback; return () => { onAllowlistChange = null; }; }
    });
    const unsubscribe = store.subscribe();
    expect(eventStore.query).toHaveBeenLastCalledWith({ kinds: [31953], authors: [auth.pubkey], '#d': ['soulfactory-fleet-config/v1'] });
    expect(store.state.event.id).toBe('mine');

    allowlist = [other];
    onAllowlistChange();
    expect(eventStore.query).toHaveBeenLastCalledWith({ kinds: [31953], authors: [auth.pubkey, other], '#d': ['soulfactory-fleet-config/v1'] });
    expect(store.state.event.id).toBe('theirs');
    expect(store.state.document.defaults.model).toBe('provider/theirs');

    // Losing the allowlist (logout of the key holder, key rotation) drops the
    // other operator's configuration again.
    allowlist = null;
    onAllowlistChange();
    expect(store.state.event.id).toBe('mine');
    unsubscribe();
    expect(onAllowlistChange).toBeNull();
  });

  it('reports an invalid cached configuration without a loading gate', () => {
    const auth = { status: 'authenticated', pubkey: 'c'.repeat(64) };
    const events = [{ ...configEvent('broken', auth.pubkey, 100, {}), content: '{not json' }];
    const store = createFleetConfigStore({
      client: {}, auth, eventStore: () => eventStoreDouble(events), registerRefresh: () => () => {}
    });
    store.subscribe();
    expect(store.state.loading).toBe(false);
    expect(store.state.document).toBeNull();
    expect(store.state.error).not.toBe('');
  });

  it('clears stale state when the authenticated operator changes', () => {
    const auth = { status: 'authenticated', pubkey: 'a'.repeat(64) };
    const document = emptyFleetConfigDocument();
    document.defaults.model = 'provider/operator-a';
    const nextDocument = emptyFleetConfigDocument();
    nextDocument.defaults.model = 'provider/operator-b';
    const events = [configEvent('z-event', auth.pubkey, 200, document)];
    const store = createFleetConfigStore({
      client: { publish: vi.fn() },
      auth,
      sign: vi.fn(),
      eventStore: () => eventStoreDouble(events),
      registerRefresh: () => () => {}
    });

    store.subscribe();
    expect(store.state.document.defaults.model).toBe('provider/operator-a');

    auth.pubkey = 'b'.repeat(64);
    store.subscribe();
    expect(store.state.event).toBeNull();
    expect(store.state.document).toBeNull();

    // The new operator's older, lower-id revision is theirs to see: nothing of
    // the previous operator's state competes with it.
    events.push(configEvent('a-event', auth.pubkey, 100, nextDocument));
    store.subscribe();
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

it('retains the lowest-ID fleet config at equal created_at in either arrival order', () => {
  const pubkey = 'c'.repeat(64);
  const low = { id: '1'.repeat(64), kind: 31953, pubkey, created_at: 100,
    tags: [['d', 'soulfactory-fleet-config/v1'], ['schema', 'soulfactory-fleet-config/v1']],
    content: JSON.stringify(emptyFleetConfigDocument()) };
  const high = { ...low, id: 'f'.repeat(64) };
  for (const order of [[low, high], [high, low]]) {
    const store = createFleetConfigStore({ auth: { pubkey }, client: {} });
    order.forEach(store.apply);
    expect(store.state.event.id).toBe(low.id);
    expect(store.apply(high)).toBe(false);
    expect(store.apply(low)).toBe(false);
  }
});
