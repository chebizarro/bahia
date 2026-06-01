/**
 * Shared relay connection guard for Nostr queries.
 * 
 * Ensures relay discovery and connection completes before queries run.
 * Safe to call multiple times - subsequent calls resolve immediately if already connected.
 */

// Browser detection that works in both SvelteKit and test environments
const isBrowser = typeof window !== 'undefined' && typeof document !== 'undefined';

// Lazy imports avoid pulling browser-only stores into non-browser tests.
let _systemModule = null;
let _nostrModule = null;

async function getSystemModule() {
  if (!_systemModule) {
    _systemModule = await import('$lib/stores/system.svelte.js');
  }
  return _systemModule;
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

function resolveBrowserRelays(info) {
  const relays = [];
  if (Array.isArray(info?.nostr?.browser_relays)) relays.push(...info.nostr.browser_relays);
  if (info?.nostr?.sidecar_url) relays.push(info.nostr.sidecar_url);
  return Array.from(new Set(relays.map(normalizeRelayUrl).filter(Boolean)));
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
    const systemModule = await getSystemModule();
    const nostrModule = await getNostrModule();
    const info = systemModule.currentSystemInfo?.() || await systemModule.loadSystemInfo();
    const relays = resolveBrowserRelays(info);

    if (relays.length === 0) {
      throw new Error('No browser relays advertised by system discovery');
    }

    const { nostr } = nostrModule;
    nostr.setRelays(relays, false);
    const summary = await nostr.connect(relays, { force: false });
    if (!summary?.connected) {
      throw new Error('Unable to connect to any advertised browser relay');
    }
  } catch (err) {
    // Log warning but continue - let queries fail with clearer errors
    if (!silent) {
      console.warn('[nostr] Relay connection warning:', err?.message || err);
    }
  }
}
