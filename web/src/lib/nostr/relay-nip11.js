import { CONTEXTVM_MAX_RELAY_MESSAGE_BYTES, STORED_EVENT_MAX_CONTENT_BYTES } from './encrypted-controlplane-constants.js';

const metadataCache = new Map();

export function relayMetadataUrl(relayUrl) {
  if (typeof relayUrl !== 'string') return '';
  if (relayUrl.startsWith('wss://')) return `https://${relayUrl.slice(6)}`;
  if (relayUrl.startsWith('ws://')) return `http://${relayUrl.slice(5)}`;
  return '';
}

export function fetchRelayMetadata(relayUrl, fetchImpl = globalThis.fetch) {
  if (metadataCache.has(relayUrl)) return metadataCache.get(relayUrl);
  const url = relayMetadataUrl(relayUrl);
  const result = (async () => {
    if (!url || typeof fetchImpl !== 'function') return { metadata: null, error: 'NIP-11 metadata fetch unavailable in this runtime' };
    try {
      const response = await fetchImpl(url, { headers: { Accept: 'application/nostr+json' }, signal: AbortSignal.timeout(3000) });
      if (!response.ok) return { metadata: null, error: `NIP-11 metadata HTTP ${response.status}` };
      const metadata = await response.json();
      return { metadata };
    } catch (error) {
      return { metadata: null, error: error?.message || String(error) };
    }
  })();
  metadataCache.set(relayUrl, result);
  return result;
}

function positiveInteger(value, fallback) {
  return Number.isSafeInteger(value) && value > 0 ? value : fallback;
}

export function limitsFromMetadata(metadata) {
  const limitation = metadata?.limitation;
  return {
    maxMessageBytes: positiveInteger(limitation?.max_message_length, CONTEXTVM_MAX_RELAY_MESSAGE_BYTES),
    maxContentBytes: positiveInteger(limitation?.max_content_length, STORED_EVENT_MAX_CONTENT_BYTES)
  };
}

export function inlineSBOMLimitBytes(limits) {
  // Preserve the proven 360 KiB ceiling; reserve the existing 20,480-byte
  // envelope allowance when a relay advertises a smaller frame limit.
  return Math.max(0, Math.min(360 * 1024, Math.floor((limits.maxMessageBytes - 20480) * 3 / 4)));
}

export function createRelayLimitResolver(fetchMetadata = fetchRelayMetadata) {
  const resolved = new Map();
  const fallback = limitsFromMetadata(null);
  function cached(relays = []) {
    const available = relays.map(relay => resolved.get(relay)).filter(Boolean);
    if (available.length === 0) return fallback;
    return available.reduce((limits, next) => ({
      maxMessageBytes: Math.min(limits.maxMessageBytes, next.maxMessageBytes),
      maxContentBytes: Math.min(limits.maxContentBytes, next.maxContentBytes)
    }));
  }
  return {
    cached,
    async resolve(relays = []) {
      await Promise.all([...new Set(relays)].map(async relay => {
        if (!resolved.has(relay)) resolved.set(relay, limitsFromMetadata((await fetchMetadata(relay))?.metadata));
      }));
      return cached(relays);
    }
  };
}

export const relayLimits = createRelayLimitResolver();
