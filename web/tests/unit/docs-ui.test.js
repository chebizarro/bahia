import { beforeEach, describe, expect, it, vi } from 'vitest';

import { renderComponent, textOf, tick } from './utils/svelte-component-test.ts';
import { BahiaClient } from '../../src/lib/api/client.js';
import { renderDocumentationMarkdown } from '../../src/lib/docs/render.js';
import DocsCatalog from '../../src/lib/components/docs/DocsCatalog.svelte';
import MarkdownDocument from '../../src/lib/components/docs/MarkdownDocument.svelte';
import DocsPage from '../../src/routes/docs/+page.svelte';
import DocsTopicPage from '../../src/routes/docs/[topic]/+page.svelte';

const gotoMock = vi.hoisted(() => vi.fn());
const pageMock = vi.hoisted(() => ({
  page: {
    params: { topic: 'index' },
    url: new URL('http://localhost/docs/index')
  }
}));

// Mock nostr docs module for page-level tests.
const nostrDocsMock = vi.hoisted(() => ({
  fetchDocsCatalog: vi.fn(),
  fetchDoc: vi.fn()
}));

vi.mock('$app/navigation', () => ({ goto: gotoMock }));
vi.mock('$app/state', () => ({ page: pageMock.page }));
vi.mock('$lib/docs/nostr.js', () => nostrDocsMock);

function jsonEnvelope(data, extra = {}) {
  return {
    ok: true,
    status: 200,
    statusText: 'OK',
    headers: new Map([['content-type', 'application/json']]),
    json: async () => ({ data }),
    ...extra
  };
}

async function flushEffects() {
  for (let i = 0; i < 5; i += 1) {
    await tick();
    await Promise.resolve();
  }
}

describe('docs API client (REST, backward compat)', () => {
  beforeEach(() => {
    global.fetch = vi.fn();
  });

  it('loads docs catalog and topic through read-only API paths', async () => {
    const client = new BahiaClient();
    global.fetch
      .mockResolvedValueOnce(jsonEnvelope({ count: 1, topics: [], groups: [] }))
      .mockResolvedValueOnce(jsonEnvelope({ metadata: { topic: 'features-services' }, markdown: '# Services', links: [] }));

    await expect(client.listDocs()).resolves.toMatchObject({ count: 1 });
    await expect(client.getDoc('features/services')).resolves.toMatchObject({ markdown: '# Services' });

    expect(global.fetch).toHaveBeenNthCalledWith(1, '/api/v1/docs', expect.objectContaining({ method: 'GET' }));
    expect(global.fetch).toHaveBeenNthCalledWith(2, `/api/v1/docs/${encodeURIComponent('features/services')}`, expect.objectContaining({ method: 'GET' }));
  });
});

describe('docs catalog component and page', () => {
  beforeEach(() => {
    nostrDocsMock.fetchDocsCatalog.mockReset();
    nostrDocsMock.fetchDoc.mockReset();
  });

  it('renders grouped catalog topics from the docs API shape', () => {
    const target = renderComponent(DocsCatalog, {
      groups: [
        { category: 'guide', label: 'Getting Started & Guides', topics: [{ topic: 'getting-started', title: 'Getting Started', href: '/docs/getting-started' }] },
        { category: 'feature', label: 'Feature Guides', topics: [{ topic: 'features-services', title: 'Services', href: '/docs/features-services' }] }
      ]
    });

    expect(textOf(target)).toContain('Getting Started & Guides');
    expect(textOf(target)).toContain('Feature Guides');
    expect(target.querySelector('a[href="/docs/features-services"]')?.textContent).toContain('Services');
  });

  it('loads the catalog from relay via NIP-23 and surfaces failures', async () => {
    nostrDocsMock.fetchDocsCatalog.mockResolvedValueOnce({
      count: 1,
      topics: [{ topic: 'index', title: 'Bahia User Guide', category: 'guide', href: '/docs/index' }],
      groups: [{ category: 'guide', label: 'Getting Started & Guides', topics: [{ topic: 'index', title: 'Bahia User Guide', category: 'guide', href: '/docs/index' }] }]
    });

    const loaded = renderComponent(DocsPage);
    await flushEffects();

    expect(textOf(loaded)).toContain('1 topics available');
    expect(loaded.querySelector('a[href="/docs/index"]')?.textContent).toContain('Bahia User Guide');

    nostrDocsMock.fetchDocsCatalog.mockRejectedValueOnce(new Error('relay unavailable'));
    const failed = renderComponent(DocsPage);
    await flushEffects();

    expect(textOf(failed)).toContain('Documentation catalog failed to load');
    expect(textOf(failed)).toContain('relay unavailable');
  });
});

describe('documentation Markdown rendering and reader page', () => {
  beforeEach(() => {
    gotoMock.mockReset();
    nostrDocsMock.fetchDocsCatalog.mockReset();
    nostrDocsMock.fetchDoc.mockReset();
    pageMock.page.params = { topic: 'index' };
    pageMock.page.url = new URL('http://localhost/docs/index');
  });

  it('sanitizes Markdown HTML and rewrites internal links from central link resolutions', async () => {
    const html = renderDocumentationMarkdown(
      '# Services\n\n[Deployments](deployments.md) [External](https://example.com) [Missing](missing.md)\n\n<script>alert(1)</script><img src="x" onerror="alert(1)">',
      [
        { original: 'deployments.md', href: '/docs/features-deployments', topic: 'features-deployments', status: 'resolved' },
        { original: 'https://example.com', href: 'https://example.com', external: true, status: 'resolved' },
        { original: 'missing.md', status: 'not_found', error: 'documentation topic not found' }
      ]
    );

    expect(html).toContain('href="/docs/features-deployments"');
    expect(html).toContain('data-doc-topic="features-deployments"');
    expect(html).toContain('target="_blank"');
    expect(html).toContain('docs-link-unresolved');
    expect(renderDocumentationMarkdown('[Unscanned](other.md)', [])).toContain('Documentation link was not resolved by the central docs service');
    expect(html).not.toContain('<script');
    expect(html).not.toContain('onerror');

    const target = renderComponent(MarkdownDocument, {
      markdown: '[Deployments](deployments.md)',
      links: [{ original: 'deployments.md', href: '/docs/features-deployments', topic: 'features-deployments', status: 'resolved' }]
    });
    target.querySelector('a').click();
    await tick();
    expect(gotoMock).toHaveBeenCalledWith('/docs/features-deployments');
  });

  it('renders document content from relay and handles missing topics', async () => {
    nostrDocsMock.fetchDoc.mockResolvedValueOnce({
      metadata: { topic: 'index', title: 'Bahia User Guide', category: 'guide' },
      markdown: '# Bahia User Guide\n\n[Services](features/services.md)',
      links: []
    });

    const loaded = renderComponent(DocsTopicPage);
    await flushEffects();

    expect(textOf(loaded)).toContain('Bahia User Guide');

    pageMock.page.params = { topic: 'missing' };
    pageMock.page.url = new URL('http://localhost/docs/missing');
    nostrDocsMock.fetchDoc.mockResolvedValueOnce(null);

    const missing = renderComponent(DocsTopicPage);
    await flushEffects();

    expect(textOf(missing)).toContain('Topic unavailable');
    expect(textOf(missing)).toContain('not found on relay');
    expect(missing.querySelector('a.button-link[href="/docs"]')?.textContent).toContain('Back to documentation catalog');
  });
});
