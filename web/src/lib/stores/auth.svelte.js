// Auth/session store for NIP-07 extension + NIP-46 Nostr Connect authentication
// UI identity/session state only - does NOT manage bahia_token as first-party auth state

import { browser } from '$app/environment';
import { toast, removeToast } from '$lib/components/toast.js';
import {
  waitForNip07,
  getPublicKey as getNip07PublicKey,
  getRelays as getNip07Relays,
  getCapabilities as getNip07Capabilities,
  getNip07Signer,
  detectNip07,
  watchNip07Availability
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
  profile: null,
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
    lastAuthenticatedAt: authState.lastAuthenticatedAt,
    profile: authState.profile
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
      lastAuthenticatedAt: session.lastAuthenticatedAt,
      profile: session.profile || null
    };
  } catch (error) {
    console.warn('Failed to load persisted auth session:', error);
    return null;
  }
}

function persistSession({ pubkey, relays, authMethod = 'nip07', nip46 = null, profile = null, lastAuthenticatedAt = new Date().toISOString() }) {
  if (!browser) return;
  try {
    localStorage.setItem(
      SESSION_KEY,
      JSON.stringify({ pubkey, relays, authMethod, nip46, profile, lastAuthenticatedAt })
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

function normalizeProfileMetadata(metadata = {}) {
  if (!metadata || typeof metadata !== 'object') return null;

  const displayName = String(metadata.display_name || metadata.displayName || '').trim();
  const name = String(metadata.name || '').trim();
  const nip05 = String(metadata.nip05 || '').trim();
  const picture = String(metadata.picture || '').trim();
  const about = String(metadata.about || '').trim();

  if (!displayName && !name && !nip05 && !picture && !about) return null;

  return {
    displayName,
    name,
    nip05,
    picture,
    about
  };
}

function normalizeRelayUrls(relays = {}) {
  if (Array.isArray(relays)) {
    return relays.filter((relay) => typeof relay === 'string' && /^wss?:\/\//i.test(relay));
  }

  return Object.entries(relays || {})
    .filter(([url, config]) => /^wss?:\/\//i.test(url) && config?.read !== false)
    .map(([url]) => url);
}

function collectProfileRelayCandidates(userRelays = {}) {
  const candidates = [];
  candidates.push(...normalizeRelayUrls(userRelays));

  // Do not use the legacy Sharegap relay fallback here. Profile lookup should stay on
  // user relays plus generic public relays so a deprecated relay endpoint cannot poison
  // bootstrap or emit distracting console failures.
  candidates.push('wss://relay.primal.net', 'wss://nos.lol');

  return Array.from(new Set(candidates.filter((relay) => typeof relay === 'string' && /^wss?:\/\//i.test(relay)))).slice(0, 8);
}

async function queryKind0OverRelay(relayUrl, pubkey, timeoutMs = 5000) {
  return new Promise((resolve) => {
    let settled = false;
    let latest = null;
    let socket;

    const finish = (event = null) => {
      if (settled) return;
      settled = true;
      try {
        if (socket && socket.readyState === WebSocket.OPEN) {
          socket.send(JSON.stringify(['CLOSE', `profile-${pubkey}`]));
        }
      } catch {}
      try {
        socket?.close();
      } catch {}
      resolve(event);
    };

    const timer = setTimeout(() => finish(latest), timeoutMs);

    try {
      socket = new WebSocket(relayUrl);
    } catch {
      clearTimeout(timer);
      finish(null);
      return;
    }

    socket.onopen = () => {
      socket.send(JSON.stringify(['REQ', `profile-${pubkey}`, { kinds: [0], authors: [pubkey], limit: 5 }]));
    };

    socket.onmessage = (message) => {
      try {
        const payload = JSON.parse(message.data);
        if (payload[0] === 'EVENT' && payload[2]?.pubkey === pubkey && typeof payload[2]?.content === 'string') {
          if (!latest || Number(payload[2].created_at || 0) > Number(latest.created_at || 0)) {
            latest = payload[2];
          }
        }
        if (payload[0] === 'EOSE' || payload[0] === 'CLOSED') {
          clearTimeout(timer);
          finish(latest);
        }
      } catch {
        clearTimeout(timer);
        finish(latest);
      }
    };

    socket.onerror = () => {
      clearTimeout(timer);
      finish(latest);
    };

    socket.onclose = () => {
      clearTimeout(timer);
      finish(latest);
    };
  });
}

async function fetchProfile(pubkey, userRelays = {}) {
  if (!isValidHexPubkey(pubkey)) return null;

  try {
    // Avoid querying the shared Bahia control-plane relay for kind-0 metadata.
    // That relay is scoped for Bahia control-plane/read-model traffic and may
    // close profile queries as blocked. Use user/public relays instead.
    const relayCandidates = collectProfileRelayCandidates(userRelays);
    if (relayCandidates.length === 0) return null;

    const results = await Promise.allSettled(relayCandidates.map((relay) => queryKind0OverRelay(relay, pubkey)));
    const latest = results
      .filter((result) => result.status === 'fulfilled' && result.value?.content)
      .map((result) => result.value)
      .sort((a, b) => Number(b?.created_at || 0) - Number(a?.created_at || 0))[0];

    if (!latest?.content) return null;
    return normalizeProfileMetadata(JSON.parse(latest.content));
  } catch (error) {
    console.warn('Failed to load Nostr kind-0 profile:', error);
    return null;
  }
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
let missingSignerToastId = null;
let stopWatchingNip07Availability = null;
let signerLifecycleWatcherInstalled = false;

function dismissMissingSignerToast() {
  if (missingSignerToastId == null) return;
  removeToast(missingSignerToastId);
  missingSignerToastId = null;
}

function showMissingSignerToast() {
  if (missingSignerToastId != null) return;
  missingSignerToastId = toast.warning(
    'No Nostr signer detected. Install a NIP-07 extension or NIP-46 provider to sign in.'
  );
}

function resolveAvailabilityCapabilities(extensionAvailable, nip46Available) {
  if (authState.status === 'authenticated') {
    if (authState.authMethod === 'nip46') {
      return nip46Available ? getNip46Capabilities() : {};
    }
    if (authState.authMethod === 'nip07') {
      return extensionAvailable ? getNip07Capabilities() : {};
    }
  }

  if (extensionAvailable) return getNip07Capabilities();
  if (nip46Available) return getNip46Capabilities();
  return {};
}

function syncSignerAvailability({ extensionAvailable, nip46Available }) {
  updateAuthState({
    extensionAvailable,
    nip46Available,
    capabilities: resolveAvailabilityCapabilities(extensionAvailable, nip46Available)
  });

  if (extensionAvailable || nip46Available) {
    dismissMissingSignerToast();
  }
}

function ensureSignerAvailabilityWatcher() {
  if (!browser) return;

  if (!stopWatchingNip07Availability) {
    stopWatchingNip07Availability = watchNip07Availability(({ available: extensionAvailable }) => {
      const { available: nip46Available } = detectNip46();
      syncSignerAvailability({ extensionAvailable, nip46Available });
    });
  }

  if (!signerLifecycleWatcherInstalled) {
    signerLifecycleWatcherInstalled = true;
    const refreshFromRuntime = () => {
      const { available: extensionAvailable } = detectNip07();
      const { available: nip46Available } = detectNip46();
      syncSignerAvailability({ extensionAvailable, nip46Available });
    };

    window.addEventListener?.('focus', refreshFromRuntime);
    window.addEventListener?.('pageshow', refreshFromRuntime);
    document?.addEventListener?.('visibilitychange', refreshFromRuntime);
  }
}

export async function initializeAuth() {
  if (initializeInProgress) return initializeInProgress;
  initializeInProgress = (async () => {
    updateAuthState({ status: 'checking' });
    ensureSignerAvailabilityWatcher();
    try {
      const [{ available: extensionAvailable }, { available: nip46Available }] = await Promise.all([
        waitForNip07({ timeoutMs: 1500 }),
        Promise.resolve(detectNip46())
      ]);
      syncSignerAvailability({ extensionAvailable, nip46Available });
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
            profile: persisted.profile || null,
            ...compatibilityPatch(),
            error: null
          });
          try {
            await configureBackendAuth(persisted.pubkey);
          } catch (backendError) {
            console.warn('Backend auth provider initialization failed:', backendError.message);
          }
          const profile = await fetchProfile(persisted.pubkey, persisted.relays);
          if (profile) {
            updateAuthState({ profile });
            persistSession({
              pubkey: persisted.pubkey,
              relays: persisted.relays,
              authMethod: persisted.authMethod,
              nip46: persisted.nip46,
              profile,
              lastAuthenticatedAt: persisted.lastAuthenticatedAt
            });
          }
          return;
        }
      }

      updateAuthState({ status: 'unauthenticated', ...compatibilityPatch(), error: null });
      if (!extensionAvailable && !nip46Available) {
        showMissingSignerToast();
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
  syncSignerAvailability({ extensionAvailable, nip46Available });
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
        profile: null,
        error: null
      });
      dismissMissingSignerToast();
      let profile = null;
      try {
        await authenticateBackendInternal(pubkey);
      } catch (backendError) {
        console.warn('Backend authentication failed:', backendError.message);
      }
      profile = await fetchProfile(pubkey, relays);
      if (profile) {
        updateAuthState({ profile });
      }
      persistSession({ pubkey, relays, authMethod: 'nip07', nip46: null, profile });
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
          profile: persisted.profile || null,
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
        profile: null,
        error: null
      });
      dismissMissingSignerToast();

      let profile = null;
      try {
        await authenticateBackendInternal(connected.pubkey);
      } catch (backendError) {
        console.warn('Backend authentication failed:', backendError.message);
      }
      profile = await fetchProfile(connected.pubkey, connected.relays);
      if (profile) {
        updateAuthState({ profile });
      }

      persistSession({
        pubkey: connected.pubkey,
        relays: connected.relays,
        authMethod: 'nip46',
        nip46: connected,
        profile
      });
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
          profile: persisted.profile || null,
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

export async function encryptWithAuth(recipientPubkey, plaintext) {
  if (authState.status !== 'authenticated') throw new Error('Not authenticated - please login first');
  try {
    const signer = resolveActiveSigner();
    if (typeof signer.encryptNip44 !== 'function') {
      throw new Error('Active signer does not expose NIP-44 encryption');
    }
    return await signer.encryptNip44(recipientPubkey, plaintext);
  } catch (error) {
    console.error('Failed to encrypt event content:', error);
    throw new Error(`Event encryption failed: ${error.message}`);
  }
}

export async function decryptWithAuth(senderPubkey, ciphertext) {
  if (authState.status !== 'authenticated') throw new Error('Not authenticated - please login first');
  try {
    const signer = resolveActiveSigner();
    if (typeof signer.decryptNip44 !== 'function') {
      throw new Error('Active signer does not expose NIP-44 decryption');
    }
    return await signer.decryptNip44(senderPubkey, ciphertext);
  } catch (error) {
    console.error('Failed to decrypt event content:', error);
    throw new Error(`Event decryption failed: ${error.message}`);
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
