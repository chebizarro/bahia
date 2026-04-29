import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest';
import { get } from 'svelte/store';

// Mock browser environment
global.window = global;

// Mock NIP-07 module
vi.mock('../../src/lib/nostr/nip07.js', () => ({
  waitForNip07: vi.fn(),
  getPublicKey: vi.fn(),
  getRelays: vi.fn(),
  getCapabilities: vi.fn(),
  signEvent: vi.fn(),
  detectNip07: vi.fn()
}));

describe('Auth Store', () => {
  let authModule;
  let nip07Module;

  beforeEach(async () => {
    // Clear localStorage
    localStorage.clear();
    
    // Reset all mocks
    vi.clearAllMocks();
    vi.resetModules();
    
    // Import mocked NIP-07 module
    nip07Module = await import('../../src/lib/nostr/nip07.js');
    
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
      
      const state = get(authModule.authState);
      
      expect(state.status).toBe('unauthenticated');
      expect(state.extensionAvailable).toBe(true);
      expect(state.pubkey).toBeNull();
      expect(state.error).toBeNull();
    });

    it('should restore session from localStorage when available', async () => {
      const session = {
        pubkey: 'b'.repeat(64),
        relays: { 'wss://relay.test': { read: true, write: true } },
        lastAuthenticatedAt: '2026-04-29T12:00:00.000Z'
      };
      localStorage.setItem('bahia_auth_session', JSON.stringify(session));
      
      await authModule.initializeAuth();
      
      const state = get(authModule.authState);
      
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
      
      const state = get(authModule.authState);
      
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
      
      const state = get(authModule.authState);
      
      expect(state.capabilities).toEqual(capabilities);
    });

    it('should handle invalid session data gracefully', async () => {
      localStorage.setItem('bahia_auth_session', 'invalid json');
      
      await authModule.initializeAuth();
      
      const state = get(authModule.authState);
      
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
      
      const state = get(authModule.authState);
      
      expect(state.status).toBe('unauthenticated');
    });

    it('should set error status on initialization failure', async () => {
      nip07Module.waitForNip07.mockRejectedValue(new Error('Init failed'));
      
      await authModule.initializeAuth();
      
      const state = get(authModule.authState);
      
      expect(state.status).toBe('error');
      expect(state.error).toBe('Init failed');
    });
  });

  describe('login', () => {
    it('should authenticate and persist session on successful login', async () => {
      const pubkey = 'e'.repeat(64);
      const relays = { 'wss://relay.login': { read: true, write: true } };
      
      nip07Module.getPublicKey.mockResolvedValue(pubkey);
      nip07Module.getRelays.mockResolvedValue(relays);
      
      await authModule.login();
      
      const state = get(authModule.authState);
      
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
      
      const state = get(authModule.authState);
      
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
      
      const state = get(authModule.authState);
      
      // Should restore previous session
      expect(state.status).toBe('authenticated');
      expect(state.pubkey).toBe(existingSession.pubkey);
    });

    it('should set error status on login failure with no previous session', async () => {
      nip07Module.getPublicKey.mockRejectedValue(new Error('Extension error'));
      
      await expect(authModule.login()).rejects.toThrow('Extension error');
      
      const state = get(authModule.authState);
      
      expect(state.status).toBe('error');
      expect(state.error).toBe('Extension error');
    });

    it('should handle getRelays failure gracefully', async () => {
      const pubkey = 'g'.repeat(64);
      
      nip07Module.getPublicKey.mockResolvedValue(pubkey);
      nip07Module.getRelays.mockRejectedValue(new Error('Relays failed'));
      
      await authModule.login();
      
      const state = get(authModule.authState);
      
      // Should still authenticate with empty relays
      expect(state.status).toBe('authenticated');
      expect(state.pubkey).toBe(pubkey);
      expect(state.relays).toEqual({});
    });
  });

  describe('logout', () => {
    it('should clear session and reset to unauthenticated', async () => {
      // First login
      await authModule.login();
      
      // Then logout
      authModule.logout();
      
      const state = get(authModule.authState);
      
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
      
      const state = get(authModule.authState);
      
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
      
      const state = get(authModule.authState);
      
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
  });

  describe('Derived stores', () => {
    it('isAuthenticated should be false when unauthenticated', async () => {
      await authModule.initializeAuth();
      
      const isAuth = get(authModule.isAuthenticated);
      
      expect(isAuth).toBe(false);
    });

    it('isAuthenticated should be true when authenticated', async () => {
      await authModule.login();
      
      const isAuth = get(authModule.isAuthenticated);
      
      expect(isAuth).toBe(true);
    });

    it('currentUser should be null when unauthenticated', async () => {
      await authModule.initializeAuth();
      
      const user = get(authModule.currentUser);
      
      expect(user).toBeNull();
    });

    it('currentUser should contain user data when authenticated', async () => {
      const pubkey = 'h'.repeat(64);
      const relays = { 'wss://relay.user': { read: true, write: true } };
      
      nip07Module.getPublicKey.mockResolvedValue(pubkey);
      nip07Module.getRelays.mockResolvedValue(relays);
      
      await authModule.login();
      
      const user = get(authModule.currentUser);
      
      expect(user).toBeTruthy();
      expect(user.pubkey).toBe(pubkey);
      expect(user.relays).toEqual(relays);
      expect(user.lastAuthenticatedAt).toBeTruthy();
    });
  });
});
