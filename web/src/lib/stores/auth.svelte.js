// Auth/session store for NIP-07 browser extension authentication
// UI identity/session state only - does NOT manage bahia_token as first-party auth state

import { browser } from '$app/environment';
import { toast } from '$lib/components/toast.js';
import {
  waitForNip07,
  getPublicKey,
  getRelays,
  getCapabilities,
  signEvent as nip07SignEvent,
  detectNip07
} from '$lib/nostr/nip07.js';
import { supportsDirectNip98Auth } from '$lib/auth/capabilities.js';

const SESSION_KEY = 'bahia_auth_session';

const initialState = {
  status: 'unknown',
  extensionAvailable: false,
  pubkey: null,
  relays: {},
  capabilities: {},
  error: null,
  lastAuthenticatedAt: null,
  backendAuthenticated: false,
  directNip98Ready: false
};

export const authState = $state({ ...initialState });

export function isAuthenticated() {
  return authState.status === 'authenticated';
}

export function currentUser() {
  if (authState.status !== 'authenticated' || !authState.pubkey) return null;
  return {
    pubkey: authState.pubkey,
    relays: authState.relays,
    capabilities: authState.capabilities,
    lastAuthenticatedAt: authState.lastAuthenticatedAt
  };
}

function updateAuthState(patch) {
  Object.assign(authState, patch);
}

function isValidHexPubkey(pubkey) {
  return typeof pubkey === 'string' && /^[0-9a-fA-F]{64}$/.test(pubkey);
}

function loadPersistedSession() {
  if (!browser) return null;
  try {
    const stored = localStorage.getItem(SESSION_KEY);
    if (!stored) return null;
    const session = JSON.parse(stored);
    if (!isValidHexPubkey(session.pubkey)) return null;
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

function persistSession(pubkey, relays) {
  if (!browser) return;
  try {
    localStorage.setItem(SESSION_KEY, JSON.stringify({ pubkey, relays, lastAuthenticatedAt: new Date().toISOString() }));
  } catch (error) {
    console.error('Failed to persist auth session:', error);
  }
}

function clearPersistedSession() {
  if (!browser) return;
  try {
    localStorage.removeItem(SESSION_KEY);
  } catch (error) {
    console.error('Failed to clear auth session:', error);
  }
}

function base64Encode(value) {
  if (typeof btoa === 'function') return btoa(value);
  return Buffer.from(value, 'utf-8').toString('base64');
}

function absoluteHTTPURL(url) {
  if (/^https?:\/\//i.test(url)) return url;
  const origin = browser && window?.location?.origin ? window.location.origin : 'http://localhost';
  return new URL(url, origin).toString();
}

function installDirectNip98Provider(api) {
  api.setAuthProvider({ getAuthorizationHeader: ({ method, url }) => signHttpRequest({ method, url }) });
  if (browser) localStorage.removeItem('bahia_token');
}

async function configureBackendAuth(pubkey, { requireBackend = false } = {}) {
  if (!browser) throw new Error('Backend auth requires browser environment');
  const { api } = await import('$lib/api/client.js');
  if (!api) throw new Error('API client not available');

  const systemInfo = await api.getSystemInfo().catch(() => null);
  if (supportsDirectNip98Auth(systemInfo)) {
    installDirectNip98Provider(api);
    updateAuthState({ backendAuthenticated: true, directNip98Ready: true, error: null });
    return { method: 'nip98', pubkey };
  }

  api.setAuthProvider(null);
  if (browser) localStorage.removeItem('bahia_token');

  updateAuthState({
    backendAuthenticated: false,
    directNip98Ready: false,
    error: requireBackend ? 'Backend direct NIP-98 auth is not enabled' : null
  });
  if (requireBackend) throw new Error('Backend direct NIP-98 auth is not enabled');
  return null;
}

async function authenticateBackendInternal(pubkey) {
  return configureBackendAuth(pubkey);
}

let initializeInProgress = null;
let loginInProgress = null;

export async function initializeAuth() {
  if (initializeInProgress) return initializeInProgress;
  initializeInProgress = (async () => {
    updateAuthState({ status: 'checking' });
    try {
      const { available } = await waitForNip07({ timeoutMs: 1500 });
      updateAuthState({ extensionAvailable: available });
      const persisted = loadPersistedSession();
      if (browser) localStorage.removeItem('bahia_token');

      if (persisted && available) {
        const capabilities = getCapabilities();
        updateAuthState({
          status: 'authenticated',
          pubkey: persisted.pubkey,
          relays: persisted.relays,
          capabilities,
          lastAuthenticatedAt: persisted.lastAuthenticatedAt,
          backendAuthenticated: false,
          directNip98Ready: false,
          error: null
        });
        try {
          await configureBackendAuth(persisted.pubkey, { exchangeIfNeeded: false });
        } catch (backendError) {
          console.warn('Backend auth provider initialization failed:', backendError.message);
        }
      } else {
        updateAuthState({ status: 'unauthenticated', backendAuthenticated: false, directNip98Ready: false, error: null });
        if (!available) {
          toast.warning('No Nostr extension detected. Install a NIP-07 extension like Alby or nos2x to sign in.');
        }
      }
    } catch (error) {
      console.error('Auth initialization failed:', error);
      updateAuthState({ status: 'error', error: error.message });
    } finally {
      initializeInProgress = null;
    }
  })();
  return initializeInProgress;
}

export async function refreshExtensionStatus() {
  const { available } = detectNip07();
  updateAuthState({ extensionAvailable: available, capabilities: available ? getCapabilities() : {} });
  return available;
}

export async function login() {
  if (loginInProgress) return loginInProgress;
  if (authState.status === 'authenticating') {
    console.warn('Login already in progress');
    return;
  }
  loginInProgress = (async () => {
    try {
      updateAuthState({ status: 'authenticating', error: null });
      const pubkey = await getPublicKey();
      const [relays, capabilities] = await Promise.all([getRelays().catch(() => ({})), Promise.resolve(getCapabilities())]);
      updateAuthState({
        status: 'authenticated',
        extensionAvailable: true,
        pubkey,
        relays,
        capabilities,
        lastAuthenticatedAt: new Date().toISOString(),
        error: null
      });
      persistSession(pubkey, relays);
      try {
        await authenticateBackendInternal(pubkey);
        toast.success('Signed in successfully');
      } catch (backendError) {
        console.warn('Backend authentication failed:', backendError.message);
        updateAuthState({ backendAuthenticated: false, directNip98Ready: false, error: `Signed in, but backend auth failed: ${backendError.message}` });
        toast.error(`Backend auth failed: ${backendError.message}. Some features may be unavailable.`);
      }
    } catch (error) {
      console.error('Login failed:', error);
      const persisted = loadPersistedSession();
      if (persisted) {
        updateAuthState({ status: 'authenticated', pubkey: persisted.pubkey, relays: persisted.relays, capabilities: getCapabilities(), lastAuthenticatedAt: persisted.lastAuthenticatedAt, error: null });
      } else {
        updateAuthState({ status: 'error', error: error.message });
      }
      throw error;
    } finally {
      loginInProgress = null;
    }
  })();
  return loginInProgress;
}

export function logout() {
  clearPersistedSession();
  if (browser) {
    import('$lib/api/client.js').then(({ api }) => {
      if (api) {
        api.setAuthProvider(null);
        if (browser) localStorage.removeItem('bahia_token');
      }
    }).catch(err => console.error('Failed to clear API token:', err));
  }
  Object.assign(authState, {
    ...initialState,
    status: 'unauthenticated',
    extensionAvailable: authState.extensionAvailable,
    capabilities: authState.extensionAvailable ? getCapabilities() : {},
    backendAuthenticated: false,
    directNip98Ready: false
  });
}

export async function signWithAuth(event) {
  if (authState.status !== 'authenticated') throw new Error('Not authenticated - please login first');
  try {
    return await nip07SignEvent(event);
  } catch (error) {
    console.error('Failed to sign event:', error);
    throw new Error(`Event signing failed: ${error.message}`);
  }
}

export async function signHttpRequest({ method = 'GET', url }) {
  if (authState.status !== 'authenticated' || !authState.pubkey) {
    throw new Error('Not authenticated - please login first');
  }
  if (!url) throw new Error('HTTP URL is required for NIP-98 signing');
  const unsignedEvent = {
    kind: 27235,
    pubkey: authState.pubkey,
    created_at: Math.floor(Date.now() / 1000),
    tags: [['u', absoluteHTTPURL(url)], ['method', method.toUpperCase()]],
    content: ''
  };
  const signedEvent = await nip07SignEvent(unsignedEvent);
  return `Nostr ${base64Encode(JSON.stringify(signedEvent))}`;
}

export async function authenticateBackend() {
  if (!browser) throw new Error('authenticateBackend() can only be called in the browser');
  if (authState.status !== 'authenticated' || !authState.pubkey) {
    await login();
    if (authState.status !== 'authenticated' || !authState.pubkey) {
      throw new Error('NIP-07 authentication required before backend auth');
    }
  }
  try {
    return await configureBackendAuth(authState.pubkey, { requireBackend: true });
  } catch (error) {
    console.error('Backend authentication failed:', error);
    updateAuthState({ backendAuthenticated: false, directNip98Ready: false, error: error.message });
    throw error;
  }
}
