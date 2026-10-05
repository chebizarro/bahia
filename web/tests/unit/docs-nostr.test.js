import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const trustMock = vi.hoisted(() => ({ publishers: ['a'.repeat(64)], stored: [] }));

vi.mock('$lib/nostr/boot.js', () => ({
  boot: vi.fn(async () => undefined),
  getEventStore: () => ({ query: () => trustMock.stored })
}));
vi.mock('$lib/stores/discovery.svelte.js', () => ({
  getBootstrapSeed: () => trustMock.publishers.length ? { service_pubkeys: trustMock.publishers } : null
}));

const subscriptionsMock = vi.hoisted(() => ({
  relayEvents: [],
  closedRelays: [],
  emitEose: true,
  handlers: null,
  unsubscribe: vi.fn(),
  nostr: {
    subscribeWithRecovery: vi.fn((_filters, handlers = {}) => {
      subscriptionsMock.handlers = handlers;
      for (const event of subscriptionsMock.relayEvents) handlers.onEvent?.(event, 'wss://docs.example');
      for (const closure of subscriptionsMock.closedRelays) handlers.onClosed?.(closure.reason, closure.relay, closure.meta);
      if (subscriptionsMock.emitEose) handlers.onEose?.('wss://docs.example');
      return subscriptionsMock.unsubscribe;
    })
  }
}));

vi.mock('$app/environment', () => ({
  browser: true,
  dev: false,
  building: false,
  version: ''
}));

vi.mock('$lib/nostr/subscriptions.js', () => subscriptionsMock);

const { fetchDocsCatalog, fetchDoc } = await import('../../src/lib/docs/nostr.js');

function docEvent(topic, title, category = 'guide', content = '# Document') {
  return {
    id: `event-${topic}`,
    kind: 30023,
    pubkey: trustMock.publishers[0],
    created_at: 1_700_000_000,
    content,
    tags: [
      ['d', topic],
      ['title', title],
      ['t', category],
      ['t', 'bahia-docs'],
      ['source', `${topic}.md`]
    ],
    sig: 'signature'
  };
}

describe('Nostr documentation client', () => {
  beforeEach(() => {
    localStorage.clear();
    trustMock.publishers = ['a'.repeat(64)];
    trustMock.stored = [];
    subscriptionsMock.relayEvents = [];
    subscriptionsMock.closedRelays = [];
    subscriptionsMock.emitEose = true;
    subscriptionsMock.handlers = null;
    subscriptionsMock.unsubscribe.mockClear();
    subscriptionsMock.nostr.subscribeWithRecovery.mockClear();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it('ignores an empty docs cache and queries relay-backed NIP-23 events', async () => {
    localStorage.setItem('bahia_docs_cache', JSON.stringify({
      cachedAt: Date.now(),
      events: []
    }));
    subscriptionsMock.relayEvents = [docEvent('features-services', 'Services', 'feature', '# Services')];

    const catalog = await fetchDocsCatalog({ timeoutMs: 2500 });

    expect(subscriptionsMock.nostr.subscribeWithRecovery).toHaveBeenCalledWith([
      { kinds: [30023], '#t': ['bahia-docs'], authors: ['a'.repeat(64)] }
    ], expect.objectContaining({
      onEvent: expect.any(Function),
      onEose: expect.any(Function),
      onClosed: expect.any(Function)
    }));
    expect(subscriptionsMock.unsubscribe).toHaveBeenCalledOnce();
    expect(catalog.count).toBe(1);
    expect(catalog.complete).toBe(true);
    expect(catalog.degraded).toBeNull();
    expect(catalog.topics[0]).toMatchObject({
      topic: 'features-services',
      title: 'Services',
      category: 'feature',
      href: '/docs/features-services'
    });
  });

  it('rejects untrusted relay and persisted events even if a query returns them', async () => {
    const trusted = docEvent('features-services', 'Services', 'feature', '# Trusted documentation');
    const attacker = { ...docEvent('features-services', 'Forged Services', 'feature', '# Forged documentation'),
      id: 'forged-doc', pubkey: 'b'.repeat(64), created_at: trusted.created_at + 100 };
    trustMock.stored = [attacker];
    subscriptionsMock.relayEvents = [attacker, trusted];
    const catalog = await fetchDocsCatalog({ bypassCache: true });
    expect(catalog.topics).toEqual([expect.objectContaining({ title: 'Services' })]);
    const doc = await fetchDoc('features-services', { bypassCache: true });
    expect(doc?.markdown).toBe('# Trusted documentation');
    expect(subscriptionsMock.nostr.subscribeWithRecovery).toHaveBeenCalledWith([
      expect.objectContaining({ authors: ['a'.repeat(64)] })
    ], expect.any(Object));
  });

  it('rejects an untrusted stored snapshot before returning from cache', async () => {
    trustMock.stored = [{ ...docEvent('features-services', 'Forged Services'), pubkey: 'b'.repeat(64) }];
    const catalog = await fetchDocsCatalog();
    expect(catalog.count).toBe(0);
    expect(subscriptionsMock.nostr.subscribeWithRecovery).toHaveBeenCalledOnce();
  });

  it('fails closed without a configured documentation publisher', async () => {
    trustMock.publishers = [];
    await expect(fetchDocsCatalog()).rejects.toThrow('Documentation publisher not configured');
    await expect(fetchDoc('features-services')).rejects.toThrow('Documentation publisher not configured');
    expect(subscriptionsMock.nostr.subscribeWithRecovery).not.toHaveBeenCalled();
  });

  it('does not cache empty relay snapshots as documentation truth', async () => {
    subscriptionsMock.relayEvents = [];

    const catalog = await fetchDocsCatalog({ timeoutMs: 2500 });

    expect(catalog.count).toBe(0);
    expect(catalog.complete).toBe(true);
    expect(catalog.degraded).toBeNull();
    expect(localStorage.getItem('bahia_docs_cache')).toBeNull();
  });

  it('returns partial docs catalog with degraded metadata when CLOSED occurs before EOSE', async () => {
    subscriptionsMock.emitEose = false;
    subscriptionsMock.relayEvents = [docEvent('features-services', 'Services', 'feature', '# Services')];
    subscriptionsMock.closedRelays = [{ reason: 'relay closed before EOSE', relay: 'wss://docs.example', meta: { terminal: true, source: 'closed' } }];

    const catalog = await fetchDocsCatalog({ bypassCache: true, timeoutMs: 2500 });

    expect(catalog.count).toBe(1);
    expect(catalog.complete).toBe(false);
    expect(catalog.degraded).toMatchObject({
      incomplete: true,
      reason: 'closed',
      partialEventCount: 1,
      relaySummary: [expect.objectContaining({ relay: 'wss://docs.example', status: 'closed', reason: 'relay closed before EOSE' })]
    });
    expect(localStorage.getItem('bahia_docs_cache')).toBeNull();
  });

  it('returns empty docs catalog with AUTH-required degraded metadata', async () => {
    subscriptionsMock.emitEose = false;
    subscriptionsMock.closedRelays = [{ reason: 'auth-required: sign in', relay: 'wss://docs-auth.example', meta: { terminal: true, source: 'auth', authRequired: true } }];

    const catalog = await fetchDocsCatalog({ bypassCache: true, timeoutMs: 2500 });

    expect(catalog.count).toBe(0);
    expect(catalog.complete).toBe(false);
    expect(catalog.degraded).toMatchObject({
      incomplete: true,
      reason: 'auth-required',
      authRequired: true,
      partialEventCount: 0,
      relaySummary: [expect.objectContaining({ relay: 'wss://docs-auth.example', status: 'auth-required' })]
    });
  });

  it('keeps the historical REQ open until EOSE instead of treating a deadline as completion', async () => {
    subscriptionsMock.emitEose = false;
    const pending = fetchDocsCatalog({ bypassCache: true, timeoutMs: 1 });
    let settled = false;
    void pending.then(() => { settled = true; });
    await vi.waitFor(() => expect(subscriptionsMock.handlers).not.toBeNull());
    await Promise.resolve();
    expect(settled).toBe(false);
    subscriptionsMock.handlers.onEose('wss://docs.example');
    await expect(pending).resolves.toMatchObject({ complete: true });
    expect(subscriptionsMock.unsubscribe).toHaveBeenCalledOnce();
  });

  it('resolves a single topic from the relay event catalog', async () => {
    subscriptionsMock.relayEvents = [
      docEvent('features-services', 'Services', 'feature', '# Services\n\n[Deployments](features/deployments.md)'),
      docEvent('features-deployments', 'Deployments', 'feature', '# Deployments')
    ];

    const doc = await fetchDoc('features-services', { bypassCache: true, timeoutMs: 2500 });

    expect(doc?.metadata).toMatchObject({ topic: 'features-services', title: 'Services' });
    expect(doc?.markdown).toContain('# Services');
    expect(doc?.links).toContainEqual(expect.objectContaining({
      original: 'features/deployments.md',
      href: '/docs/features-deployments',
      topic: 'features-deployments',
      status: 'resolved'
    }));
    expect(doc?.complete).toBe(true);
    expect(doc?.degraded).toBeNull();
  });
});
