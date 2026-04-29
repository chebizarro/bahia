// Auth/session store for NIP-07 browser extension authentication
// UI identity/session state only - does NOT manage bahia_token

import { writable, derived } from 'svelte/store';
import { browser } from '$app/environment';
import {
  waitForNip07,
  getPublicKey,
  getRelays,
  getCapabilities,
  signEvent as nip07SignEvent,
  detectNip07
} from '$lib/nostr/nip07.js';

// LocalStorage key for session persistence
const SESSION_KEY = 'bahia_auth_session';

// Initial state
const initialState = {
  status: 'unknown', // unknown | checking | unauthenticated | authenticating | authenticated | error
  extensionAvailable: false,
  pubkey: null,
  relays: {},
  capabilities: {},
  error: null,
  lastAuthenticatedAt: null,
  backendAuthenticated: false,
  tokenExpiresAt: null
};

// Load persisted session from localStorage
function loadPersistedSession() {
  if (!browser) return null;
  
  try {
    const stored = localStorage.getItem(SESSION_KEY);
    if (!stored) return null;
    
    const session = JSON.parse(stored);
    
    // Validate session structure
    if (!session.pubkey || typeof session.pubkey !== 'string') {
      return null;
    }
    
    return {
      pubkey: session.pubkey,
      relays: session.relays || {},
      lastAuthenticatedAt: session.lastAuthenticatedAt
    };
  } catch (error) {
    console.warn('Failed to load persisted auth session:', error);
    return null;
  }
}

// Persist session to localStorage (only non-secret metadata)
function persistSession(pubkey, relays) {
  if (!browser) return;
  
  try {
    const session = {
      pubkey,
      relays,
      lastAuthenticatedAt: new Date().toISOString()
    };
    localStorage.setItem(SESSION_KEY, JSON.stringify(session));
  } catch (error) {
    console.error('Failed to persist auth session:', error);
  }
}

// Clear persisted session
function clearPersistedSession() {
  if (!browser) return;
  
  try {
    localStorage.removeItem(SESSION_KEY);
  } catch (error) {
    console.error('Failed to clear auth session:', error);
  }
}

// Create auth state store
export const authState = writable(initialState);

// Derived store: simple authenticated flag
export const isAuthenticated = derived(
  authState,
  $auth => $auth.status === 'authenticated'
);

// Derived store: current user info
export const currentUser = derived(
  authState,
  $auth => {
    if ($auth.status !== 'authenticated' || !$auth.pubkey) {
      return null;
    }
    
    return {
      pubkey: $auth.pubkey,
      relays: $auth.relays,
      capabilities: $auth.capabilities,
      lastAuthenticatedAt: $auth.lastAuthenticatedAt
    };
  }
);

// Track in-flight login to prevent duplicate calls
let loginInProgress = null;

/**
 * Initialize auth system
 * Should be called once on app mount
 */
export async function initializeAuth() {
  authState.update(state => ({ ...state, status: 'checking' }));
  
  try {
    // Wait for extension to become available
    const { available } = await waitForNip07({ timeoutMs: 1500 });
    
    // Update extension availability
    authState.update(state => ({ 
      ...state, 
      extensionAvailable: available 
    }));
    
    // Try to restore previous session
    const persisted = loadPersistedSession();
    
    if (persisted && available) {
      // Restore session with capabilities
      const capabilities = getCapabilities();
      
      authState.update(state => ({
        ...state,
        status: 'authenticated',
        pubkey: persisted.pubkey,
        relays: persisted.relays,
        capabilities,
        lastAuthenticatedAt: persisted.lastAuthenticatedAt,
        error: null
      }));
    } else {
      // No valid session
      authState.update(state => ({ 
        ...state, 
        status: 'unauthenticated',
        error: null
      }));
    }
  } catch (error) {
    console.error('Auth initialization failed:', error);
    authState.update(state => ({
      ...state,
      status: 'error',
      error: error.message
    }));
  }
}

/**
 * Refresh extension availability status
 * Useful if extension was installed after page load
 */
export async function refreshExtensionStatus() {
  const { available } = detectNip07();
  
  authState.update(state => ({
    ...state,
    extensionAvailable: available,
    capabilities: available ? getCapabilities() : {}
  }));
  
  return available;
}

/**
 * Login with NIP-07 extension
 * Prompts user for public key and fetches session data
 */
export async function login() {
  // Prevent duplicate login calls
  if (loginInProgress) {
    return loginInProgress;
  }
  
  // Check if already authenticating
  const currentState = await new Promise(resolve => {
    const unsubscribe = authState.subscribe(state => {
      unsubscribe();
      resolve(state);
    });
  });
  
  if (currentState.status === 'authenticating') {
    console.warn('Login already in progress');
    return;
  }
  
  // Create login promise
  loginInProgress = (async () => {
    try {
      authState.update(state => ({ 
        ...state, 
        status: 'authenticating',
        error: null
      }));
      
      // Get public key from extension (will prompt user)
      const pubkey = await getPublicKey();
      
      // Get additional session data
      const [relays, capabilities] = await Promise.all([
        getRelays().catch(() => ({})),
        Promise.resolve(getCapabilities())
      ]);
      
      // Update state
      authState.update(state => ({
        ...state,
        status: 'authenticated',
        pubkey,
        relays,
        capabilities,
        lastAuthenticatedAt: new Date().toISOString(),
        error: null
      }));
      
      // Persist session
      persistSession(pubkey, relays);
      
    } catch (error) {
      console.error('Login failed:', error);
      
      // Restore previous state if there was one
      const persisted = loadPersistedSession();
      
      if (persisted) {
        authState.update(state => ({
          ...state,
          status: 'authenticated',
          pubkey: persisted.pubkey,
          relays: persisted.relays,
          capabilities: getCapabilities(),
          lastAuthenticatedAt: persisted.lastAuthenticatedAt,
          error: null
        }));
      } else {
        authState.update(state => ({
          ...state,
          status: 'error',
          error: error.message
        }));
      }
      
      throw error;
    } finally {
      loginInProgress = null;
    }
  })();
  
  return loginInProgress;
}

/**
 * Logout and clear session
 * Clears both NIP-07 session and backend API token
 */
export function logout() {
  clearPersistedSession();
  
  // Clear backend API token
  if (browser) {
    import('$lib/api/client.js').then(({ api }) => {
      if (api) {
        api.setToken(null);
      }
    }).catch(err => {
      console.error('Failed to clear API token:', err);
    });
  }
  
  authState.update(state => ({
    ...initialState,
    status: 'unauthenticated',
    extensionAvailable: state.extensionAvailable,
    capabilities: state.extensionAvailable ? getCapabilities() : {},
    backendAuthenticated: false,
    tokenExpiresAt: null
  }));
}

/**
 * Sign an event using the authenticated session
 * @param {Object} event - Nostr event to sign
 * @returns {Promise<Object>} Signed event
 * @throws {Error} If not authenticated or signing fails
 */
export async function signWithAuth(event) {
  const currentState = await new Promise(resolve => {
    const unsubscribe = authState.subscribe(state => {
      unsubscribe();
      resolve(state);
    });
  });
  
  if (currentState.status !== 'authenticated') {
    throw new Error('Not authenticated - please login first');
  }
  
  try {
    const signedEvent = await nip07SignEvent(event);
    return signedEvent;
  } catch (error) {
    console.error('Failed to sign event:', error);
    throw new Error(`Event signing failed: ${error.message}`);
  }
}

/**
 * Authenticate with the backend API by exchanging a NIP-98 signed event for a JWT token
 * This requires an active NIP-07 session
 * @throws {Error} If not in browser, not authenticated, or exchange fails
 */
export async function authenticateBackend() {
  if (!browser) {
    throw new Error('authenticateBackend() can only be called in the browser');
  }

  // Import API client
  const { api } = await import('$lib/api/client.js');
  if (!api) {
    throw new Error('API client not available');
  }

  // Get current auth state
  const currentState = await new Promise(resolve => {
    const unsubscribe = authState.subscribe(state => {
      unsubscribe();
      resolve(state);
    });
  });

  // Ensure NIP-07 auth exists
  if (currentState.status !== 'authenticated' || !currentState.pubkey) {
    // Try to login first
    await login();
    
    // Get updated state
    const updatedState = await new Promise(resolve => {
      const unsubscribe = authState.subscribe(state => {
        unsubscribe();
        resolve(state);
      });
    });
    
    if (updatedState.status !== 'authenticated' || !updatedState.pubkey) {
      throw new Error('NIP-07 authentication required before backend auth');
    }
  }

  try {
    // Build unsigned NIP-98 event
    const now = Math.floor(Date.now() / 1000);
    const unsignedEvent = {
      kind: 27235,
      pubkey: currentState.pubkey,
      created_at: now,
      tags: [
        ['u', '/api/v1/auth/nostr'],
        ['method', 'POST']
      ],
      content: ''
    };

    // Sign the event
    const signedEvent = await signWithAuth(unsignedEvent);

    // Exchange for JWT
    const response = await api.exchangeNostrAuth(signedEvent);

    // Update API client with new token
    api.setToken(response.token);

    // Update auth state
    authState.update(state => ({
      ...state,
      backendAuthenticated: true,
      tokenExpiresAt: response.expires_at,
      error: null
    }));

    return response;
  } catch (error) {
    console.error('Backend authentication failed:', error);
    
    // Update state to reflect failure
    authState.update(state => ({
      ...state,
      backendAuthenticated: false,
      tokenExpiresAt: null,
      error: error.message
    }));

    throw error;
  }
}
