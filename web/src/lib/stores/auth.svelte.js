/***/
 * Auth/session store for NIP-07 extension + NIP-46 Nostr Connect authentication.
 *
 * §6.2: A persisted, signer-verified session counts as authenticated
 * immediately. No REST probe, no discovery gate, no backendAuthenticated flag.
 *
 * Background signer verification is non-blocking. On first authenticated boot,
 * calls requestPersistentStorage for durable IndexedDB storage.
 *
 * Roles come from relay membership events in auth-roles.svelte.js, not from
 * a REST /orgs probe.
 *
 * @module lib/stores/auth
 */

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
import { normalizeRelayUrl, uniqueRelays } from '$lib/nostr/pool-utils.js';
import { setRelayAuthSigner } from '$lib/nostr/relay-auth-signer.js';
import { requestPersistentStorage } from '$lib/nostr/store-interface.js';
import { stopRoleDerivation } from './auth-roles.svelte.js';

const SESSION_KEY = 'bahia_auth_session';
const SIGNER_VERIFY_EXPIRY_MS = 24 * 60 * 60 * 1000; // 24 hours

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
  signerVerifiedAt: null,
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
  // The relay sidecar requires NIP-42 for protected topics by default; the
  // pool must be able to answer its AUTH challenge from the moment a session
  // exists (persisted or fresh), and must stop once it ends.
  setRelayAuthSigner(authState.status === 'authenticated' ? signWithAuth : null);
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
    error: error?.message || String(error)
  });
  setRelayAuthSigner(null);
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

export function resolveActiveSigner() {
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
      signerVerifiedAt: session.signerVerifiedAt || null,
      profile: session.profile || null
    };
  } catch (error) {
    console.warn('Failed to load persisted auth session:', error);
    return null;
  }
}

function persistSession({ pubkey, relays, authMethod = 'nip07', nip46 = null, profile = null, lastAuthenticatedAt = new Date().toISOString(), signerVerifiedAt = null }) {
  if (!browser) return;
  try {
    localStorage.setItem(
      SESSION_KEY,
      JSON.stringify({ pubkey, relays: normalizeRelayMap(relays), authMethod, nip46, profile, lastAuthenticatedAt, signerVerifiedAt })
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

  return { displayName, name, nip05, picture, about, banner, website, lud16 };
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
        { read: config?.read !== false, write: config?.write !== false }
      ])
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

// ---------------------------------------------------------------------------
// Signer availability watcher
// ---------------------------------------------------------------------------

let initializeInProgress = null;
let loginInProgress = null;
let missingSignerToastId = null;
let stopWatchingNip07Availability = null;
let signerLifecycleWatcherInstalled = false;
/** Track whether we've already requested persistent storage this session.*/
let persistentStorageRequested = false;

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

// ---------------------------------------------------------------------------
// Background signer verification (non-blocking)
// ---------------------------------------------------------------------------

/***/
 * Verify the signer still matches the persisted session in the background.
 * Non-blocking — does not prevent rendering.
 */
async function backgroundSignerVerify(persisted) {
  try {
    if (persisted.authMethod === 'nip46' && persisted.nip46?.uri) {
      const connected = await connectNip46(persisted.nip46);
      if (connected.pubkey.toLowerCase() !== persisted.pubkey.toLowerCase()) {
        console.warn('[auth] NIP-46 signer pubkey mismatch — clearing session');
        clearPersistedSession();
        stopRoleDerivation();
        updateAuthState({ status: 'unauthenticated', error: null });
        return;
      }
      // Update relays/nip46 session info
      updateAuthState({
        relays: connected.relays,
        nip46: connected,
        signerVerifiedAt: new Date().toISOString()
      });
      persistSession({
        pubkey: persisted.pubkey,
        relays: connected.relays,
        authMethod: 'nip46',
        nip46: connected,
        profile: authState.profile,
        lastAuthenticatedAt: authState.lastAuthenticatedAt,
        signerVerifiedAt: new Date().toISOString()
      });
    } else {
      const signerPubkey = await getNip07PublicKey();
      if (signerPubkey.toLowerCase() !== persisted.pubkey.toLowerCase()) {
        console.warn('[auth] NIP-07 signer pubkey mismatch — clearing session');
        clearPersistedSession();
        stopRoleDerivation();
        updateAuthState({ status: 'unauthenticated', error: null });
        return;
      }
      updateAuthState({ signerVerifiedAt: new Date().toISOString() });
      persistSession({
        ...persisted,
        signerVerifiedAt: new Date().toISOString()
      });
    }
  } catch (err) {
    console.warn('[auth] Background signer verification failed:', err.message);
    // Don't clear session — signer may be temporarily unavailable.
    // User stays authenticated with cached session.
  }
}

// ---------------------------------------------------------------------------
// initializeAuth — §6.2 new auth bootstrap
// ---------------------------------------------------------------------------

export async function initializeAuth() {
  if (initializeInProgress) return initializeInProgress;
  initializeInProgress = (async () => {
    // Only an undetermined session shows the transitional status. The layout
    // and AuthGuard both bootstrap auth, and the later call lands after the
    // routed page is interactive; regressing a resolved session to 'checking'
    // would make AuthGuard unmount that page and discard what the user entered.
    // A repeat bootstrap re-evaluates the session in place instead.
    if (authState.status === 'unknown') updateAuthState({ status: 'checking' });
    ensureSignerAvailabilityWatcher();

    try {
      // 1. Detect signers (non-blocking for the render path)
      const [{ available: extensionAvailable }, { available: nip46Available }] = await Promise.all([
        waitForNip07({ timeoutMs: 1500 }),
        Promise.resolve(detectNip46())
      ]);
      syncSignerAvailability({ extensionAvailable, nip46Available });

      // 2. Check persisted session
      const persisted = loadPersistedSession();
      if (browser) localStorage.removeItem('bahia_token'); // clean up legacy token

      if (persisted) {
        // §6.2 step 1: persisted session = AUTHENTICATED IMMEDIATELY
        // No REST probe, no discovery gate.
        const capabilities = persisted.authMethod === 'nip46' ? getNip46Capabilities() : getNip07Capabilities();

        updateAuthState({
          status: 'authenticated',
          pubkey: persisted.pubkey,
          relays: persisted.relays,
          capabilities,
          authMethod: persisted.authMethod,
          nip46: persisted.nip46,
          lastAuthenticatedAt: persisted.lastAuthenticatedAt,
          signerVerifiedAt: persisted.signerVerifiedAt,
          profile: persisted.profile || null,
          error: null
        });

        // Request persistent storage on first authenticated boot (§14 decision 14)
        if (!persistentStorageRequested) {
          persistentStorageRequested = true;
          requestPersistentStorage().catch(err =>
            console.warn('[auth] requestPersistentStorage failed:', err)
          );
        }

        // §6.2 step 2: Background signer verification (non-blocking)
        const needsVerification = !persisted.signerVerifiedAt ||
          (Date.now() - new Date(persisted.signerVerifiedAt).getTime()) > SIGNER_VERIFY_EXPIRY_MS;

        if (needsVerification) {
          // Fire-and-forget — does not block rendering
          backgroundSignerVerify(persisted);
        }

        // Wire NIP-98 for any remaining interim REST calls
        if (browser) localStorage.removeItem('bahia_token');

        return;
      }

      // 3. No persisted session
      updateAuthState({ status: 'unauthenticated', error: null });
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
      const now = new Date().toISOString();
      updateAuthState({
        status: 'authenticated',
        extensionAvailable: true,
        authMethod: 'nip07',
        pubkey,
        relays,
        capabilities,
        nip46: null,
        lastAuthenticatedAt: now,
        signerVerifiedAt: now,
        profile: null,
        error: null
      });
      dismissMissingSignerToast();

      persistSession({
        pubkey,
        relays,
        authMethod: 'nip07',
        nip46: null,
        lastAuthenticatedAt: now,
        signerVerifiedAt: now
      });

      // Request persistent storage on first authenticated boot
      if (!persistentStorageRequested) {
        persistentStorageRequested = true;
        requestPersistentStorage().catch(err =>
          console.warn('[auth] requestPersistentStorage failed:', err)
        );
      }

      // Wire NIP-98 for interim REST calls
      if (browser) localStorage.removeItem('bahia_token');

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
      const now = new Date().toISOString();

      updateAuthState({
        status: 'authenticated',
        authMethod: 'nip46',
        nip46Available: true,
        pubkey: connected.pubkey,
        relays: connected.relays,
        capabilities,
        nip46: connected,
        lastAuthenticatedAt: now,
        signerVerifiedAt: now,
        profile: null,
        error: null
      });
      dismissMissingSignerToast();

      persistSession({
        pubkey: connected.pubkey,
        relays: connected.relays,
        authMethod: 'nip46',
        nip46: connected,
        lastAuthenticatedAt: now,
        signerVerifiedAt: now
      });

      if (!persistentStorageRequested) {
        persistentStorageRequested = true;
        requestPersistentStorage().catch(err =>
          console.warn('[auth] requestPersistentStorage failed:', err)
        );
      }

      if (browser) localStorage.removeItem('bahia_token');

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
  stopRoleDerivation();
  import('$lib/nostr/encrypted-controlplane.js').then(({ disconnectEncryptedControlplane }) => {
    disconnectEncryptedControlplane();
  }).catch(() => {});
  clearPersistedSession();
  void disconnectNip46().catch((err) => console.warn('Failed to disconnect NIP-46 session:', err));
  if (browser) localStorage.removeItem('bahia_token');
  Object.assign(authState, {
    ...initialState,
    status: 'unauthenticated',
    extensionAvailable: authState.extensionAvailable,
    nip46Available: authState.nip46Available,
    capabilities: authState.extensionAvailable ? getNip07Capabilities() : authState.nip46Available ? getNip46Capabilities() : {}
  });
  setRelayAuthSigner(null);
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
    lastAuthenticatedAt: authState.lastAuthenticatedAt || new Date().toISOString(),
    signerVerifiedAt: authState.signerVerifiedAt
  });
  return normalizedProfile;
}

// ---------------------------------------------------------------------------
// Signing and encryption (unchanged API surface)
// ---------------------------------------------------------------------------

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

/***/
 * For interim REST calls that still need backend auth.
 * This just wires NIP-98 signing — no /orgs probe.
 */
export async function authenticateBackend() {
  if (!browser) throw new Error('authenticateBackend() can only be called in the browser');
  if (authState.status !== 'authenticated' || !authState.pubkey) {
    await login();
    if (authState.status !== 'authenticated' || !authState.pubkey) {
      throw new Error('Nostr authentication required before backend auth');
    }
  }
  if (browser) localStorage.removeItem('bahia_token');
  return { method: 'nip98', pubkey: authState.pubkey };
}
