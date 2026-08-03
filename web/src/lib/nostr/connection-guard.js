/**
 * Shared relay connection guard for Nostr queries.
 * 
 * Ensures relay discovery and connection completes before queries run.
 * Safe to call multiple times - subsequent calls resolve immediately if already connected.
 */

// Browser detection that works in both SvelteKit and test environments
const isBrowser = typeof window !== 'undefined' && typeof document !== 'undefined';

// Lazy imports avoid pulling browser-only stores into non-browser tests.
let _discoveryModule = null;
let _nostrModule = null;

async function getDiscoveryModule() {
  if (!_discoveryModule) {
    _discoveryModule = await import('$lib/stores/discovery.svelte.js');
  }
  return _discoveryModule;
}

async function getNostrModule() {
  if (!_nostrModule) {
    _nostrModule = await import('$lib/nostr/client.js');
  }
  return _nostrModule;
}

function normalizeRelayUrl(url) {
  if (!url || typeof url !== 'string') return '';
  if (url.startsWith('ws://') || url.startsWith('wss://')) return url;
  if (url.startsWith('https://')) return `wss://${url.slice('https://'.length)}`;
  if (url.startsWith('http://')) return `ws://${url.slice('http://'.length)}`;
  return url;
}

/**
 * Ensures deployment-configured Nostr relays are connected before proceeding.
 * 
 * This guards against race conditions where queries start before relay
 * bootstrap completes. The connection flow:
 * Discovery events are ordinary asynchronous metadata and are deliberately
 * absent from this connection path.
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
    const discoveryModule = await getDiscoveryModule();
    const nostrModule = await getNostrModule();
    const seed = discoveryModule.getBootstrapSeed();
    const relays = Array.from(new Set((seed?.relay_urls || []).map(normalizeRelayUrl).filter(Boolean)));

    if (relays.length === 0) {
      throw new Error('No browser relays configured by the deployment bootstrap');
    }

    const { nostr } = nostrModule;
    nostr.setRelays(relays, false);
    const summary = await nostr.connect(relays, { force: false });
    if (!summary?.connected) {
      throw new Error('Unable to connect to any configured browser relay');
    }
  } catch (err) {
    // Log warning but continue - let queries fail with clearer errors
    if (!silent) {
      console.warn('[nostr] Relay connection warning:', err?.message || err);
    }
  }
}
