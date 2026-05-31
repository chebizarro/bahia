export function relaySummaryFromStates(relayStates) {
  return Array.from(relayStates.entries()).map(([relay, state]) => ({ relay, ...state }));
}

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

export function summarizeRelayConnections(relays, statusMap = {}) {
  const relayStatuses = relays.map((url) => ({
    url,
    status: statusMap[url] || 'unknown'
  }));
  const connected = relayStatuses.filter((relay) => relay.status === 'connected').length;
  const failed = relayStatuses.filter((relay) => ['error', 'failed', 'disconnected'].includes(relay.status)).length;
  const connecting = relayStatuses.filter((relay) => relay.status === 'connecting').length;
  return { total: relays.length, connected, failed, connecting, relays: relayStatuses };
}

export function messageFromError(error) {
  if (error instanceof Error) return error.message;
  if (typeof error === 'string') return error;
  return String(error || 'unknown relay error');
}

export function publishSentBeforeFailure(message) {
  const lower = String(message || '').toLowerCase();
  return !lower.includes('connection failure') &&
    !lower.includes('closed connection') &&
    !lower.includes('connection skipped') &&
    !lower.includes('relay connection closed') &&
    !lower.includes('websocket closed');
}
