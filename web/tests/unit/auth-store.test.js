/**
 * Auth store tests — updated for Phase 4 §6.2 auth bootstrap.
 *
 * Key behavioral changes from pre-Phase 4:
 * - No REST probe, no backendAuthenticated, no compatibility flags
 * - Persisted session = authenticated immediately
 * - Background signer verification (non-blocking)
 * - No separate pool for relay/profile hydration
 * - Roles from relay membership events, not REST /orgs
 */

import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';

global.window = global;

vi.mock('../../src/lib/stores/auth-roles.svelte.js', () => ({
  stopRoleDerivation: vi.fn()
}));

vi.mock('../../src/lib/nostr/store-interface.js', () => ({
  requestPersistentStorage: vi.fn().mockResolvedValue(true)
}));

vi.mock('../../src/lib/nostr/nip07.js', () => ({
  waitForNip07: vi.fn(),
  getPublicKey: vi.fn(),
  getRelays: vi.fn(),
  getCapabilities: vi.fn(),
  signEvent: vi.fn(),
  getNip07Signer: vi.fn(),
  detectNip07: vi.fn(),
  watchNip07Availability: vi.fn()
}));

vi.mock('../../src/lib/nostr/nip46.js', () => ({
  detectNip46: vi.fn(),
  parseNostrConnectUri: vi.fn(),
  connectNip46: vi.fn(),
  disconnectNip46: vi.fn(),
  signEvent: vi.fn(),
  getNip46Signer: vi.fn(),
  getCapabilities: vi.fn()
}));

vi.mock('../../src/lib/nostr/encrypted-controlplane.js', () => ({
  disconnectEncryptedControlplane: vi.fn()
}));

describe('Auth Store', () => {
  let authModule;
  let nip07Module;
  let nip46Module;

  beforeEach(async () => {
    localStorage.clear();
    vi.clearAllMocks();
    vi.resetModules();

    nip07Module = await import('../../src/lib/nostr/nip07.js');
    nip46Module = await import('../../src/lib/nostr/nip46.js');

    nip07Module.waitForNip07.mockResolvedValue({ available: true });
    nip07Module.getPublicKey.mockResolvedValue('a'.repeat(64));
    nip07Module.getRelays.mockResolvedValue({
      'wss://relay.example.com': { read: true, write: true }
    });
    nip07Module.getCapabilities.mockReturnValue({
      getPublicKey: true,
      signEvent: true,
      getRelays: true,
      nip04: false,
      nip44: false
    });
    nip07Module.detectNip07.mockReturnValue({ available: true });
    nip07Module.watchNip07Availability.mockImplementation((onChange, { fireImmediately = true } = {}) => {
      if (fireImmediately) onChange({ available: true });
      return vi.fn();
    });
    nip07Module.getNip07Signer.mockReturnValue({
      getPublicKey: nip07Module.getPublicKey,
      signEvent: nip07Module.signEvent,
      getRelays: nip07Module.getRelays,
      encryptNip44: vi.fn().mockResolvedValue('ciphertext'),
      decryptNip44: vi.fn().mockResolvedValue('plaintext')
    });
    nip46Module.detectNip46.mockReturnValue({ available: false, provider: null, reason: 'missing_nip46_provider' });
    nip46Module.parseNostrConnectUri.mockImplementation((uri) => ({
      uri,
      signerPubkey: '9'.repeat(64),
      relays: ['wss://relay.nip46.test'],
      secret: 'secret',
      metadata: null
    }));
    nip46Module.connectNip46.mockResolvedValue({
      uri: 'nostrconnect://' + '9'.repeat(64) + '?relay=wss://relay.nip46.test&secret=secret',
      signerPubkey: '9'.repeat(64),
      pubkey: 'a'.repeat(64),
      relays: { 'wss://relay.nip46.test': { read: true, write: true } },
      secret: 'secret',
      metadata: null,
      connectedAt: '2026-05-02T00:00:00.000Z'
    });
    nip46Module.disconnectNip46.mockResolvedValue();
    nip46Module.signEvent.mockImplementation(async (event) => ({ ...event, id: 'nip46-event-id', sig: 'nip46-signature' }));
    nip46Module.getCapabilities.mockReturnValue({ connect: true, disconnect: true, getPublicKey: true, signEvent: true, getRelays: true });
    nip46Module.getNip46Signer.mockReturnValue({
      getPublicKey: vi.fn().mockResolvedValue('a'.repeat(64)),
      signEvent: nip46Module.signEvent,
      getRelays: vi.fn().mockResolvedValue({ 'wss://relay.nip46.test': { read: true, write: true } }),
      disconnect: nip46Module.disconnectNip46
    });

    authModule = await import('../../src/lib/stores/auth.js');
  });

  afterEach(() => {
    localStorage.clear();
  });

  describe('initializeAuth', () => {
    it('should initialize with unauthenticated status when no session exists', async () => {
      await authModule.initializeAuth();
      const state = authModule.authState;
      expect(state.status).toBe('unauthenticated');
      expect(state.extensionAvailable).toBe(true);
      expect(state.pubkey).toBeNull();
      expect(state.error).toBeNull();
    });

    it('should restore session from localStorage immediately (no REST probe)', async () => {
      const session = {
        pubkey: 'b'.repeat(64),
        relays: { 'wss://relay.test': { read: true, write: true } },
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z',
        signerVerifiedAt: new Date().toISOString()
      };
      localStorage.setItem('bahia_auth_session', JSON.stringify(session));

      await authModule.initializeAuth();
      const state = authModule.authState;

      // §6.2: persisted session = AUTHENTICATED IMMEDIATELY
      expect(state.status).toBe('authenticated');
      expect(state.pubkey).toBe(session.pubkey);
      expect(state.lastAuthenticatedAt).toBe(session.lastAuthenticatedAt);
      // No REST probe — fetch was never called
      expect(global.fetch).not.toHaveBeenCalled();
    });

    it('should restore session even if extension is temporarily unavailable', async () => {
      // §6.2: persisted session trusted immediately; signer verify is background
      const session = {
        pubkey: 'c'.repeat(64),
        relays: {},
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z',
        signerVerifiedAt: new Date().toISOString()
      };
      localStorage.setItem('bahia_auth_session', JSON.stringify(session));
      nip07Module.waitForNip07.mockResolvedValue({ available: false });

      await authModule.initializeAuth();
      const state = authModule.authState;

      // Still authenticated — signer will verify in background
      expect(state.status).toBe('authenticated');
      expect(state.pubkey).toBe(session.pubkey);
    });

    it('should update capabilities when restoring session', async () => {
      const session = {
        pubkey: 'd'.repeat(64),
        relays: {},
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z',
        signerVerifiedAt: new Date().toISOString()
      };
      localStorage.setItem('bahia_auth_session', JSON.stringify(session));

      const capabilities = {
        getPublicKey: true,
        signEvent: true,
        getRelays: true,
        nip04: true,
        nip44: true
      };
      nip07Module.getCapabilities.mockReturnValue(capabilities);
      nip07Module.getPublicKey.mockResolvedValue(session.pubkey);

      await authModule.initializeAuth();
      expect(authModule.authState.capabilities).toEqual(capabilities);
    });

    it('should handle invalid session data gracefully', async () => {
      localStorage.setItem('bahia_auth_session', 'invalid json');
      await authModule.initializeAuth();
      expect(authModule.authState.status).toBe('unauthenticated');
      expect(authModule.authState.pubkey).toBeNull();
    });

    it('should handle session without pubkey', async () => {
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        relays: {},
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z'
      }));
      await authModule.initializeAuth();
      expect(authModule.authState.status).toBe('unauthenticated');
    });

    it('should ignore session with invalid pubkey format', async () => {
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        pubkey: 'not-a-hex-pubkey',
        relays: {},
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z'
      }));
      await authModule.initializeAuth();
      expect(authModule.authState.status).toBe('unauthenticated');
      expect(authModule.authState.pubkey).toBeNull();
    });

    it('should set error status on initialization failure', async () => {
      nip07Module.waitForNip07.mockRejectedValue(new Error('Init failed'));
      await authModule.initializeAuth();
      expect(authModule.authState.status).toBe('error');
      expect(authModule.authState.error).toBe('Init failed');
    });

    it('updates extension availability when the watcher reports a late provider injection', async () => {
      let handleAvailabilityChange = null;
      nip07Module.waitForNip07.mockResolvedValue({ available: false });
      nip07Module.detectNip07.mockReturnValue({ available: false });
      nip07Module.watchNip07Availability.mockImplementation((onChange, { fireImmediately = true } = {}) => {
        handleAvailabilityChange = onChange;
        if (fireImmediately) onChange({ available: false });
        return vi.fn();
      });

      await authModule.initializeAuth();
      expect(authModule.authState.extensionAvailable).toBe(false);

      nip07Module.detectNip07.mockReturnValue({ available: true });
      handleAvailabilityChange?.({ available: true });
      expect(authModule.authState.extensionAvailable).toBe(true);
    });

    it('calls requestPersistentStorage on first authenticated boot', async () => {
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        pubkey: 'e'.repeat(64),
        relays: {},
        authMethod: 'nip07',
        lastAuthenticatedAt: new Date().toISOString(),
        signerVerifiedAt: new Date().toISOString()
      }));

      await authModule.initializeAuth();
      await new Promise(r => setTimeout(r, 10));

      const { requestPersistentStorage } = await import('../../src/lib/nostr/store-interface.js');
      expect(requestPersistentStorage).toHaveBeenCalledTimes(1);
    });
  });

  describe('login', () => {
    it('should authenticate and persist session on successful login', async () => {
      const pubkey = 'e'.repeat(64);
      const relays = { 'wss://relay.login': { read: true, write: true } };

      nip07Module.getPublicKey.mockResolvedValue(pubkey);
      nip07Module.getRelays.mockResolvedValue(relays);

      await authModule.login();
      const state = authModule.authState;

      expect(state.status).toBe('authenticated');
      expect(state.pubkey).toBe(pubkey);
      expect(state.relays).toEqual(relays);
      expect(state.lastAuthenticatedAt).toBeTruthy();
      expect(state.signerVerifiedAt).toBeTruthy();
      expect(state.error).toBeNull();

      const stored = JSON.parse(localStorage.getItem('bahia_auth_session'));
      expect(stored.pubkey).toBe(pubkey);
    });

    it('should update capabilities on login', async () => {
      const capabilities = {
        getPublicKey: true,
        signEvent: true,
        getRelays: false,
        nip04: false,
        nip44: false
      };
      nip07Module.getCapabilities.mockReturnValue(capabilities);

      await authModule.login();
      expect(authModule.authState.capabilities).toEqual(capabilities);
    });

    it('does not resurrect an existing session after an explicit login failure', async () => {
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        pubkey: 'f'.repeat(64),
        relays: {},
        lastAuthenticatedAt: '2026-04-29T10:00:00.000Z'
      }));

      nip07Module.getPublicKey.mockRejectedValue(new Error('User denied'));
      await expect(authModule.login()).rejects.toThrow('User denied');

      expect(authModule.authState.status).toBe('error');
      expect(authModule.authState.pubkey).toBeNull();
      expect(authModule.authState.error).toBe('User denied');
      expect(localStorage.getItem('bahia_auth_session')).toBeNull();
    });

    it('should set error status on login failure with no previous session', async () => {
      nip07Module.getPublicKey.mockRejectedValue(new Error('Extension error'));
      await expect(authModule.login()).rejects.toThrow('Extension error');
      expect(authModule.authState.status).toBe('error');
      expect(authModule.authState.error).toBe('Extension error');
    });

    it('should handle getRelays failure gracefully', async () => {
      const pubkey = 'g'.repeat(64);
      nip07Module.getPublicKey.mockResolvedValue(pubkey);
      nip07Module.getRelays.mockRejectedValue(new Error('Relays failed'));

      await authModule.login();
      expect(authModule.authState.status).toBe('authenticated');
      expect(authModule.authState.pubkey).toBe(pubkey);
      expect(authModule.authState.relays).toEqual({});
    });
  });

  describe('encrypted signer readiness', () => {
    it('probes encrypted signer support once per signer/session target and caches success', async () => {
      const encryptNip44 = vi.fn().mockResolvedValue('ciphertext');
      nip07Module.getNip07Signer.mockReturnValue({
        getPublicKey: nip07Module.getPublicKey,
        signEvent: nip07Module.signEvent,
        getRelays: nip07Module.getRelays,
        encryptNip44,
        decryptNip44: vi.fn().mockResolvedValue('plaintext')
      });

      await authModule.login();
      await authModule.ensureEncryptedSignerReady('b'.repeat(64));
      await authModule.ensureEncryptedSignerReady('b'.repeat(64));

      expect(encryptNip44).toHaveBeenCalledTimes(1);
      expect(authModule.authState.capabilities.nip44).not.toBe(false);
    });

    it('marks the signer unavailable after a NIP-44 bridge failure', async () => {
      nip07Module.getNip07Signer.mockReturnValue({
        getPublicKey: nip07Module.getPublicKey,
        signEvent: nip07Module.signEvent,
        getRelays: nip07Module.getRelays,
        encryptNip44: vi.fn().mockRejectedValue(new Error('Failed to encrypt with NIP-44: Could not establish connection. Receiving end does not exist.')),
        decryptNip44: vi.fn()
      });

      await authModule.login();
      await expect(authModule.ensureEncryptedSignerReady('b'.repeat(64))).rejects.toThrow('Receiving end does not exist');
      expect(authModule.authState.capabilities.nip44).toBe(false);
      expect(authModule.authState.capabilities.nip44Blocker).toContain('Receiving end does not exist');
    });
  });

  describe('loginWithNostrConnect', () => {
    it('should authenticate and persist session from nostrconnect URI', async () => {
      const uri = `nostrconnect://${'9'.repeat(64)}?relay=wss://relay.nip46.test&secret=secret`;
      await authModule.loginWithNostrConnect(uri);

      expect(authModule.authState.status).toBe('authenticated');
      expect(authModule.authState.authMethod).toBe('nip46');
      expect(authModule.authState.pubkey).toBe('a'.repeat(64));
      expect(nip46Module.parseNostrConnectUri).toHaveBeenCalledWith(uri);
      expect(nip46Module.connectNip46).toHaveBeenCalled();

      const stored = JSON.parse(localStorage.getItem('bahia_auth_session'));
      expect(stored.authMethod).toBe('nip46');
      expect(stored.nip46.uri).toContain('nostrconnect://');
    });

    it('does not resurrect a persisted session after NIP-46 connection failure', async () => {
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        pubkey: 'f'.repeat(64),
        authMethod: 'nip07',
        relays: {}
      }));
      nip46Module.connectNip46.mockRejectedValueOnce(new Error('remote signer rejected'));

      await expect(authModule.loginWithNostrConnect(
        `nostrconnect://${'9'.repeat(64)}?relay=wss://relay.nip46.test&secret=secret`
      )).rejects.toThrow('remote signer rejected');

      expect(authModule.authState.status).toBe('error');
      expect(authModule.authState.pubkey).toBeNull();
      expect(localStorage.getItem('bahia_auth_session')).toBeNull();
      expect(nip46Module.disconnectNip46).toHaveBeenCalled();
    });

    it('should reconnect persisted NIP-46 session on initialize', async () => {
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        pubkey: 'a'.repeat(64),
        authMethod: 'nip46',
        relays: { 'wss://relay.nip46.test': { read: true, write: true } },
        nip46: {
          uri: `nostrconnect://${'9'.repeat(64)}?relay=wss://relay.nip46.test&secret=secret`,
          signerPubkey: '9'.repeat(64),
          relays: ['wss://relay.nip46.test'],
          secret: 'secret'
        },
        lastAuthenticatedAt: '2026-05-02T00:00:00.000Z'
      }));

      nip46Module.detectNip46.mockReturnValue({ available: true, provider: {} });
      await authModule.initializeAuth();

      // §6.2: authenticated immediately, NIP-46 reconnect in background
      expect(authModule.authState.status).toBe('authenticated');
      expect(authModule.authState.authMethod).toBe('nip46');
    });
  });

  describe('logout', () => {
    it('should clear session and reset to unauthenticated', async () => {
      await authModule.login();
      authModule.logout();

      expect(authModule.authState.status).toBe('unauthenticated');
      expect(authModule.authState.pubkey).toBeNull();
      expect(authModule.authState.relays).toEqual({});
      expect(authModule.authState.error).toBeNull();
      expect(localStorage.getItem('bahia_auth_session')).toBeNull();
    });

    it('should preserve extension availability after logout', async () => {
      await authModule.login();
      authModule.logout();
      expect(authModule.authState.extensionAvailable).toBe(true);
    });

    it('should preserve capabilities after logout if extension is available', async () => {
      const capabilities = {
        getPublicKey: true,
        signEvent: true,
        getRelays: true,
        nip04: false,
        nip44: false
      };
      nip07Module.getCapabilities.mockReturnValue(capabilities);

      await authModule.login();
      authModule.logout();
      expect(authModule.authState.capabilities).toEqual(capabilities);
    });

    it('calls stopRoleDerivation on logout', async () => {
      await authModule.login();
      authModule.logout();

      const { stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');
      expect(stopRoleDerivation).toHaveBeenCalled();
    });
  });

  describe('updateAuthProfile', () => {
    it('updates the active session profile and persists it for UserMenu hydration', async () => {
      await authModule.login();

      const profile = authModule.updateAuthProfile({
        name: 'Alice',
        display_name: 'Alice Example',
        about: 'Maintainer'
      });
      const persisted = JSON.parse(localStorage.getItem('bahia_auth_session'));

      expect(profile).toEqual({
        displayName: 'Alice Example',
        name: 'Alice',
        nip05: '',
        picture: '',
        about: 'Maintainer',
        banner: '',
        website: '',
        lud16: ''
      });
      expect(authModule.authState.profile).toEqual(profile);
      expect(persisted.profile).toEqual(profile);
    });
  });

  describe('signWithAuth', () => {
    it('should reject when not authenticated', async () => {
      await expect(authModule.signWithAuth({ kind: 1, content: 'test' })).rejects.toThrow('Not authenticated');
    });

    it('should sign event when authenticated', async () => {
      const event = { kind: 1, content: 'Hello', tags: [], created_at: Math.floor(Date.now() / 1000) };
      const signedEvent = { ...event, id: 'event-id', sig: 'signature', pubkey: 'a'.repeat(64) };
      nip07Module.signEvent.mockResolvedValue(signedEvent);

      await authModule.login();
      const result = await authModule.signWithAuth(event);

      expect(result).toEqual(signedEvent);
      expect(nip07Module.signEvent).toHaveBeenCalledWith(event);
    });

    it('should propagate signing errors', async () => {
      nip07Module.signEvent.mockRejectedValue(new Error('Signing failed'));
      await authModule.login();
      await expect(authModule.signWithAuth({ kind: 1, content: 'test' })).rejects.toThrow('Event signing failed: Signing failed');
    });

    it('should sign with NIP-46 signer when auth method is nip46', async () => {
      const uri = `nostrconnect://${'9'.repeat(64)}?relay=wss://relay.nip46.test&secret=secret`;
      const event = { kind: 1, content: 'nip46', tags: [], created_at: 1700000000 };

      await authModule.loginWithNostrConnect(uri);
      const signed = await authModule.signWithAuth(event);

      expect(nip46Module.signEvent).toHaveBeenCalledWith(event);
      expect(signed.id).toBe('nip46-event-id');
    });

    it('signHttpRequest returns a NIP-98 authorization header with absolute URL and method tags', async () => {
      nip07Module.signEvent.mockImplementation(async (event) => ({ ...event, id: 'event-id', sig: 'signature' }));
      await authModule.login();

      const header = await authModule.signHttpRequest({ method: 'post', url: '/api/v1/services' });
      const encoded = header.replace('Nostr ', '');
      const decoded = JSON.parse(Buffer.from(encoded, 'base64').toString('utf-8'));

      expect(decoded.kind).toBe(27235);
      expect(decoded.tags).toContainEqual(['u', 'http://localhost:3000/api/v1/services']);
      expect(decoded.tags).toContainEqual(['method', 'POST']);
    });
  });

  describe('Derived stores', () => {
    it('isAuthenticated should be false when unauthenticated', async () => {
      await authModule.initializeAuth();
      expect(authModule.isAuthenticated()).toBe(false);
    });

    it('isAuthenticated should be true when authenticated', async () => {
      await authModule.login();
      expect(authModule.isAuthenticated()).toBe(true);
    });

    it('currentUser should be null when unauthenticated', async () => {
      await authModule.initializeAuth();
      expect(authModule.currentUser()).toBeNull();
    });

    it('currentUser should contain user data when authenticated', async () => {
      const pubkey = 'h'.repeat(64);
      nip07Module.getPublicKey.mockResolvedValue(pubkey);
      nip07Module.getRelays.mockResolvedValue({ 'wss://relay.user': { read: true, write: true } });

      await authModule.login();
      const user = authModule.currentUser();

      expect(user).toBeTruthy();
      expect(user.pubkey).toBe(pubkey);
      expect(user.relays).toEqual({ 'wss://relay.user': { read: true, write: true } });
      expect(user.lastAuthenticatedAt).toBeTruthy();
    });
  });
});
