import { beforeEach, describe, expect, it, vi } from 'vitest';

const subscriptionsMock = vi.hoisted(() => ({
  queryOrPartial: vi.fn(),
  readModelEvents: vi.fn((result) => Array.isArray(result) ? result : (result?.events || []))
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
    pubkey: 'docs-publisher',
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
    subscriptionsMock.queryOrPartial.mockReset();
    subscriptionsMock.readModelEvents.mockClear();
  });

  it('ignores an empty docs cache and queries relay-backed NIP-23 events', async () => {
    localStorage.setItem('bahia_docs_cache', JSON.stringify({
      cachedAt: Date.now(),
      events: []
    }));
    subscriptionsMock.queryOrPartial.mockResolvedValueOnce({
      events: [docEvent('features-services', 'Services', 'feature', '# Services')]
    });

    const catalog = await fetchDocsCatalog({ timeoutMs: 2500 });

    expect(subscriptionsMock.queryOrPartial).toHaveBeenCalledWith([
      { kinds: [30023], '#t': ['bahia-docs'] }
    ], { scope: 'docs-catalog', timeoutMs: 2500 });
    expect(catalog.count).toBe(1);
    expect(catalog.topics[0]).toMatchObject({
      topic: 'features-services',
      title: 'Services',
      category: 'feature',
      href: '/docs/features-services'
    });
  });

  it('does not cache empty relay snapshots as documentation truth', async () => {
    subscriptionsMock.queryOrPartial.mockResolvedValueOnce({ events: [] });

    const catalog = await fetchDocsCatalog({ timeoutMs: 2500 });

    expect(catalog.count).toBe(0);
    expect(localStorage.getItem('bahia_docs_cache')).toBeNull();
  });

  it('resolves a single topic from the relay event catalog', async () => {
    subscriptionsMock.queryOrPartial.mockResolvedValueOnce({
      events: [
        docEvent('features-services', 'Services', 'feature', '# Services\n\n[Deployments](features/deployments.md)'),
        docEvent('features-deployments', 'Deployments', 'feature', '# Deployments')
      ]
    });

    const doc = await fetchDoc('features-services', { bypassCache: true, timeoutMs: 2500 });

    expect(doc?.metadata).toMatchObject({ topic: 'features-services', title: 'Services' });
    expect(doc?.markdown).toContain('# Services');
    expect(doc?.links).toContainEqual(expect.objectContaining({
      original: 'features/deployments.md',
      href: '/docs/features-deployments',
      topic: 'features-deployments',
      status: 'resolved'
    }));
  });
});
