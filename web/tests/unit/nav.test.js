import { existsSync, readdirSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, it, expect } from 'vitest';
import {
  DOCS_HOME_LINK,
  NAV_LINKS,
  NAV_SECTIONS,
  PRIMARY_NAV_LINKS,
  authPresentation,
  currentLocation,
  currentRouteDocs,
  currentRouteDocsRef,
  isActiveNavLink,
  isActiveNavSection,
  routeDocTopics,
  truncatePubkey
} from '../../src/lib/components/nav-model.js';
import {
  anonymousMenuItems,
  authenticatedMenuItems,
  menuKeyHandler
} from '../../src/lib/components/user-menu-model.js';

const DOCS_ROOT = [
  join(process.cwd(), 'docs', 'user-guide'),
  join(process.cwd(), '..', 'docs', 'user-guide')
].find((candidate) => existsSync(candidate));

function docsCatalogTopics(dir = DOCS_ROOT) {
  const topics = new Set();
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const fullPath = join(dir, entry.name);
    if (entry.isDirectory()) {
      for (const topic of docsCatalogTopics(fullPath)) topics.add(topic);
      continue;
    }
    if (!entry.isFile() || !entry.name.endsWith('.md')) continue;
    const rel = relative(DOCS_ROOT, fullPath).replaceAll('\\', '/').replace(/\.md$/, '');
    topics.add(rel.replaceAll('/', '-'));
  }
  return topics;
}

describe('nav model helpers', () => {
  it('groups navigation links into consolidated sections', () => {
    expect(NAV_SECTIONS.map((section) => ({ title: section.title, icon: section.icon }))).toEqual([
      { title: 'Workspace', icon: 'layout-dashboard' },
      { title: 'Delivery', icon: 'rocket' },
      { title: 'Operations', icon: 'server' },
      { title: 'Intelligence', icon: 'brain' },
      { title: 'Admin', icon: 'shield' }
    ]);

    expect(NAV_LINKS).toEqual(expect.arrayContaining([
      expect.objectContaining({ href: '/souls', label: 'Souls', docTopic: 'features-souls' }),
      expect.objectContaining({ href: '/continuity', label: 'Continuity', docTopic: 'features-continuity' }),
      expect.objectContaining({ href: '/dns', label: 'DNS', docTopic: 'features-dns' }),
      expect.objectContaining({ href: '/events', label: 'Events', docTopic: 'features-events' }),
      expect.objectContaining({ href: '/fleet-health', label: 'Fleet Health', statusKey: 'fleetHealth', docTopic: 'features-fleet-health' }),
      expect.objectContaining({ href: '/notifications', label: 'Notifications', docTopic: 'features-notifications' }),
      expect.objectContaining({ href: '/ml', label: 'Inference', docTopic: 'features-ml-models' }),
      expect.objectContaining({ href: '/llm', label: 'LLM', docTopic: 'features-llm-routes' }),
      expect.objectContaining({ href: '/payments', label: 'Payments', docTopic: 'features-payments' }),
      expect.objectContaining({ href: '/policies', label: 'Policies', docTopic: 'features-policies' }),
      expect.objectContaining({ href: '/config-fabric', label: 'Config Fabric', docTopic: 'features-policies' })
    ]));
    expect(PRIMARY_NAV_LINKS).toBe(NAV_LINKS);
    const pendingApprovals = NAV_LINKS.find((link) => link.href === '/deployments/pending');
    expect(pendingApprovals).toEqual({ href: '/deployments/pending', label: 'Pending Approvals', docTopic: 'features-deployments' });
    expect(pendingApprovals).not.toHaveProperty('badge');
  });

  it('computes active links and sections correctly', () => {
    expect(isActiveNavLink('/souls', '/souls')).toBe(true);
    expect(isActiveNavLink('/souls/new', '/souls')).toBe(true);
    expect(isActiveNavLink('/llm', '/llm')).toBe(true);
    expect(isActiveNavLink('/llm/history', '/llm')).toBe(true);
    expect(isActiveNavLink('/deployments', '/deployments')).toBe(true);
    expect(isActiveNavLink('/deployments/pending', '/deployments')).toBe(false);
    expect(isActiveNavLink('/deployments/pending', '/deployments/pending')).toBe(true);
    expect(isActiveNavLink('/events/live', '/events')).toBe(false);

    expect(isActiveNavSection('/deployments/pending', NAV_SECTIONS[1])).toBe(true);
    expect(isActiveNavSection('/workers/abc', NAV_SECTIONS[2])).toBe(true);
    expect(isActiveNavSection('/llm/history', NAV_SECTIONS[3])).toBe(true);
    expect(isActiveNavSection('/settings', NAV_SECTIONS[0])).toBe(false);
  });

  it('derives route documentation metadata from consolidated nav links', () => {
    expect(DOCS_HOME_LINK).toEqual({ href: '/docs', label: 'Docs' });
    expect(currentRouteDocs('/services')).toEqual({
      routeHref: '/services',
      routeLabel: 'Services',
      topic: 'features-services',
      href: '/docs/features-services',
      label: 'Services documentation'
    });
    expect(currentRouteDocs('/deployments/pending/approval-1')).toMatchObject({
      routeHref: '/deployments/pending',
      topic: 'features-deployments'
    });
    expect(currentRouteDocs('/fleet-health')).toMatchObject({ topic: 'features-fleet-health' });
    expect(currentRouteDocs('/llm/history')).toMatchObject({ topic: 'features-llm-routes' });
    expect(currentRouteDocs('/ml/endpoints')).toMatchObject({ topic: 'features-ml-models' });
    expect(currentRouteDocs('/settings')).toBeNull();
    expect(currentRouteDocsRef('/services')).toEqual({
      ref: 'docs:features-services',
      label: 'Services documentation',
      href: '/docs/features-services',
      topic: 'features-services',
      source: 'route'
    });
  });

  it('keeps nav doc topics backed by the docs catalog source tree', () => {
    const catalogTopics = docsCatalogTopics();
    expect(routeDocTopics()).toEqual(expect.arrayContaining([
      { href: '/services', label: 'Services', docTopic: 'features-services' },
      { href: '/deployments', label: 'Deployments', docTopic: 'features-deployments' },
      { href: '/fleet-health', label: 'Fleet Health', docTopic: 'features-fleet-health' },
      { href: '/continuity', label: 'Continuity', docTopic: 'features-continuity' },
      { href: '/events', label: 'Events', docTopic: 'features-events' },
      { href: '/llm', label: 'LLM', docTopic: 'features-llm-routes' },
      { href: '/ml', label: 'Inference', docTopic: 'features-ml-models' }
    ]));
    for (const { docTopic } of routeDocTopics()) {
      expect(catalogTopics.has(docTopic), `${docTopic} should exist in docs/user-guide catalog`).toBe(true);
    }
  });

  it('derives the current breadcrumb location from consolidated sections', () => {
    expect(currentLocation('/workers/abc')).toEqual({
      section: 'Operations',
      page: 'Workers',
      icon: 'server'
    });
    expect(currentLocation('/fleet-health')).toEqual({
      section: 'Operations',
      page: 'Fleet Health',
      icon: 'server'
    });
    expect(currentLocation('/llm/history')).toEqual({
      section: 'Intelligence',
      page: 'LLM',
      icon: 'brain'
    });
    expect(currentLocation('/unknown')).toEqual({ section: null, page: null, icon: null });
  });

  it('truncates pubkeys and derives authenticated auth presentation', () => {
    const pubkey = 'f'.repeat(64);
    expect(truncatePubkey(pubkey)).toBe('ffffffff...ffff');
    expect(truncatePubkey('short')).toBe('short');

    expect(authPresentation({
      status: 'idle',
      pubkey,
      backendAuthenticated: false,
      error: 'backend unavailable'
    }, true)).toMatchObject({
      mode: 'authenticated',
      pubkey,
      truncatedPubkey: 'ffffffff...ffff',
      backendAuthenticated: false,
      showWarning: true,
      warning: 'backend unavailable',
      profile: null,
      displayLabel: 'ffffffff...ffff',
      nip05: '',
      avatarUrl: ''
    });
  });

  it('derives loading and anonymous auth presentation states', () => {
    expect(authPresentation({ status: 'checking' }, false)).toEqual({
      mode: 'loading',
      label: 'Checking...'
    });

    expect(authPresentation({
      status: 'error',
      extensionAvailable: false,
      error: 'missing extension'
    }, false)).toEqual({
      mode: 'anonymous',
      extensionAvailable: false,
      buttonLabel: 'No Extension',
      buttonTitle: 'No browser signer detected',
      showError: true,
      error: 'missing extension'
    });
  });

  it('defines user menu items for anonymous and authenticated states', () => {
    expect(anonymousMenuItems.map((item) => item.id)).toEqual(['nip07', 'nip46']);
    expect(anonymousMenuItems.find((item) => item.id === 'nip07')).toMatchObject({ action: 'login-nip07' });
    expect(anonymousMenuItems.find((item) => item.id === 'nip46')).toMatchObject({ href: '/settings#nostr-connect' });

    expect(authenticatedMenuItems.map((item) => item.id)).toEqual(['profile', 'relays', 'logout']);
    expect(authenticatedMenuItems.find((item) => item.id === 'profile')).toMatchObject({ href: '/settings/profile' });
    expect(existsSync(join(process.cwd(), 'src', 'routes', 'settings', 'profile', '+page.svelte'))
      || existsSync(join(process.cwd(), 'web', 'src', 'routes', 'settings', 'profile', '+page.svelte'))).toBe(true);
    expect(authenticatedMenuItems.find((item) => item.id === 'relays')).toMatchObject({ href: '/settings/relays' });
    expect(authenticatedMenuItems.find((item) => item.id === 'logout')).toMatchObject({ action: 'logout' });
  });

  it('handles user menu keyboard navigation with wrapping and disabled items', () => {
    const calls = [];
    const event = (key) => ({ key, preventDefault: () => calls.push(key) });
    const items = [{ id: 'one' }, { id: 'two', disabled: true }, { id: 'three' }];

    expect(menuKeyHandler(event('ArrowDown'), { items, activeIndex: 0 })).toBe(2);
    expect(menuKeyHandler(event('ArrowDown'), { items, activeIndex: 2 })).toBe(0);
    expect(menuKeyHandler(event('ArrowUp'), { items, activeIndex: 0 })).toBe(2);
    expect(menuKeyHandler(event('Home'), { items, activeIndex: 2 })).toBe(0);
    expect(menuKeyHandler(event('End'), { items, activeIndex: 0 })).toBe(2);

    let closed = false;
    expect(menuKeyHandler(event('Escape'), { items, activeIndex: 2, close: () => { closed = true; } })).toBe(-1);
    expect(closed).toBe(true);
    expect(calls).toEqual(['ArrowDown', 'ArrowDown', 'ArrowUp', 'Home', 'End', 'Escape']);
  });
});
