import { describe, it, expect } from 'vitest';
import {
  NAV_LINKS,
  NAV_SECTIONS,
  PRIMARY_NAV_LINKS,
  authPresentation,
  isActiveNavLink,
  isActiveNavSection,
  truncatePubkey
} from '../../src/lib/components/nav-model.js';

describe('nav model helpers', () => {
  it('groups navigation links into primary shortcuts and sections', () => {
    expect(PRIMARY_NAV_LINKS.map((link) => link.href)).toEqual([
      '/',
      '/services',
      '/deployments',
      '/environments'
    ]);

    expect(NAV_SECTIONS.map((section) => section.title)).toEqual([
      'Workspace',
      'Delivery',
      'Operations',
      'Admin'
    ]);

    expect(NAV_LINKS).toContainEqual({ href: '/souls', label: 'Souls' });
    expect(NAV_LINKS).toContainEqual({ href: '/llm', label: 'LLM' });
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
    expect(isActiveNavSection('/settings', NAV_SECTIONS[0])).toBe(false);
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
});
