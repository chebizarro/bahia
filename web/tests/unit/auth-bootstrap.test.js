/**
 * Tests for auth bootstrap: session restore without network.
 *
 * - Persisted session = authenticated immediately (no REST probe)
 * - requestPersistentStorage called on first authenticated boot
 * - No backendAuthenticated flag
 * - No discovery gate
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

// Mock $app/environment
vi.mock('$app/environment', () => ({
  browser: true,
  dev: false,
  building: false,
  version: 'test'
}));

const mockGetPublicKey = vi.fn();
const mockGetRelays = vi.fn().mockResolvedValue({});
const mockGetCapabilities = vi.fn().mockReturnValue({
  getPublicKey: true, signEvent: true, nip44: true, getRelays: true
});
const mockGetNip07Signer = vi.fn().mockReturnValue({
  getPublicKey: mockGetPublicKey,
  signEvent: vi.fn(),
  encryptNip44: vi.fn(),
  decryptNip44: vi.fn()
});

vi.mock('$lib/nostr/nip07.js', () => ({
  waitForNip07: vi.fn().mockResolvedValue({ available: true }),
  getPublicKey: mockGetPublicKey,
  getRelays: mockGetRelays,
  getCapabilities: mockGetCapabilities,
  getNip07Signer: mockGetNip07Signer,
  detectNip07: vi.fn().mockReturnValue({ available: true }),
  watchNip07Availability: vi.fn().mockReturnValue(() => {})
}));

vi.mock('$lib/nostr/nip46.js', () => ({
  detectNip46: vi.fn().mockReturnValue({ available: false }),
  parseNostrConnectUri: vi.fn(),
  connectNip46: vi.fn(),
  disconnectNip46: vi.fn().mockResolvedValue(undefined),
  getNip46Signer: vi.fn(),
  getCapabilities: vi.fn().mockReturnValue({})
}));

vi.mock('$lib/nostr/pool-utils.js', () => ({
  normalizeRelayUrl: vi.fn(url => url),
  uniqueRelays: vi.fn(arr => [...new Set(arr)])
}));

const mockRequestPersistentStorage = vi.fn().mockResolvedValue(true);
vi.mock('$lib/nostr/store-interface.js', () => ({
  requestPersistentStorage: mockRequestPersistentStorage
}));

vi.mock('$lib/components/toast.js', () => ({
  toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
  removeToast: vi.fn()
}));

vi.mock('$lib/api/client.js', () => ({
  api: { setAuthProvider: vi.fn(), fetch: vi.fn() }
}));

vi.mock('$lib/nostr/encrypted-controlplane.js', () => ({
  disconnectEncryptedControlplane: vi.fn()
}));

vi.mock('$lib/stores/auth-roles.svelte.js', () => ({
  stopRoleDerivation: vi.fn()
}));

const TEST_PUBKEY = 'a'.repeat(64);

describe('auth bootstrap', () => {
  beforeEach(() => {
    vi.resetModules();
    localStorage.clear();
    vi.clearAllMocks();
    mockRequestPersistentStorage.mockResolvedValue(true);
  });

  it('restores persisted session immediately without network (no REST probe)', async () => {
    // Pre-seed a session in localStorage
    localStorage.setItem('bahia_auth_session', JSON.stringify({
      pubkey: TEST_PUBKEY,
      relays: {},
      authMethod: 'nip07',
      lastAuthenticatedAt: new Date().toISOString(),
      signerVerifiedAt: new Date().toISOString()
    }));

    const { initializeAuth, authState, isAuthenticated } = await import('../../src/lib/stores/auth.svelte.js');
    await initializeAuth();

    // Should be authenticated immediately
    expect(authState.status).toBe('authenticated');
    expect(isAuthenticated()).toBe(true);
    expect(authState.pubkey).toBe(TEST_PUBKEY);

    // No backendAuthenticated flag
    expect(authState.backendAuthenticated).toBeUndefined();

    // No REST probe happened — we didn't call fetch or /orgs
    expect(global.fetch).not.toHaveBeenCalled();
  });

  it('calls requestPersistentStorage on first authenticated boot', async () => {
    localStorage.setItem('bahia_auth_session', JSON.stringify({
      pubkey: TEST_PUBKEY,
      relays: {},
      authMethod: 'nip07',
      lastAuthenticatedAt: new Date().toISOString(),
      signerVerifiedAt: new Date().toISOString()
    }));

    const { initializeAuth } = await import('../../src/lib/stores/auth.svelte.js');
    await initializeAuth();

    // Wait for fire-and-forget persistent storage request
    await new Promise(r => setTimeout(r, 10));

    expect(mockRequestPersistentStorage).toHaveBeenCalledTimes(1);
  });

  it('transitions to unauthenticated when no persisted session exists', async () => {
    // No session in localStorage
    const { initializeAuth, authState } = await import('../../src/lib/stores/auth.svelte.js');
    await initializeAuth();

    expect(authState.status).toBe('unauthenticated');
    expect(authState.pubkey).toBeNull();
  });

  it('does not call backendAuthenticated gate', async () => {
    localStorage.setItem('bahia_auth_session', JSON.stringify({
      pubkey: TEST_PUBKEY,
      relays: {},
      authMethod: 'nip07',
      lastAuthenticatedAt: new Date().toISOString(),
      signerVerifiedAt: new Date().toISOString()
    }));

    const { initializeAuth, authState } = await import('../../src/lib/stores/auth.svelte.js');
    await initializeAuth();

    // The auth state should NOT have backendAuthenticated or compatibility fields
    expect(authState).not.toHaveProperty('backendAuthenticated');
    expect(authState).not.toHaveProperty('compatibility');
    expect(authState).not.toHaveProperty('directNip98Ready');
  });
});
