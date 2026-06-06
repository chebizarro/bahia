// NIP-07 browser extension detection and wrapper
// Provides SSR-safe access to window.nostr

/**
 * Detect if NIP-07 extension is available
 * @returns {Object} Detection result with available, provider, and reason
 */
export function detectNip07() {
  // SSR safety check
  if (typeof window === 'undefined') {
    return {
      available: false,
      provider: null,
      reason: 'not_browser'
    };
  }

  // Check for window.nostr
  if (!window.nostr) {
    return {
      available: false,
      provider: null,
      reason: 'missing_window_nostr'
    };
  }

  return {
    available: true,
    provider: window.nostr,
    reason: null
  };
}

const nip07AvailabilityWatchers = new Set();
let nip07ObserverInstalled = false;
let lastKnownNip07Availability = null;
let lastKnownNip07Provider = null;
let nip07CryptoQueue = Promise.resolve();
let nip07AvailabilityPoller = null;

function getTimerHost() {
  if (typeof window !== 'undefined' && typeof window.setInterval === 'function') {
    return window;
  }
  if (typeof globalThis.setInterval === 'function') {
    return globalThis;
  }
  return null;
}

function clearNip07AvailabilityPoller() {
  if (!nip07AvailabilityPoller) return;
  const timerHost = getTimerHost();
  if (timerHost && typeof timerHost.clearInterval === 'function') {
    timerHost.clearInterval(nip07AvailabilityPoller);
  } else if (typeof globalThis.clearInterval === 'function') {
    globalThis.clearInterval(nip07AvailabilityPoller);
  }
  nip07AvailabilityPoller = null;
}

function notifyNip07AvailabilityWatchers({ force = false } = {}) {
  const result = detectNip07();
  const providerChanged = result.provider !== lastKnownNip07Provider;
  if (!force && result.available === lastKnownNip07Availability && !providerChanged) {
    return result;
  }

  lastKnownNip07Availability = result.available;
  lastKnownNip07Provider = result.provider;
  nip07AvailabilityWatchers.forEach((watcher) => watcher(result));
  return result;
}

function installNip07Observer() {
  if (typeof window === 'undefined' || nip07ObserverInstalled) return;
  nip07ObserverInstalled = true;
  const initial = detectNip07();
  lastKnownNip07Availability = initial.available;
  lastKnownNip07Provider = initial.provider;

  const scheduleCheck = () => {
    queueMicrotask(() => {
      notifyNip07AvailabilityWatchers();
    });
  };

  const nostrDescriptor = Object.getOwnPropertyDescriptor(window, 'nostr');
  if (!nostrDescriptor || nostrDescriptor.configurable) {
    let currentProvider = window.nostr;
    Object.defineProperty(window, 'nostr', {
      configurable: true,
      enumerable: true,
      get() {
        return currentProvider;
      },
      set(provider) {
        currentProvider = provider;
        scheduleCheck();
      }
    });
  }

  window.addEventListener?.('focus', scheduleCheck);
  window.addEventListener?.('pageshow', scheduleCheck);
  document?.addEventListener?.('visibilitychange', scheduleCheck);
}

function updateNip07Polling() {
  if (typeof window === 'undefined') return;
  const needsPolling = nip07AvailabilityWatchers.size > 0 && !detectNip07().available;
  const timerHost = getTimerHost();
  if (needsPolling && !nip07AvailabilityPoller && timerHost) {
    nip07AvailabilityPoller = timerHost.setInterval(() => {
      const result = notifyNip07AvailabilityWatchers();
      if (result.available) {
        clearNip07AvailabilityPoller();
      }
    }, 100);
    return;
  }

  if (!needsPolling) {
    clearNip07AvailabilityPoller();
  }
}

/**
 * Subscribe to NIP-07 availability changes.
 * Reacts to late provider injection and focus/visibility changes without polling.
 * @param {(result: {available: boolean, provider: any, reason: string | null}) => void} onChange
 * @param {{fireImmediately?: boolean}} options
 * @returns {() => void}
 */
export function watchNip07Availability(onChange, { fireImmediately = true } = {}) {
  if (typeof onChange !== 'function') {
    return () => {};
  }

  if (typeof window === 'undefined') {
    if (fireImmediately) {
      onChange(detectNip07());
    }
    return () => {};
  }

  installNip07Observer();
  nip07AvailabilityWatchers.add(onChange);
  updateNip07Polling();

  if (fireImmediately) {
    onChange(detectNip07());
  }

  return () => {
    nip07AvailabilityWatchers.delete(onChange);
    updateNip07Polling();
  };
}

/**
 * Wait for NIP-07 extension to become available
 * Some extensions inject after page load
 * @param {Object} options - Configuration options
 * @param {number} options.timeoutMs - Maximum wait time in milliseconds
 * @param {number} options.intervalMs - Polling interval in milliseconds
 * @returns {Promise<Object>} Detection result
 */
export function waitForNip07({ timeoutMs = 1500, intervalMs = 100 } = {}) {
  void intervalMs;

  const initial = detectNip07();
  if (initial.available || typeof window === 'undefined' || timeoutMs <= 0) {
    return Promise.resolve(initial);
  }

  return new Promise((resolve) => {
    let settled = false;

    const stopWatching = watchNip07Availability((result) => {
      if (!result.available || settled) return;
      settled = true;
      clearTimeout(timeoutId);
      stopWatching();
      resolve(result);
    }, { fireImmediately: false });

    const timeoutId = setTimeout(() => {
      if (settled) return;
      settled = true;
      stopWatching();
      resolve(detectNip07());
    }, timeoutMs);
  });
}

/**
 * Validate hex string format (64 chars, hex only)
 * @param {string} str - String to validate
 * @returns {boolean} True if valid hex pubkey
 */
function isValidHexPubkey(str) {
  return typeof str === 'string' && 
         str.length === 64 && 
         /^[0-9a-fA-F]{64}$/.test(str);
}

function requireNip44Provider() {
  const { available, reason } = detectNip07();

  if (!available) {
    throw new Error(`NIP-07 extension not available: ${reason}`);
  }

  const provider = window.nostr?.nip44;
  if (!provider || typeof provider.encrypt !== 'function' || typeof provider.decrypt !== 'function') {
    throw new Error('NIP-07 signer does not expose NIP-44 encrypt/decrypt support');
  }

  return provider;
}

function delay(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function isTransientBridgeError(error) {
  const message = String(error?.message || error || '').toLowerCase();
  return message.includes('could not establish connection')
    || message.includes('receiving end does not exist')
    || message.includes('extension context invalidated')
    || message.includes('message port closed');
}

async function runQueuedNip44Operation(operation) {
  const previous = nip07CryptoQueue.catch(() => {});
  const next = previous.then(operation);
  const queueEntry = next.catch(() => {}).finally(() => {
    if (nip07CryptoQueue === queueEntry) {
      nip07CryptoQueue = Promise.resolve();
    }
  });
  nip07CryptoQueue = queueEntry;
  return next;
}

async function withNip44ProviderRetry(action) {
  const retryDelays = [0, 150, 400];
  let lastError = null;

  for (let attempt = 0; attempt < retryDelays.length; attempt += 1) {
    if (retryDelays[attempt] > 0) {
      await delay(retryDelays[attempt]);
      await waitForNip07({ timeoutMs: retryDelays[attempt] + 150 });
    }

    const provider = requireNip44Provider();
    try {
      return await action(provider);
    } catch (error) {
      lastError = error;
      if (!isTransientBridgeError(error) || attempt === retryDelays.length - 1) {
        throw error;
      }
    }
  }

  throw lastError || new Error('Unknown NIP-44 provider failure');
}

function ensureCryptoPubkey(pubkey, role) {
  if (!isValidHexPubkey(pubkey)) {
    throw new Error(`Invalid ${role} pubkey for NIP-44 encryption`);
  }
}

/**
 * Get public key from NIP-07 extension
 * @returns {Promise<string>} Hex-encoded public key
 * @throws {Error} If extension unavailable or pubkey invalid
 */
export async function getPublicKey() {
  const { available, reason } = detectNip07();
  
  if (!available) {
    throw new Error(`NIP-07 extension not available: ${reason}`);
  }

  try {
    const pubkey = await window.nostr.getPublicKey();
    
    if (!isValidHexPubkey(pubkey)) {
      throw new Error('Invalid public key format returned by extension');
    }
    
    return pubkey;
  } catch (error) {
    throw new Error(`Failed to get public key: ${error.message}`);
  }
}

/**
 * Get relay list from NIP-07 extension
 * @returns {Promise<Object>} Relay object (url -> read/write permissions)
 */
export async function getRelays() {
  const { available, reason } = detectNip07();
  
  if (!available) {
    throw new Error(`NIP-07 extension not available: ${reason}`);
  }

  try {
    // getRelays may not be implemented by all extensions
    if (typeof window.nostr.getRelays !== 'function') {
      return {};
    }
    
    const relays = await window.nostr.getRelays();
    return relays || {};
  } catch (error) {
    // If getRelays fails, return empty object rather than throwing
    console.warn('Failed to get relays from NIP-07:', error);
    return {};
  }
}

/**
 * Sign an event using NIP-07 extension
 * @param {Object} event - Nostr event object to sign
 * @returns {Promise<Object>} Signed event with id and sig fields
 * @throws {Error} If extension unavailable or signing fails
 */
export async function signEvent(event) {
  const { available, reason } = detectNip07();
  
  if (!available) {
    throw new Error(`NIP-07 extension not available: ${reason}`);
  }

  if (!event || typeof event !== 'object') {
    throw new Error('Invalid event object');
  }

  try {
    const signedEvent = await window.nostr.signEvent(event);
    return signedEvent;
  } catch (error) {
    throw new Error(`Failed to sign event: ${error.message}`);
  }
}

/**
 * Encrypt plaintext to a recipient with NIP-44 using the active NIP-07 signer.
 * @param {string} recipientPubkey - Hex-encoded recipient pubkey
 * @param {string} plaintext - Cleartext payload
 * @returns {Promise<string>} NIP-44 ciphertext
 */
export async function encryptNip44(recipientPubkey, plaintext) {
  ensureCryptoPubkey(recipientPubkey, 'recipient');
  if (typeof plaintext !== 'string') {
    throw new Error('NIP-44 plaintext must be a string');
  }

  return runQueuedNip44Operation(async () => {
    try {
      return await withNip44ProviderRetry((provider) => provider.encrypt(recipientPubkey, plaintext));
    } catch (error) {
      throw new Error(`Failed to encrypt with NIP-44: ${error.message}`);
    }
  });
}

/**
 * Decrypt NIP-44 ciphertext from a sender using the active NIP-07 signer.
 * @param {string} senderPubkey - Hex-encoded sender pubkey
 * @param {string} ciphertext - NIP-44 ciphertext payload
 * @returns {Promise<string>} Cleartext payload
 */
export async function decryptNip44(senderPubkey, ciphertext) {
  ensureCryptoPubkey(senderPubkey, 'sender');
  if (typeof ciphertext !== 'string' || ciphertext.length === 0) {
    throw new Error('NIP-44 ciphertext must be a non-empty string');
  }

  return runQueuedNip44Operation(async () => {
    try {
      return await withNip44ProviderRetry((provider) => provider.decrypt(senderPubkey, ciphertext));
    } catch (error) {
      throw new Error(`Failed to decrypt with NIP-44: ${error.message}`);
    }
  });
}

/**
 * Get capabilities of the NIP-07 extension
 * @returns {Object} Capability flags for available methods
 */
export function getCapabilities() {
  const { available } = detectNip07();
  
  if (!available) {
    return {
      getPublicKey: false,
      signEvent: false,
      getRelays: false,
      nip04: false,
      nip44: false
    };
  }

  const nostr = window.nostr;
  
  return {
    getPublicKey: typeof nostr.getPublicKey === 'function',
    signEvent: typeof nostr.signEvent === 'function',
    getRelays: typeof nostr.getRelays === 'function',
    nip04: typeof nostr.nip04 === 'object' && 
           typeof nostr.nip04.encrypt === 'function' &&
           typeof nostr.nip04.decrypt === 'function',
    nip44: typeof nostr.nip44 === 'object' &&
           typeof nostr.nip44.encrypt === 'function' &&
           typeof nostr.nip44.decrypt === 'function'
  };
}

/**
 * Resolve a signer-shaped NIP-07 contract
 * @returns {{getPublicKey: Function, signEvent: Function, getRelays: Function}}
 */
export function getNip07Signer() {
  return {
    getPublicKey,
    signEvent,
    getRelays,
    encryptNip44,
    decryptNip44
  };
}
