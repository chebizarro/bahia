import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';

// Mock browser environment
global.window = global;

const systemStoreMock = vi.hoisted(() => ({
  info: null,
  currentSystemInfo: vi.fn(() => systemStoreMock.info),
  loadSystemInfo: vi.fn(async () => systemStoreMock.info)
}));

vi.mock('../../src/lib/stores/system.svelte.js', () => ({
  currentSystemInfo: systemStoreMock.currentSystemInfo,
  loadSystemInfo: systemStoreMock.loadSystemInfo
}));

// Mock NIP-07 module
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

// Mock NIP-46 module
vi.mock('../../src/lib/nostr/nip46.js', () => ({
  detectNip46: vi.fn(),
  parseNostrConnectUri: vi.fn(),
  connectNip46: vi.fn(),
  disconnectNip46: vi.fn(),
  signEvent: vi.fn(),
  getNip46Signer: vi.fn(),
  getCapabilities: vi.fn()
}));

describe('Auth Store', () => {
  let authModule;
  let nip07Module;
  let nip46Module;

  beforeEach(async () => {
    // Clear localStorage
    localStorage.clear();
    
    // Reset all mocks
    vi.clearAllMocks();
    vi.resetModules();
    systemStoreMock.info = null;
    
    // Import mocked NIP-07 module
    nip07Module = await import('../../src/lib/nostr/nip07.js');
    nip46Module = await import('../../src/lib/nostr/nip46.js');
    
    // Set default mock implementations
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
      getRelays: nip07Module.getRelays
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
      uri: 'nostrconnect://'+ '9'.repeat(64) + '?relay=wss://relay.nip46.test&secret=secret',
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
    global.fetch = vi.fn().mockResolvedValue({
      ok: true,
      json: async () => ({ data: { token: 'test-token', expires_at: new Date(Date.now() + 3600000).toISOString() } })
    });
    
    // Dynamically import auth module to get fresh state
    authModule = await import('../../src/lib/stores/auth.js');
  });

  afterEach(() => {
    // Clean up any persisted sessions
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
      expect(state.compatibility.restNip98Ready).toBe(false);
      expect(state.compatibility.restNip98LastError).toBeNull();
    });

    it('should restore session from localStorage when available', async () => {
      const session = {
        pubkey: 'b'.repeat(64),
        relays: { 'wss://relay.test': { read: true, write: true } },
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z'
      };
      localStorage.setItem('bahia_auth_session', JSON.stringify(session));
      
      await authModule.initializeAuth();
      
      const state = authModule.authState;
      
      expect(state.status).toBe('authenticated');
      expect(state.pubkey).toBe(session.pubkey);
      expect(state.relays).toEqual(session.relays);
      expect(state.lastAuthenticatedAt).toBe(session.lastAuthenticatedAt);
    });

    it('should not restore session if extension is unavailable', async () => {
      const session = {
        pubkey: 'c'.repeat(64),
        relays: {},
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z'
      };
      localStorage.setItem('bahia_auth_session', JSON.stringify(session));
      
      nip07Module.waitForNip07.mockResolvedValue({ available: false });
      
      await authModule.initializeAuth();
      
      const state = authModule.authState;
      
      expect(state.status).toBe('unauthenticated');
      expect(state.extensionAvailable).toBe(false);
      expect(state.pubkey).toBeNull();
    });

    it('should update capabilities when restoring session', async () => {
      const session = {
        pubkey: 'd'.repeat(64),
        relays: {},
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z'
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
      
      await authModule.initializeAuth();
      
      const state = authModule.authState;
      
      expect(state.capabilities).toEqual(capabilities);
    });

    it('keeps signer session authenticated when direct NIP-98 is unavailable during initialize', async () => {
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        pubkey: 'd'.repeat(64),
        relays: { 'wss://relay.test': { read: true, write: true } },
        authMethod: 'nip07',
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z'
      }));

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { features: { direct_nostr_http_auth: false } } })
      });

      await authModule.initializeAuth();

      expect(authModule.authState.status).toBe('authenticated');
      expect(authModule.authState.error).toBeNull();
      expect(authModule.authState.compatibility.restNip98Ready).toBe(false);
      expect(authModule.authState.compatibility.restNip98LastError).toContain('not enabled');
    });

    it('should handle invalid session data gracefully', async () => {
      localStorage.setItem('bahia_auth_session', 'invalid json');
      
      await authModule.initializeAuth();
      
      const state = authModule.authState;
      
      expect(state.status).toBe('unauthenticated');
      expect(state.pubkey).toBeNull();
    });

    it('should handle session without pubkey', async () => {
      const invalidSession = {
        relays: {},
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z'
      };
      localStorage.setItem('bahia_auth_session', JSON.stringify(invalidSession));
      
      await authModule.initializeAuth();
      
      const state = authModule.authState;
      
      expect(state.status).toBe('unauthenticated');
    });

    it('should ignore session with invalid pubkey format', async () => {
      const invalidSession = {
        pubkey: 'not-a-hex-pubkey',
        relays: { 'wss://relay.test': { read: true, write: true } },
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z'
      };
      localStorage.setItem('bahia_auth_session', JSON.stringify(invalidSession));

      await authModule.initializeAuth();

      const state = authModule.authState;

      expect(state.status).toBe('unauthenticated');
      expect(state.pubkey).toBeNull();
    });

    it('should set error status on initialization failure', async () => {
      nip07Module.waitForNip07.mockRejectedValue(new Error('Init failed'));
      
      await authModule.initializeAuth();
      
      const state = authModule.authState;
      
      expect(state.status).toBe('error');
      expect(state.error).toBe('Init failed');
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
      expect(state.error).toBeNull();
      
      // Check localStorage persistence
      const stored = JSON.parse(localStorage.getItem('bahia_auth_session'));
      expect(stored.pubkey).toBe(pubkey);
      expect(stored.relays).toEqual(relays);
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
      
      const state = authModule.authState;
      
      expect(state.capabilities).toEqual(capabilities);
    });

    it('should handle login failure and preserve existing session if any', async () => {
      // Set up existing session
      const existingSession = {
        pubkey: 'f'.repeat(64),
        relays: {},
        lastAuthenticatedAt: '2026-04-29T10:00:00.000Z'
      };
      localStorage.setItem('bahia_auth_session', JSON.stringify(existingSession));
      
      // Make login fail
      nip07Module.getPublicKey.mockRejectedValue(new Error('User denied'));
      
      await expect(authModule.login()).rejects.toThrow('User denied');
      
      const state = authModule.authState;
      
      // Should restore previous session
      expect(state.status).toBe('authenticated');
      expect(state.pubkey).toBe(existingSession.pubkey);
    });

    it('should set error status on login failure with no previous session', async () => {
      nip07Module.getPublicKey.mockRejectedValue(new Error('Extension error'));
      
      await expect(authModule.login()).rejects.toThrow('Extension error');
      
      const state = authModule.authState;
      
      expect(state.status).toBe('error');
      expect(state.error).toBe('Extension error');
    });

    it('should handle getRelays failure gracefully', async () => {
      const pubkey = 'g'.repeat(64);
      
      nip07Module.getPublicKey.mockResolvedValue(pubkey);
      nip07Module.getRelays.mockRejectedValue(new Error('Relays failed'));
      
      await authModule.login();
      
      const state = authModule.authState;
      
      // Should still authenticate with empty relays
      expect(state.status).toBe('authenticated');
      expect(state.pubkey).toBe(pubkey);
      expect(state.relays).toEqual({});
    });

    it('installs direct NIP-98 provider instead of exchanging JWT when advertised', async () => {
      const signedEvent = { id: 'event-id', sig: 'signature', pubkey: 'a'.repeat(64), kind: 27235, tags: [] };
      nip07Module.signEvent.mockImplementation(async (event) => ({ ...event, ...signedEvent, tags: event.tags }));
      systemStoreMock.info = { features: { direct_nostr_http_auth: true } };
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: [] })
      });

      await authModule.login();
      const { api } = await import('../../src/lib/api/client.js');
      await api.listServices();

      expect(authModule.authState.directNip98Ready).toBe(true);
      expect(authModule.authState.compatibility.restNip98Advertised).toBe(true);
      expect(authModule.authState.compatibility.restNip98Ready).toBe(true);
      expect(authModule.authState.compatibility.restNip98LastError).toBeNull();
      expect(localStorage.getItem('bahia_token')).toBeNull();
      expect(global.fetch).not.toHaveBeenCalledWith('/api/v1/auth/nostr', expect.any(Object));
      expect(global.fetch).toHaveBeenLastCalledWith('/api/v1/services', expect.objectContaining({
        headers: expect.objectContaining({ Authorization: expect.stringMatching(/^Nostr /) })
      }));
    });

    it('does not fall back to JWT exchange when direct NIP-98 is unavailable', async () => {
      localStorage.setItem('bahia_token', 'legacy-token');
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { features: { nostr_auth_exchange: true, direct_nostr_http_auth: false } } })
      });

      await authModule.login();

      expect(authModule.authState.backendAuthenticated).toBe(false);
      expect(authModule.authState.directNip98Ready).toBe(false);
      expect(authModule.authState.status).toBe('authenticated');
      expect(authModule.authState.error).toBeNull();
      expect(authModule.authState.compatibility.restNip98Ready).toBe(false);
      expect(authModule.authState.compatibility.restNip98LastError).toContain('not enabled');
      expect(localStorage.getItem('bahia_token')).toBeNull();
      expect(global.fetch).not.toHaveBeenCalledWith('/api/v1/auth/nostr', expect.any(Object));
    });
  });

  describe('loginWithNostrConnect', () => {
    it('should authenticate and persist session from nostrconnect URI', async () => {
      const uri = `nostrconnect://${'9'.repeat(64)}?relay=wss://relay.nip46.test&secret=secret`;

      await authModule.loginWithNostrConnect(uri);

      const state = authModule.authState;
      expect(state.status).toBe('authenticated');
      expect(state.authMethod).toBe('nip46');
      expect(state.pubkey).toBe('a'.repeat(64));
      expect(nip46Module.parseNostrConnectUri).toHaveBeenCalledWith(uri);
      expect(nip46Module.connectNip46).toHaveBeenCalled();

      const stored = JSON.parse(localStorage.getItem('bahia_auth_session'));
      expect(stored.authMethod).toBe('nip46');
      expect(stored.nip46.uri).toContain('nostrconnect://');
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

      expect(nip46Module.connectNip46).toHaveBeenCalled();
      expect(authModule.authState.status).toBe('authenticated');
      expect(authModule.authState.authMethod).toBe('nip46');
    });
  });

  describe('logout', () => {
    it('should clear session and reset to unauthenticated', async () => {
      // First login
      await authModule.login();
      
      // Then logout
      authModule.logout();
      
      const state = authModule.authState;
      
      expect(state.status).toBe('unauthenticated');
      expect(state.pubkey).toBeNull();
      expect(state.relays).toEqual({});
      expect(state.error).toBeNull();
      
      // Check localStorage is cleared
      expect(localStorage.getItem('bahia_auth_session')).toBeNull();
    });

    it('should preserve extension availability after logout', async () => {
      await authModule.login();
      
      authModule.logout();
      
      const state = authModule.authState;
      
      expect(state.extensionAvailable).toBe(true);
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
      
      const state = authModule.authState;
      
      expect(state.capabilities).toEqual(capabilities);
    });
  });

  describe('signWithAuth', () => {
    it('should reject when not authenticated', async () => {
      const event = { kind: 1, content: 'test' };
      
      await expect(authModule.signWithAuth(event)).rejects.toThrow('Not authenticated');
    });

    it('should sign event when authenticated', async () => {
      const event = {
        kind: 1,
        content: 'Hello',
        tags: [],
        created_at: Math.floor(Date.now() / 1000)
      };
      
      const signedEvent = {
        ...event,
        id: 'event-id',
        sig: 'signature',
        pubkey: 'a'.repeat(64)
      };
      
      nip07Module.signEvent.mockResolvedValue(signedEvent);
      
      // First authenticate
      await authModule.login();
      
      // Then sign
      const result = await authModule.signWithAuth(event);
      
      expect(result).toEqual(signedEvent);
      expect(nip07Module.signEvent).toHaveBeenCalledWith(event);
    });

    it('should propagate signing errors', async () => {
      nip07Module.signEvent.mockRejectedValue(new Error('Signing failed'));
      
      // Authenticate first
      await authModule.login();
      
      const event = { kind: 1, content: 'test' };
      
      await expect(authModule.signWithAuth(event)).rejects.toThrow('Event signing failed: Signing failed');
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
      
      const isAuth = authModule.isAuthenticated();
      
      expect(isAuth).toBe(false);
    });

    it('isAuthenticated should be true when authenticated', async () => {
      await authModule.login();
      
      const isAuth = authModule.isAuthenticated();
      
      expect(isAuth).toBe(true);
    });

    it('currentUser should be null when unauthenticated', async () => {
      await authModule.initializeAuth();
      
      const user = authModule.currentUser();
      
      expect(user).toBeNull();
    });

    it('currentUser should contain user data when authenticated', async () => {
      const pubkey = 'h'.repeat(64);
      const relays = { 'wss://relay.user': { read: true, write: true } };
      
      nip07Module.getPublicKey.mockResolvedValue(pubkey);
      nip07Module.getRelays.mockResolvedValue(relays);
      
      await authModule.login();
      
      const user = authModule.currentUser();
      
      expect(user).toBeTruthy();
      expect(user.pubkey).toBe(pubkey);
      expect(user.relays).toEqual(relays);
      expect(user.lastAuthenticatedAt).toBeTruthy();
    });
  });
});
