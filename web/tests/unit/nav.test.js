import { describe, it, expect } from 'vitest';
import {
  NAV_LINKS,
  NAV_SECTIONS,
  PRIMARY_NAV_LINKS,
  authPresentation,
  currentLocation,
  isActiveNavLink,
  isActiveNavSection,
  truncatePubkey
} from '../../src/lib/components/nav-model.js';
import {
  anonymousMenuItems,
  authenticatedMenuItems,
  menuKeyHandler
} from '../../src/lib/components/user-menu-model.js';

describe('nav model helpers', () => {
  it('groups navigation links into consolidated sections', () => {
    expect(NAV_SECTIONS.map((section) => ({ title: section.title, icon: section.icon }))).toEqual([
      { title: 'Workspace', icon: 'layout-dashboard' },
      { title: 'Delivery', icon: 'rocket' },
      { title: 'Operations', icon: 'server' },
      { title: 'Intelligence', icon: 'brain' },
      { title: 'Admin', icon: 'shield' }
    ]);

    expect(NAV_LINKS).toContainEqual({ href: '/souls', label: 'Souls' });
    expect(NAV_LINKS).toContainEqual({ href: '/dns', label: 'DNS' });
    expect(NAV_LINKS).toContainEqual({ href: '/fleet-health', label: 'Fleet Health', statusKey: 'fleetHealth' });
    expect(NAV_LINKS).toContainEqual({ href: '/notifications', label: 'Notifications' });
    expect(NAV_LINKS).toContainEqual({ href: '/ml', label: 'Inference' });
    expect(NAV_LINKS).toContainEqual({ href: '/llm', label: 'LLM' });
    expect(NAV_LINKS).toContainEqual({ href: '/payments', label: 'Payments' });
    expect(NAV_LINKS).toContainEqual({ href: '/policies', label: 'Policies' });
    const pendingApprovals = NAV_LINKS.find((link) => link.href === '/deployments/pending');
    expect(pendingApprovals).toEqual({ href: '/deployments/pending', label: 'Pending Approvals' });
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
      buttonTitle: 'No Nostr extension detected (NIP-07)',
      showError: true,
      error: 'missing extension'
    });
  });

  it('defines user menu items for anonymous and authenticated states', () => {
    expect(anonymousMenuItems.map((item) => item.id)).toEqual(['nip07', 'nip46']);
    expect(anonymousMenuItems.find((item) => item.id === 'nip07')).toMatchObject({ action: 'login-nip07' });
    expect(anonymousMenuItems.find((item) => item.id === 'nip46')).toMatchObject({ href: '/settings#nostr-connect' });

    expect(authenticatedMenuItems.map((item) => item.id)).toEqual(['profile', 'relays', 'logout']);
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
