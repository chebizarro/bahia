const BOOTSTRAP_RETRY_INTERVAL_MS = 30_000;
let lastBootstrapFailedAt = null;
let bootstrapControlplaneForRetry = null;

export const controlplaneConnection = $state({
  status: 'idle',
  connected: false,
  ready: false,
  bootstrapComplete: false,
  relays: [],
  servicePubkey: '',
  lastError: null,
  lastEoseAt: null,
  resubscribeAttempts: 0,
  lastClosedReason: null,
  lastEventAt: null,
  reconnects: 0
});

export function clearBootstrapRetryLimit() {
  lastBootstrapFailedAt = null;
}

export function markBootstrapFailedAt(timestamp = Date.now()) {
  lastBootstrapFailedAt = timestamp;
}

export function bootstrapRetryLimited(now = Date.now()) {
  if (lastBootstrapFailedAt === null) return false;
  return now - lastBootstrapFailedAt < BOOTSTRAP_RETRY_INTERVAL_MS;
}

export function registerBootstrapControlplaneForRetry(fn) {
  bootstrapControlplaneForRetry = fn;
}

export async function manualRetry() {
  clearBootstrapRetryLimit();
  if (typeof bootstrapControlplaneForRetry !== 'function') {
    return { ok: false, reason: 'controlplane bootstrap is not registered' };
  }
  return bootstrapControlplaneForRetry({ force: true });
}

export function resetConnectionState() {
  clearBootstrapRetryLimit();
  controlplaneConnection.status = 'idle';
  controlplaneConnection.connected = false;
  controlplaneConnection.ready = false;
  controlplaneConnection.bootstrapComplete = false;
  controlplaneConnection.relays = [];
  controlplaneConnection.servicePubkey = '';
  controlplaneConnection.lastError = null;
  controlplaneConnection.lastEoseAt = null;
  controlplaneConnection.resubscribeAttempts = 0;
  controlplaneConnection.lastClosedReason = null;
  controlplaneConnection.lastEventAt = null;
  controlplaneConnection.reconnects = 0;
}

export function normalizeRelayUrl(url) {
  if (!url || typeof url !== 'string') return '';
  if (url.startsWith('ws://') || url.startsWith('wss://')) return url;
  if (url.startsWith('https://')) return `wss://${url.slice('https://'.length)}`;
  if (url.startsWith('http://')) return `ws://${url.slice('http://'.length)}`;
  return url;
}

export function resolveBrowserRelays(systemInfo) {
  const nostrInfo = systemInfo?.nostr || {};
  const relays = [];

  if (Array.isArray(nostrInfo.browser_relays)) relays.push(...nostrInfo.browser_relays);
  if (nostrInfo.sidecar_url) relays.push(nostrInfo.sidecar_url);

  return Array.from(new Set(relays.map(normalizeRelayUrl).filter(Boolean)));
}

export function connectedRelaysFromSummary(summary) {
  return (summary?.relays || [])
    .filter((relay) => relay?.status === 'connected')
    .map((relay) => relay.url)
    .filter(Boolean);
}

export function setBootstrapError(message) {
  controlplaneConnection.status = 'error';
  controlplaneConnection.ready = false;
  controlplaneConnection.bootstrapComplete = false;
  controlplaneConnection.lastError = message;
}

export function markBootstrapComplete() {
  controlplaneConnection.ready = true;
  controlplaneConnection.bootstrapComplete = true;
  controlplaneConnection.status = 'live';
}
