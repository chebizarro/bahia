import { currentSystemInfo } from '$lib/stores/system.svelte.js';
import { getTagValues } from './client.js';

export function normalizeTags(tags) {
  if (!Array.isArray(tags)) return [];
  return tags
    .filter((tag) => Array.isArray(tag) && typeof tag[0] === 'string' && tag[0])
    .map((tag) => tag.map((value) => String(value)));
}

export function normalizeRelays(relays) {
  if (!Array.isArray(relays)) return [];
  const seen = new Set();
  const out = [];
  for (const relay of relays) {
    const url = typeof relay === 'string' ? relay.trim() : '';
    if (!/^wss?:\/\//i.test(url) || seen.has(url)) continue;
    seen.add(url);
    out.push(url);
  }
  return out;
}

export function publishAccepted(result) {
  return result?.sent === true && result?.accepted === true;
}

export function jsonContent(value) {
  return typeof value === 'string' ? value : JSON.stringify(value ?? {});
}

function contextVMMethod(operation) {
  const trimmed = String(operation || '').trim();
  if (!trimmed) return '';
  if (trimmed.includes('/')) return trimmed;
  const parts = trimmed.split('.').map((part) => part.trim()).filter(Boolean);
  if (parts.length >= 3 && parts[parts.length - 1] === 'request') {
    return `${parts[0]}/${parts.slice(1, -1).join('-')}`;
  }
  if (parts.length >= 2) return `${parts[0]}/${parts.slice(1).join('-')}`;
  return trimmed;
}

export function buildContextVMRequest({ operation, payload, requestId }) {
  const method = contextVMMethod(operation);
  const params = payload && typeof payload === 'object' && !Array.isArray(payload) ? { ...payload } : { value: payload };
  params._meta = {
    ...(params._meta && typeof params._meta === 'object' ? params._meta : {}),
    progressToken: requestId
  };
  return { jsonrpc: '2.0', id: requestId, method, params };
}

export function extractContextVMResult(payload, requestEventId, contextVMRequestId = requestEventId) {
  if (payload?.jsonrpc === '2.0') {
    if (payload.id !== contextVMRequestId) {
      throw new Error('ContextVM encrypted result payload did not correlate to the ContextVM request id');
    }
    if (payload.error) {
      throw new Error(payload.error.message || 'ContextVM encrypted request failed');
    }
    return payload.result ?? {};
  }
  if (payload?.request_event_id !== requestEventId) {
    throw new Error('ContextVM result payload did not correlate to the request event id');
  }
  return payload;
}

export function parseJson(value) {
  try {
    return JSON.parse(value);
  } catch (error) {
    throw new Error(`ContextVM result decrypted but did not contain valid JSON: ${error.message}`);
  }
}

export function randomId() {
  const cryptoApi = globalThis.crypto;
  if (cryptoApi?.randomUUID) return cryptoApi.randomUUID();
  if (cryptoApi?.getRandomValues) {
    const bytes = new Uint8Array(16);
    cryptoApi.getRandomValues(bytes);
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('');
  }
  return `req-${Date.now()}`;
}

export function hasTagValue(event, name, value) {
  return getTagValues(event, name).includes(value);
}

export function openRelayUrls(client) {
  if (typeof client?.getConnectedRelays !== 'function') return null;
  return client.getConnectedRelays();
}

export function formatClosedRelays(closedRelays) {
  return Array.from(closedRelays.entries())
    .map(([relay, reason]) => `${relay}${reason ? ` (${reason})` : ''}`)
    .join('; ');
}

export function signalAbortError(signal, fallback = 'ContextVM result wait aborted') {
  return signal?.reason instanceof Error ? signal.reason : new Error(fallback);
}

export function throwIfSignalAborted(signal, fallback) {
  if (signal?.aborted) throw signalAbortError(signal, fallback);
}

export function encryptedRelayUrlsFromSystemInfo(systemInfo = currentSystemInfo()) {
  return normalizeRelays(systemInfo?.nostr?.contextvm_relays || systemInfo?.nostr?.browser_relays);
}

export function servicePubkeyFromSystemInfo(systemInfo = currentSystemInfo()) {
  return systemInfo?.nostr?.service_pubkey || '';
}

export function encryptedRequestsAvailable(systemInfo = currentSystemInfo()) {
  return systemInfo?.features?.encrypted_nostr_requests === true
    && encryptedRelayUrlsFromSystemInfo(systemInfo).length > 0
    && Boolean(servicePubkeyFromSystemInfo(systemInfo));
}
