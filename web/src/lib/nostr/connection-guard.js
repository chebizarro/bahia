/**
 * Shared relay connection guard for Nostr queries.
 * 
 * Ensures relay discovery and connection completes before queries run.
 * Safe to call multiple times - subsequent calls resolve immediately if already connected.
 */

// Browser detection that works in both SvelteKit and test environments
const isBrowser = typeof window !== 'undefined' && typeof document !== 'undefined';

// Lazy import to avoid issues in test environments
let _eagerRelayConnect = null;
async function getEagerRelayConnect() {
  if (!_eagerRelayConnect) {
    const module = await import('$lib/stores/system.svelte.js');
    _eagerRelayConnect = module.eagerRelayConnect;
  }
  return _eagerRelayConnect;
}

/**
 * Ensures Nostr relays are discovered and connected before proceeding.
 * 
 * This guards against race conditions where queries start before relay
 * bootstrap completes. The connection flow:
 * 1. Discovers bootstrap relays from config/injection
 * 2. Fetches system discovery to find browser relays
 * 3. Connects to browser relays
 * 
 * Safe to call multiple times - idempotent after first connection.
 * 
 * @param {Object} [options]
 * @param {boolean} [options.silent=false] - Suppress warning logs on failure
 * @returns {Promise<void>}
 */
export async function ensureRelayConnection({ silent = false } = {}) {
  if (!isBrowser) return;
  
  try {
    const eagerRelayConnect = await getEagerRelayConnect();
    await eagerRelayConnect();
  } catch (err) {
    // Log warning but continue - let queries fail with clearer errors
    if (!silent) {
      console.warn('[nostr] Relay connection warning:', err?.message || err);
    }
  }
}
