// Auth/session store for NIP-07 extension + NIP-46 Nostr Connect authentication
// UI identity/session state only - does NOT manage bahia_token as first-party auth state

import { browser } from '$app/environment';
import { toast } from '$lib/components/toast.js';
import {
  waitForNip07,
  getPublicKey as getNip07PublicKey,
  getRelays as getNip07Relays,
  getCapabilities as getNip07Capabilities,
  getNip07Signer,
  detectNip07
} from '$lib/nostr/nip07.js';
import {
  detectNip46,
  parseNostrConnectUri,
  connectNip46,
  disconnectNip46,
  getNip46Signer,
  getCapabilities as getNip46Capabilities
} from '$lib/nostr/nip46.js';
import { supportsDirectNip98Auth } from '$lib/auth/capabilities.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

const SESSION_KEY = 'bahia_auth_session';

const initialState = {
  status: 'unknown',
  extensionAvailable: false,
  nip46Available: false,
  authMethod: null,
  pubkey: null,
  relays: {},
  capabilities: {},
  error: null,
  lastAuthenticatedAt: null,
  compatibility: {
    restNip98Advertised: false,
    restNip98Ready: false,
    restNip98LastError: null
  },
  backendAuthenticated: false,
  directNip98Ready: false,
  nip46: null
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
    authMethod: authState.authMethod,
    lastAuthenticatedAt: authState.lastAuthenticatedAt
  };
}

function updateAuthState(patch) {
  Object.assign(authState, patch);
}

function isValidHexPubkey(pubkey) {
  return typeof pubkey === 'string' && /^[0-9a-fA-F]{64}$/.test(pubkey);
}

function compatibilityPatch({ restNip98Advertised = false, restNip98Ready = false, restNip98LastError = null } = {}) {
  return {
    compatibility: {
      restNip98Advertised,
      restNip98Ready,
      restNip98LastError
    },
    backendAuthenticated: restNip98Ready,
    directNip98Ready: restNip98Ready
  };
}

function resolveActiveSigner() {
  if (authState.authMethod === 'nip46') {
    return getNip46Signer();
  }
  return getNip07Signer();
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
      authMethod: session.authMethod || 'nip07',
      nip46: session.nip46 || null,
      lastAuthenticatedAt: session.lastAuthenticatedAt
    };
  } catch (error) {
    console.warn('Failed to load persisted auth session:', error);
    return null;
  }
}

function persistSession({ pubkey, relays, authMethod = 'nip07', nip46 = null }) {
  if (!browser) return;
  try {
    localStorage.setItem(
      SESSION_KEY,
      JSON.stringify({ pubkey, relays, authMethod, nip46, lastAuthenticatedAt: new Date().toISOString() })
    );
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

  const systemInfo = currentSystemInfo() || await loadSystemInfo().catch(() => null);
  if (supportsDirectNip98Auth(systemInfo)) {
    installDirectNip98Provider(api);
    updateAuthState({ ...compatibilityPatch({ restNip98Advertised: true, restNip98Ready: true }), error: null });
    return { method: 'nip98', pubkey };
  }

  api.setAuthProvider(null);
  if (browser) localStorage.removeItem('bahia_token');

  const compatibilityError = 'Backend direct NIP-98 auth is not enabled';
  updateAuthState(compatibilityPatch({
    restNip98Advertised: false,
    restNip98Ready: false,
    restNip98LastError: compatibilityError
  }));

  if (requireBackend) throw new Error(compatibilityError);
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
      const [{ available: extensionAvailable }, { available: nip46Available }] = await Promise.all([
        waitForNip07({ timeoutMs: 1500 }),
        Promise.resolve(detectNip46())
      ]);
      updateAuthState({ extensionAvailable, nip46Available });
      const persisted = loadPersistedSession();
      if (browser) localStorage.removeItem('bahia_token');

      if (persisted) {
        const supportsMethod =
          (persisted.authMethod === 'nip46' && nip46Available) ||
          (persisted.authMethod !== 'nip46' && extensionAvailable);

        if (supportsMethod) {
          if (persisted.authMethod === 'nip46' && persisted.nip46?.uri) {
            try {
              const connected = await connectNip46(persisted.nip46);
              persisted.pubkey = connected.pubkey;
              persisted.relays = connected.relays;
              persisted.nip46 = connected;
            } catch (error) {
              console.warn('Failed to reconnect NIP-46 session:', error);
              updateAuthState({ status: 'unauthenticated', ...compatibilityPatch(), error: null });
              return;
            }
          }

          const capabilities = persisted.authMethod === 'nip46' ? getNip46Capabilities() : getNip07Capabilities();
          updateAuthState({
            status: 'authenticated',
            pubkey: persisted.pubkey,
            relays: persisted.relays,
            capabilities,
            authMethod: persisted.authMethod,
            nip46: persisted.nip46,
            lastAuthenticatedAt: persisted.lastAuthenticatedAt,
            ...compatibilityPatch(),
            error: null
          });
          try {
            await configureBackendAuth(persisted.pubkey);
          } catch (backendError) {
            console.warn('Backend auth provider initialization failed:', backendError.message);
          }
          return;
        }
      }

      updateAuthState({ status: 'unauthenticated', ...compatibilityPatch(), error: null });
      if (!extensionAvailable && !nip46Available) {
        toast.warning('No Nostr signer detected. Install a NIP-07 extension or NIP-46 provider to sign in.');
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
  const { available: extensionAvailable } = detectNip07();
  const { available: nip46Available } = detectNip46();
  updateAuthState({
    extensionAvailable,
    nip46Available,
    capabilities:
      authState.status === 'authenticated' && authState.authMethod === 'nip46'
        ? getNip46Capabilities()
        : extensionAvailable
          ? getNip07Capabilities()
          : {}
  });
  return extensionAvailable || nip46Available;
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
      const pubkey = await getNip07PublicKey();
      const [relays, capabilities] = await Promise.all([
        getNip07Relays().catch(() => ({})),
        Promise.resolve(getNip07Capabilities())
      ]);
      updateAuthState({
        status: 'authenticated',
        extensionAvailable: true,
        authMethod: 'nip07',
        pubkey,
        relays,
        capabilities,
        nip46: null,
        lastAuthenticatedAt: new Date().toISOString(),
        error: null
      });
      persistSession({ pubkey, relays, authMethod: 'nip07', nip46: null });
      try {
        await authenticateBackendInternal(pubkey);
      } catch (backendError) {
        console.warn('Backend authentication failed:', backendError.message);
      }
      toast.success('Signed in successfully');
    } catch (error) {
      console.error('Login failed:', error);
      const persisted = loadPersistedSession();
      if (persisted) {
        updateAuthState({
          status: 'authenticated',
          pubkey: persisted.pubkey,
          relays: persisted.relays,
          authMethod: persisted.authMethod || 'nip07',
          capabilities: persisted.authMethod === 'nip46' ? getNip46Capabilities() : getNip07Capabilities(),
          nip46: persisted.nip46 || null,
          lastAuthenticatedAt: persisted.lastAuthenticatedAt,
          error: null
        });
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

export async function loginWithNostrConnect(uri) {
  if (loginInProgress) return loginInProgress;
  loginInProgress = (async () => {
    try {
      updateAuthState({ status: 'authenticating', error: null });
      const session = parseNostrConnectUri(uri);
      const connected = await connectNip46(session);
      const capabilities = getNip46Capabilities();

      updateAuthState({
        status: 'authenticated',
        authMethod: 'nip46',
        nip46Available: true,
        pubkey: connected.pubkey,
        relays: connected.relays,
        capabilities,
        nip46: connected,
        lastAuthenticatedAt: new Date().toISOString(),
        error: null
      });

      persistSession({
        pubkey: connected.pubkey,
        relays: connected.relays,
        authMethod: 'nip46',
        nip46: connected
      });

      try {
        await authenticateBackendInternal(connected.pubkey);
      } catch (backendError) {
        console.warn('Backend authentication failed:', backendError.message);
      }
      toast.success('Connected signer successfully');
    } catch (error) {
      console.error('Nostr Connect login failed:', error);
      const persisted = loadPersistedSession();
      if (persisted) {
        updateAuthState({
          status: 'authenticated',
          pubkey: persisted.pubkey,
          relays: persisted.relays,
          authMethod: persisted.authMethod || 'nip07',
          capabilities: persisted.authMethod === 'nip46' ? getNip46Capabilities() : getNip07Capabilities(),
          nip46: persisted.nip46 || null,
          lastAuthenticatedAt: persisted.lastAuthenticatedAt,
          error: null
        });
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

export async function canUseNostrConnectUri(uri) {
  const parsed = parseNostrConnectUri(uri);
  return Boolean(parsed?.uri);
}

export async function connectNostrConnectSessionFromStorage() {
  const persisted = loadPersistedSession();
  if (!persisted || persisted.authMethod !== 'nip46' || !persisted.nip46?.uri) return null;
  const connected = await connectNip46(persisted.nip46);
  persistSession({
    pubkey: connected.pubkey,
    relays: connected.relays,
    authMethod: 'nip46',
    nip46: connected
  });
  return connected;
}

export function logout() {
  clearPersistedSession();
  void disconnectNip46().catch((err) => console.warn('Failed to disconnect NIP-46 session:', err));
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
    nip46Available: authState.nip46Available,
    capabilities: authState.extensionAvailable ? getNip07Capabilities() : authState.nip46Available ? getNip46Capabilities() : {},
    ...compatibilityPatch()
  });
}

export async function signWithAuth(event) {
  if (authState.status !== 'authenticated') throw new Error('Not authenticated - please login first');
  try {
    const signer = resolveActiveSigner();
    return await signer.signEvent(event);
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
  const signer = resolveActiveSigner();
  const signedEvent = await signer.signEvent(unsignedEvent);
  return `Nostr ${base64Encode(JSON.stringify(signedEvent))}`;
}

export async function authenticateBackend() {
  if (!browser) throw new Error('authenticateBackend() can only be called in the browser');
  if (authState.status !== 'authenticated' || !authState.pubkey) {
    await login();
    if (authState.status !== 'authenticated' || !authState.pubkey) {
      throw new Error('Nostr authentication required before backend auth');
    }
  }
  try {
    return await configureBackendAuth(authState.pubkey, { requireBackend: true });
  } catch (error) {
    console.error('Backend authentication failed:', error);
    updateAuthState(compatibilityPatch({
      restNip98Advertised: false,
      restNip98Ready: false,
      restNip98LastError: error.message
    }));
    throw error;
  }
}
