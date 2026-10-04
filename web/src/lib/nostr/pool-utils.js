function arrayFrom(value) {
  if (!value) return [];
  return Array.isArray(value) ? value : [value];
}

export function normalizeRelayUrl(url) {
  if (typeof url !== 'string' || !url.trim()) return '';
  try {
    let normalized = url.trim();
    if (!normalized.includes('://')) normalized = `wss://${normalized}`;
    const parsed = new URL(normalized);
    if (parsed.protocol === 'http:') parsed.protocol = 'ws:';
    if (parsed.protocol === 'https:') parsed.protocol = 'wss:';
    parsed.pathname = parsed.pathname.replace(/\/+/g, '/');
    if (parsed.pathname.endsWith('/')) parsed.pathname = parsed.pathname.slice(0, -1);
    if ((parsed.port === '80' && parsed.protocol === 'ws:') || (parsed.port === '443' && parsed.protocol === 'wss:')) parsed.port = '';
    parsed.searchParams.sort();
    parsed.hash = '';
    return parsed.toString();
  } catch {
    return url.trim();
  }
}

// toWebSocketUrl coerces a configured relay URL to its ws(s) scheme without
// otherwise rewriting it, preserving the exact string used as a relay key by
// configuration, NIP-66 d-tags and read-model state. normalizeRelayUrl above is
// the stricter pool-identity form and is not interchangeable with it.
export function toWebSocketUrl(url) {
  if (!url || typeof url !== 'string') return '';
  if (url.startsWith('ws://') || url.startsWith('wss://')) return url;
  if (url.startsWith('https://')) return `wss://${url.slice('https://'.length)}`;
  if (url.startsWith('http://')) return `ws://${url.slice('http://'.length)}`;
  return url;
}

export function uniqueRelays(relays = []) {
  const seen = new Set();
  const out = [];
  for (const relay of arrayFrom(relays)) {
    const url = typeof relay === 'string' ? relay.trim() : '';
    if (!url) continue;
    const key = normalizeRelayUrl(url);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(url);
  }
  return out;
}
