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

/**
 * Wait for NIP-07 extension to become available
 * Some extensions inject after page load
 * @param {Object} options - Configuration options
 * @param {number} options.timeoutMs - Maximum wait time in milliseconds
 * @param {number} options.intervalMs - Polling interval in milliseconds
 * @returns {Promise<Object>} Detection result
 */
export function waitForNip07({ timeoutMs = 1500, intervalMs = 100 } = {}) {
  return new Promise((resolve) => {
    const startTime = Date.now();

    const check = () => {
      const result = detectNip07();
      
      if (result.available) {
        resolve(result);
        return;
      }

      // Check if timeout exceeded
      if (Date.now() - startTime >= timeoutMs) {
        resolve(result);
        return;
      }

      // Continue polling
      setTimeout(check, intervalMs);
    };

    check();
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
    getRelays
  };
}
