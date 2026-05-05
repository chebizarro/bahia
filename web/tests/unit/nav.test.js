import { describe, it, expect } from 'vitest';
import { NAV_LINKS, authPresentation, isActiveNavLink, truncatePubkey } from '../../src/lib/components/nav-model.js';

describe('nav model helpers', () => {
  it('includes the Souls and LLM links and computes active states correctly', () => {
    expect(NAV_LINKS).toContainEqual({ href: '/souls', label: 'Souls' });
    expect(NAV_LINKS).toContainEqual({ href: '/llm', label: 'LLM' });
    expect(isActiveNavLink('/souls', '/souls')).toBe(true);
    expect(isActiveNavLink('/souls/new', '/souls')).toBe(true);
    expect(isActiveNavLink('/llm', '/llm')).toBe(true);
    expect(isActiveNavLink('/llm/history', '/llm')).toBe(true);
    expect(isActiveNavLink('/deployments', '/deployments')).toBe(true);
    expect(isActiveNavLink('/deployments/pending', '/deployments')).toBe(false);
    expect(isActiveNavLink('/deployments/pending', '/deployments/pending')).toBe(true);
    expect(isActiveNavLink('/events/live', '/events')).toBe(false);
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
    }, true)).toEqual({
      mode: 'authenticated',
      pubkey,
      truncatedPubkey: 'ffffffff...ffff',
      backendAuthenticated: false,
      showWarning: true,
      warning: 'backend unavailable'
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
      buttonLabel: '⚠️ No Extension',
      buttonTitle: 'No Nostr extension detected (NIP-07)',
      showError: true,
      error: 'missing extension'
    });
  });
});
