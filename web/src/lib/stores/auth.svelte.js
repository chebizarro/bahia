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
import { KINDS } from '$lib/nostr/kinds.js';
import { PoolBackedClient } from '$lib/nostr/pool-client.js';
import { normalizeRelayUrl, uniqueRelays } from '$lib/nostr/pool-utils.js';
import { supportsDirectNip98Auth } from '$lib/auth/capabilities.js';
import { currentSystemInfo, loadSystemInfo } from './system.svelte.js';

const SESSION_KEY = 'bahia_auth_session';
const AUTH_BOOTSTRAP_RELAYS = ['wss://nos.lol', 'wss://relay.primal.net'];
const AUTH_QUERY_TIMEOUT_MS = 5000;
const LEGACY_BAHIA_RELAY = normalizeRelayUrl('wss://bahia.sharegap.net/relay');

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

let encryptedSignerProbe = {
  authMethod: null,
  recipientPubkey: null,
  promise: null,
  ready: false,
  error: null
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

function transitionToAuthError(error) {
  const extensionAvailable = authState.extensionAvailable;
  const nip46Available = authState.nip46Available;
  Object.assign(authState, {
    ...initialState,
    status: 'error',
    extensionAvailable,
    nip46Available,
    capabilities: resolveAvailabilityCapabilities(extensionAvailable, nip46Available),
    ...compatibilityPatch(),
    error: error?.message || String(error)
  });
  resetEncryptedSignerProbe();
}

function resetEncryptedSignerProbe() {
  encryptedSignerProbe = {
    authMethod: authState.authMethod,
    recipientPubkey: null,
    promise: null,
    ready: false,
    error: null
  };
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

function nip44BridgeFailure(error) {
  const message = String(error?.message || error || '').toLowerCase();
  return message.includes('failed to encrypt with nip-44')
    || message.includes('failed to decrypt with nip-44')
    || message.includes('receiving end does not exist')
    || message.includes('could not establish connection')
    || message.includes('message port closed')
    || message.includes('extension context invalidated');
}

function markEncryptedSignerUnavailable(message) {
  if (!message) return;
  const nextCapabilities = {
    ...(authState.capabilities || {}),
    nip44: false,
    nip44Blocker: message
  };
  updateAuthState({ capabilities: nextCapabilities });
  encryptedSignerProbe = {
    authMethod: authState.authMethod,
    recipientPubkey: encryptedSignerProbe.recipientPubkey,
    promise: null,
    ready: false,
    error: message
  };
}

function markEncryptedSignerAvailable() {
  const nextCapabilities = {
    ...(authState.capabilities || {}),
    nip44: true,
    nip44Blocker: null
  };
  updateAuthState({ capabilities: nextCapabilities });
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
      relays: normalizeRelayMap(session.relays || {}),
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
      JSON.stringify({ pubkey, relays: normalizeRelayMap(relays), authMethod, nip46, profile, lastAuthenticatedAt })
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
  const banner = String(metadata.banner || '').trim();
  const website = String(metadata.website || '').trim();
  const lud16 = String(metadata.lud16 || '').trim();

  if (!displayName && !name && !nip05 && !picture && !about && !banner && !website && !lud16) return null;

  return {
    displayName,
    name,
    nip05,
    picture,
    about,
    banner,
    website,
    lud16
  };
}

function normalizeRelayMap(relays = {}) {
  if (Array.isArray(relays)) {
    return Object.fromEntries(uniqueRelays(relays)
      .filter((relay) => /^wss?:\/\//i.test(relay))
      .map((relay) => [normalizeRelayUrl(relay), { read: true, write: true }]));
  }

  return Object.fromEntries(
    Object.entries(relays || {})
      .filter(([url]) => /^wss?:\/\//i.test(url))
      .map(([url, config]) => [
        normalizeRelayUrl(url),
        {
          read: config?.read !== false,
          write: config?.write !== false
        }
      ])
  );
}

function normalizeRelayUrls(relays = {}) {
  return Object.entries(normalizeRelayMap(relays))
    .filter(([, config]) => config?.read !== false)
    .map(([url]) => url);
}

function mergeRelayMaps(...relayMaps) {
  const merged = {};
  for (const candidate of relayMaps) {
    const normalized = normalizeRelayMap(candidate);
    for (const [url, config] of Object.entries(normalized)) {
      if (!merged[url]) {
        merged[url] = { read: false, write: false };
      }
      merged[url] = {
        read: merged[url].read || config.read !== false,
        write: merged[url].write || config.write !== false
      };
    }
  }
  return merged;
}

function sameRelayMap(left = {}, right = {}) {
  const leftEntries = Object.entries(normalizeRelayMap(left)).sort(([a], [b]) => a.localeCompare(b));
  const rightEntries = Object.entries(normalizeRelayMap(right)).sort(([a], [b]) => a.localeCompare(b));
  return JSON.stringify(leftEntries) === JSON.stringify(rightEntries);
}

function collectBootstrapRelayCandidates(userRelays = {}) {
  return uniqueRelays([
    ...AUTH_BOOTSTRAP_RELAYS,
    ...normalizeRelayUrls(userRelays)
  ]).filter((relay) => normalizeRelayUrl(relay) !== LEGACY_BAHIA_RELAY).slice(0, 10);
}

function cleanupAuthMetadataClient() {
  if (authMetadataUnsubscribe) authMetadataUnsubscribe();
  authMetadataUnsubscribe = null;
  if (authMetadataClient) authMetadataClient.disconnect();
  authMetadataClient = null;
}

async function queryAuthEventsOnRelays(pubkey, relays = [], kinds = [], limit = 5) {
  if (!isValidHexPubkey(pubkey) || !Array.isArray(kinds) || kinds.length === 0) return [];
  if (relays.length === 0) return [];

  cleanupAuthMetadataClient();
  authMetadataClient = new PoolBackedClient({
    relays,
    saveRelayConfig: () => {}
  });

  try {
    const summary = await authMetadataClient.connect(relays, { force: true });
    if (!summary?.connected) {
      cleanupAuthMetadataClient();
      return [];
    }

    return await new Promise((resolve, reject) => {
      const events = [];
      const seenIds = new Set();
      const timer = setTimeout(() => {
        resolve(events);
      }, AUTH_QUERY_TIMEOUT_MS);

      authMetadataUnsubscribe = authMetadataClient.subscribe([
        { kinds, authors: [pubkey], limit }
      ], {
        onEvent: (event) => {
          if (event?.id && seenIds.has(event.id)) return;
          if (event?.id) seenIds.add(event.id);
          events.push(event);
        },
        onEose: () => {
          clearTimeout(timer);
          resolve(events);
        },
        onClosed: () => {
          clearTimeout(timer);
          resolve(events);
        }
      });
    });
  } catch (error) {
    console.warn('Failed auth metadata relay query:', error);
    cleanupAuthMetadataClient();
    return [];
  }
}

async function queryAuthEvents(pubkey, userRelays = {}, kinds = [], limit = 5) {
  const relayCandidates = collectBootstrapRelayCandidates(userRelays);
  const bootstrapRelays = relayCandidates.filter((relay) => AUTH_BOOTSTRAP_RELAYS.includes(relay));
  const fallbackRelays = relayCandidates.filter((relay) => !AUTH_BOOTSTRAP_RELAYS.includes(relay));

  const bootstrapEvents = await queryAuthEventsOnRelays(pubkey, bootstrapRelays, kinds, limit);
  if (bootstrapEvents.length > 0 || fallbackRelays.length === 0) {
    return bootstrapEvents;
  }

  return queryAuthEventsOnRelays(pubkey, fallbackRelays, kinds, limit);
}

function latestAuthEvent(events, kind, pubkey) {
  return (events || [])
    .filter((event) => event?.kind === kind && event?.pubkey === pubkey)
    .sort((a, b) => Number(b?.created_at || 0) - Number(a?.created_at || 0))[0] || null;
}

function parseNip65RelayList(event) {
  const relays = {};
  for (const tag of Array.isArray(event?.tags) ? event.tags : []) {
    if (tag[0] !== 'r' || typeof tag[1] !== 'string' || !/^wss?:\/\//i.test(tag[1])) continue;
    const marker = String(tag[2] || '').toLowerCase();
    relays[normalizeRelayUrl(tag[1])] = {
      read: marker !== 'write',
      write: marker !== 'read'
    };
  }
  return relays;
}

async function fetchRelayList(pubkey, userRelays = {}) {
  const relayListKind = KINDS.NIP65_RELAY_LIST || 10002;
  const events = await queryAuthEvents(pubkey, userRelays, [relayListKind], 5);
  const latest = latestAuthEvent(events, relayListKind, pubkey);
  if (!latest) return {};
  return parseNip65RelayList(latest);
}

async function fetchProfile(pubkey, userRelays = {}) {
  if (!isValidHexPubkey(pubkey)) return null;

  try {
    const events = await queryAuthEvents(pubkey, userRelays, [0], 5);
    const latest = latestAuthEvent(events, 0, pubkey);
    if (!latest?.content) return null;
    return normalizeProfileMetadata(JSON.parse(latest.content));
  } catch (error) {
    console.warn('Failed to load Nostr kind-0 profile:', error);
    return null;
  }
}

async function hydrateAuthMetadata({ pubkey, relays = {}, authMethod = 'nip07', nip46 = null, lastAuthenticatedAt = new Date().toISOString() }) {
  const mergedRelays = mergeRelayMaps(relays, await fetchRelayList(pubkey, relays));
  if (!sameRelayMap(relays, mergedRelays)) {
    updateAuthState({ relays: mergedRelays });
  }

  const profile = await fetchProfile(pubkey, mergedRelays);
  if (profile) {
    updateAuthState({ profile });
  }

  persistSession({
    pubkey,
    relays: mergedRelays,
    authMethod,
    nip46,
    profile,
    lastAuthenticatedAt
  });

  return { relays: mergedRelays, profile };
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
    updateAuthState({
      ...compatibilityPatch({ restNip98Advertised: true, restNip98Ready: false }),
      error: null
    });
    try {
      await api.fetch('/orgs', { method: 'GET', retries: 0 });
    } catch (error) {
      updateAuthState(compatibilityPatch({
        restNip98Advertised: true,
        restNip98Ready: false,
        restNip98LastError: error?.message || 'Backend rejected the NIP-98 authentication probe'
      }));
      if (requireBackend) throw error;
      return null;
    }
    updateAuthState({
      ...compatibilityPatch({ restNip98Advertised: true, restNip98Ready: true }),
      error: null
    });
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
let authMetadataClient = null;
let authMetadataUnsubscribe = null;

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
              if (connected.pubkey.toLowerCase() !== persisted.pubkey.toLowerCase()) {
                throw new Error('Persisted NIP-46 identity does not match the connected signer');
              }
              persisted.pubkey = connected.pubkey;
              persisted.relays = connected.relays;
              persisted.nip46 = connected;
            } catch (error) {
              console.warn('Failed to reconnect NIP-46 session:', error);
              clearPersistedSession();
              updateAuthState({ status: 'unauthenticated', ...compatibilityPatch(), error: null });
              return;
            }
          } else {
            try {
              const signerPubkey = await getNip07PublicKey();
              if (signerPubkey.toLowerCase() !== persisted.pubkey.toLowerCase()) {
                throw new Error('Persisted NIP-07 identity does not match the connected signer');
              }
            } catch (error) {
              console.warn('Failed to verify persisted NIP-07 session:', error);
              clearPersistedSession();
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
          await hydrateAuthMetadata({
            pubkey: persisted.pubkey,
            relays: persisted.relays,
            authMethod: persisted.authMethod,
            nip46: persisted.nip46,
            lastAuthenticatedAt: persisted.lastAuthenticatedAt
          });
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
      try {
        await authenticateBackendInternal(pubkey);
      } catch (backendError) {
        console.warn('Backend authentication failed:', backendError.message);
      }
      await hydrateAuthMetadata({
        pubkey,
        relays,
        authMethod: 'nip07',
        nip46: null,
        lastAuthenticatedAt: authState.lastAuthenticatedAt
      });
      toast.success('Signed in successfully');
    } catch (error) {
      console.error('Login failed:', error);
      clearPersistedSession();
      transitionToAuthError(error);
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

      try {
        await authenticateBackendInternal(connected.pubkey);
      } catch (backendError) {
        console.warn('Backend authentication failed:', backendError.message);
      }
      await hydrateAuthMetadata({
        pubkey: connected.pubkey,
        relays: connected.relays,
        authMethod: 'nip46',
        nip46: connected,
        lastAuthenticatedAt: authState.lastAuthenticatedAt
      });
      toast.success('Connected signer successfully');
    } catch (error) {
      console.error('Nostr Connect login failed:', error);
      clearPersistedSession();
      await disconnectNip46().catch((disconnectError) => {
        console.warn('Failed to disconnect rejected NIP-46 session:', disconnectError);
      });
      transitionToAuthError(error);
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
  cleanupAuthMetadataClient();
  import('$lib/nostr/encrypted-controlplane.js').then(({ disconnectEncryptedControlplane }) => {
    disconnectEncryptedControlplane();
  }).catch(() => {});
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
  resetEncryptedSignerProbe();
}

export function updateAuthProfile(profile) {
  if (authState.status !== 'authenticated' || !authState.pubkey) {
    throw new Error('Not authenticated - please login first');
  }
  const normalizedProfile = normalizeProfileMetadata(profile);
  updateAuthState({ profile: normalizedProfile });
  persistSession({
    pubkey: authState.pubkey,
    relays: authState.relays,
    authMethod: authState.authMethod || 'nip07',
    nip46: authState.nip46 || null,
    profile: normalizedProfile,
    lastAuthenticatedAt: authState.lastAuthenticatedAt || new Date().toISOString()
  });
  return normalizedProfile;
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
    if (nip44BridgeFailure(error)) {
      markEncryptedSignerUnavailable(error?.message || String(error));
    }
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
    if (nip44BridgeFailure(error)) {
      markEncryptedSignerUnavailable(error?.message || String(error));
    }
    console.error('Failed to decrypt event content:', error);
    throw new Error(`Event decryption failed: ${error.message}`);
  }
}

export async function ensureEncryptedSignerReady(recipientPubkey) {
  if (authState.status !== 'authenticated') {
    throw new Error('Not authenticated - please login first');
  }
  if (!recipientPubkey) {
    throw new Error('Recipient pubkey is required for encrypted signer readiness checks');
  }

  const capabilityBlocker = authState.capabilities?.nip44Blocker;
  if (authState.capabilities?.nip44 === false && capabilityBlocker) {
    throw new Error(capabilityBlocker);
  }

  const authMethod = authState.authMethod || 'nip07';
  if (
    encryptedSignerProbe.ready &&
    encryptedSignerProbe.authMethod === authMethod &&
    encryptedSignerProbe.recipientPubkey === recipientPubkey
  ) {
    return true;
  }

  if (
    encryptedSignerProbe.promise &&
    encryptedSignerProbe.authMethod === authMethod &&
    encryptedSignerProbe.recipientPubkey === recipientPubkey
  ) {
    return encryptedSignerProbe.promise;
  }

  const probePromise = (async () => {
    await encryptWithAuth(recipientPubkey, JSON.stringify({
      version: 'bahia-encrypted-v1',
      probe: true,
      requester_pubkey: authState.pubkey,
      created_at: Math.floor(Date.now() / 1000)
    }));
    markEncryptedSignerAvailable();
    encryptedSignerProbe = {
      authMethod,
      recipientPubkey,
      promise: null,
      ready: true,
      error: null
    };
    return true;
  })().catch((error) => {
    encryptedSignerProbe = {
      authMethod,
      recipientPubkey,
      promise: null,
      ready: false,
      error: error?.message || String(error)
    };
    throw error;
  });

  encryptedSignerProbe = {
    authMethod,
    recipientPubkey,
    promise: probePromise,
    ready: false,
    error: null
  };

  return probePromise;
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
    throw error;
  }
}
