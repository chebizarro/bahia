import { browser } from '$app/environment';
import { loadSystemInfo } from './system.svelte.js';
import { bootstrapControlplane } from './controlplane.svelte.js';
import {
  DNS_COMMANDS,
  dnsResultIsFailure,
  startDNSCommand
} from '$lib/nostr/dns-controlplane.js';
import {
  getDTag,
  getTagValue,
  getTagValues,
  isReplaceableTombstone,
  nostr,
  upsertReplaceableEvent
} from '$lib/nostr/client.js';

import {
  DNS_ZONE_STATE,
  DNS_ENDPOINT_STATE,
  DNS_POLICY_STATE,
  DNS_BACKEND_STATE,
} from '$lib/nostr/kinds.gen.js';

export const DNS_READ_MODEL_KINDS = {
  ZONE: DNS_ZONE_STATE,
  ENDPOINT: DNS_ENDPOINT_STATE,
  POLICY: DNS_POLICY_STATE,
  BACKEND: DNS_BACKEND_STATE
};

const DNS_KIND_LIST = Object.values(DNS_READ_MODEL_KINDS);
const DNS_READ_MODEL_LIMIT = 5000;

export const dnsState = $state({
  zones: [],
  endpoints: [],
  driftEvents: [],
  policies: [],
  backends: [],
  loading: {
    zones: false,
    endpoints: false,
    drift: false,
    policies: false,
    backends: false,
    subscription: false
  },
  error: {
    zones: null,
    endpoints: null,
    drift: null,
    policies: null,
    backends: null,
    subscription: null
  },
  lastLoadedAt: {
    zones: null,
    endpoints: null,
    drift: null,
    policies: null,
    backends: null
  },
  connection: {
    status: 'idle',
    connected: false,
    relays: [],
    servicePubkey: '',
    relayHealth: {},
    metadata: {},
    eoseRelays: [],
    lastEoseAt: null,
    lastEventAt: null
  },
  commandRuns: []
});

const replaceableEvents = new Map();
const seenEventIds = new Set();
const zoneMap = new Map();
const endpointMap = new Map();
const policyMap = new Map();
const backendMap = new Map();

let unsubscribeDNSReadModels = null;
let commandRunSeq = 0;
let connectedRelayCount = 0;

function replaceArray(target, values) {
  target.length = 0;
  target.push(...values);
}

function sortByNameOrId(a, b) {
  return String(a.name || a.fqdn || a.id || '').localeCompare(String(b.name || b.fqdn || b.id || ''));
}

function refreshCollections() {
  replaceArray(dnsState.zones, Array.from(zoneMap.values()).sort(sortByNameOrId));
  replaceArray(dnsState.endpoints, Array.from(endpointMap.values()).sort(sortByNameOrId));
  replaceArray(dnsState.policies, Array.from(policyMap.values()).sort(sortByNameOrId));
  replaceArray(dnsState.backends, Array.from(backendMap.values()).sort(sortByNameOrId));
  replaceArray(dnsState.driftEvents, buildDriftEvents());
}

function buildDriftEvents() {
  return Array.from(endpointMap.values())
    .filter((endpoint) => {
      const drift = String(endpoint.drift_status || endpoint.driftStatus || '').toLowerCase();
      return drift && !['in_sync', 'insync', 'synced', 'none'].includes(drift);
    })
    .map((endpoint) => ({
      id: `drift:${endpoint.id}`,
      event_id: endpoint.nostr_event_id,
      fqdn: endpoint.fqdn,
      zone: endpoint.zone,
      status: endpoint.drift_status || endpoint.driftStatus,
      timestamp: endpoint.updated_at || endpoint.nostr_created_at,
      expected: endpoint.desired || endpoint.expected,
      actual: endpoint.observed || endpoint.actual,
      endpoint
    }))
    .sort((a, b) => String(b.timestamp || '').localeCompare(String(a.timestamp || '')));
}

function setCollectionLoading(value) {
  dnsState.loading.zones = value;
  dnsState.loading.endpoints = value;
  dnsState.loading.drift = value;
  dnsState.loading.policies = value;
  dnsState.loading.backends = value;
  dnsState.loading.subscription = value;
}

function setCollectionError(message) {
  dnsState.error.zones = message;
  dnsState.error.endpoints = message;
  dnsState.error.drift = message;
  dnsState.error.policies = message;
  dnsState.error.backends = message;
  dnsState.error.subscription = message;
}

function clearCollectionError() {
  setCollectionError(null);
}

function markCollectionLoaded(kind) {
  const now = Date.now();
  if (!kind || kind === DNS_READ_MODEL_KINDS.ZONE) dnsState.lastLoadedAt.zones = now;
  if (!kind || kind === DNS_READ_MODEL_KINDS.ENDPOINT) dnsState.lastLoadedAt.endpoints = now;
  if (!kind || kind === DNS_READ_MODEL_KINDS.POLICY) dnsState.lastLoadedAt.policies = now;
  if (!kind || kind === DNS_READ_MODEL_KINDS.BACKEND) dnsState.lastLoadedAt.backends = now;
  dnsState.lastLoadedAt.drift = now;
}

function normalizeRelayUrl(url) {
  if (!url || typeof url !== 'string') return '';
  const trimmed = url.trim();
  if (trimmed.startsWith('ws://') || trimmed.startsWith('wss://')) return trimmed;
  if (trimmed.startsWith('https://')) return `wss://${trimmed.slice('https://'.length)}`;
  if (trimmed.startsWith('http://')) return `ws://${trimmed.slice('http://'.length)}`;
  return trimmed;
}

function normalizeRelayList(value) {
  const values = Array.isArray(value) ? value : String(value || '').split(',');
  return Array.from(new Set(values.map(normalizeRelayUrl).filter(Boolean)));
}

function relayMetadataUrl(relayUrl) {
  if (!relayUrl || typeof relayUrl !== 'string') return '';
  if (relayUrl.startsWith('wss://')) return `https://${relayUrl.slice('wss://'.length)}`;
  if (relayUrl.startsWith('ws://')) return `http://${relayUrl.slice('ws://'.length)}`;
  return relayUrl;
}

async function queryRelayMetadata(relays) {
  const results = await Promise.all(relays.map(async (relay) => {
    const url = relayMetadataUrl(relay);
    if (!url || typeof fetch !== 'function') {
      return [relay, { ok: false, error: 'NIP-11 metadata fetch unavailable in this runtime' }];
    }

    try {
      const response = await fetch(url, { headers: { Accept: 'application/nostr+json' } });
      if (!response.ok) {
        return [relay, { ok: false, error: `NIP-11 metadata HTTP ${response.status}` }];
      }
      const metadata = await response.json();
      return [relay, { ok: true, metadata }];
    } catch (error) {
      return [relay, { ok: false, error: error?.message || String(error) }];
    }
  }));

  const health = {};
  const metadata = {};
  for (const [relay, result] of results) {
    health[relay] = result.ok ? 'metadata-ok' : `metadata-error: ${result.error}`;
    if (result.metadata) metadata[relay] = result.metadata;
  }
  dnsState.connection.relayHealth = health;
  dnsState.connection.metadata = metadata;
  return results;
}

async function resolveSubscriptionConfig(relayUrl, pubkey) {
  let relays = normalizeRelayList(relayUrl);
  let servicePubkey = String(pubkey || '').trim();

  if ((relays.length === 0 || !servicePubkey) && browser) {
    const info = await loadSystemInfo();
    if (relays.length === 0) {
      relays = normalizeRelayList(info?.nostr?.browser_relays || info?.nostr?.sidecar_url || []);
    }
    servicePubkey = servicePubkey || String(info?.nostr?.service_pubkey || '').trim();
  }

  if (relays.length === 0) throw new Error('No DNS Nostr relay configured');
  if (!servicePubkey) throw new Error('No Bahia service pubkey configured for DNS read-model subscription');

  return { relays, servicePubkey };
}

export function dnsReadModelFilters(pubkey = dnsState.connection.servicePubkey, { since = null } = {}) {
  const servicePubkey = String(pubkey || '').trim();
  const authorFilter = servicePubkey ? { authors: [servicePubkey] } : {};
  const temporal = since ? { since } : { limit: DNS_READ_MODEL_LIMIT };
  return [{ kinds: DNS_KIND_LIST, '#t': ['bahia'], ...temporal, ...authorFilter }];
}

function parseEventContent(event) {
  try {
    const content = JSON.parse(event?.content || '{}');
    if (!content || typeof content !== 'object' || Array.isArray(content)) {
      throw new Error('content must be a JSON object');
    }
    return content;
  } catch (error) {
    throw new Error(`invalid DNS read-model JSON content: ${error?.message || error}`);
  }
}

function eventMeta(event, content) {
  return {
    ...content,
    id: content.id || content.coordinate || getDTag(event),
    d: getDTag(event),
    nostr_event_id: event.id,
    nostr_pubkey: event.pubkey,
    nostr_created_at: event.created_at ? new Date(event.created_at * 1000).toISOString() : ''
  };
}

function normalizeEndpointEvent(event, content) {
  const coordinate = content.coordinate || getDTag(event);
  const capabilities = Array.isArray(content.capabilities)
    ? content.capabilities
    : getTagValues(event, 'capability');
  const service = content.service || content.service_id || getTagValue(event, 'service');
  const route = content.route || content.route_id || getTagValue(event, 'route');
  const environment = content.environment || content.env || getTagValue(event, 'env') || getTagValue(event, 'environment');
  const fqdn = content.fqdn || content.dns || getTagValue(event, 'dns');
  const address = content.address || content.addr || getTagValue(event, 'addr');

  return {
    ...eventMeta(event, content),
    id: coordinate,
    coordinate,
    service,
    service_id: content.service_id || service,
    route,
    route_id: content.route_id || route,
    environment,
    env: environment,
    fqdn,
    dns: fqdn,
    name: content.name || route || service || fqdn || coordinate,
    address,
    addr: address,
    protocol: content.protocol || content.proto || getTagValue(event, 'proto'),
    proto: content.proto || content.protocol || getTagValue(event, 'proto'),
    port: content.port || getTagValue(event, 'port'),
    health: content.health || getTagValue(event, 'health'),
    runtime: content.runtime || getTagValue(event, 'runtime'),
    zone: content.zone || getTagValue(event, 'zone'),
    family: content.family || getTagValue(event, 'family'),
    npub: content.npub || content.worker_pubkey || getTagValue(event, 'npub') || getTagValue(event, 'worker'),
    mesh: content.mesh || getTagValue(event, 'mesh'),
    capabilities
  };
}

function normalizeZoneEvent(event, content) {
  return {
    ...eventMeta(event, content),
    id: getDTag(event),
    name: content.name || content.zone || getTagValue(event, 'zone') || getDTag(event).replace(/^zone:/, ''),
    zone: content.zone || content.name || getTagValue(event, 'zone'),
    backend: content.backend || content.backend_ref || getTagValue(event, 'backend'),
    provider: content.provider || content.backend_ref || getTagValue(event, 'backend'),
    visibility: content.visibility || getTagValue(event, 'visibility'),
    health: content.health || content.status || 'projected'
  };
}

function normalizePolicyEvent(event, content) {
  return {
    ...eventMeta(event, content),
    id: content.id || getTagValue(event, 'policy') || getDTag(event),
    name: content.name || getTagValue(event, 'policy') || getDTag(event),
    zone: content.zone || content.zone_id || getTagValue(event, 'zone'),
    enabled: content.enabled ?? getTagValue(event, 'enabled', true),
    rule_count: Array.isArray(content.rules) ? content.rules.length : content.rule_count,
    rules: Array.isArray(content.rules) ? content.rules : []
  };
}

function normalizeBackendEvent(event, content) {
  return {
    ...eventMeta(event, content),
    id: content.ref || getTagValue(event, 'backend') || getDTag(event),
    name: content.ref || getTagValue(event, 'backend') || getDTag(event),
    ref: content.ref || getTagValue(event, 'backend'),
    type: content.type || getTagValue(event, 'type'),
    health: content.health || getTagValue(event, 'health'),
    zones: Array.isArray(content.zones) ? content.zones : getTagValues(event, 'zone')
  };
}

function validateDNSReadModelEvent(event, content) {
  if (!event?.id || typeof event.kind !== 'number') return 'DNS read-model event is missing id or kind';
  if (!DNS_KIND_LIST.includes(event.kind)) return `Unsupported DNS read-model kind ${event.kind}`;
  if (dnsState.connection.servicePubkey && event.pubkey !== dnsState.connection.servicePubkey) {
    return 'DNS read-model event author does not match configured Bahia service pubkey';
  }
  if (!getDTag(event)) return 'DNS read-model event is missing required d tag';
  if (content.deleted !== true && !getTagValues(event, 't').includes('bahia')) {
    return 'DNS read-model event is missing required bahia type tag';
  }
  return '';
}

function applyToKindMap(event, content, targetMap, normalizer) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const d = getDTag(event);
  if (isReplaceableTombstone(event) || content.deleted === true) {
    targetMap.delete(d);
  } else {
    targetMap.set(d, normalizer(event, content));
  }
  return true;
}

export function applyDNSReadModelEvent(event) {
  if (!event?.id) return false;
  if (seenEventIds.has(event.id)) return false;

  let content;
  try {
    content = parseEventContent(event);
  } catch (error) {
    dnsState.error.subscription = error?.message || String(error);
    return false;
  }

  const validationError = validateDNSReadModelEvent(event, content);
  if (validationError) {
    dnsState.error.subscription = validationError;
    return false;
  }

  let changed = false;
  switch (event.kind) {
    case DNS_READ_MODEL_KINDS.ZONE:
      changed = applyToKindMap(event, content, zoneMap, normalizeZoneEvent);
      break;
    case DNS_READ_MODEL_KINDS.ENDPOINT:
      changed = applyToKindMap(event, content, endpointMap, normalizeEndpointEvent);
      break;
    case DNS_READ_MODEL_KINDS.POLICY:
      changed = applyToKindMap(event, content, policyMap, normalizePolicyEvent);
      break;
    case DNS_READ_MODEL_KINDS.BACKEND:
      changed = applyToKindMap(event, content, backendMap, normalizeBackendEvent);
      break;
  }

  seenEventIds.add(event.id);
  if (changed) {
    dnsState.connection.lastEventAt = new Date().toISOString();
    markCollectionLoaded(event.kind);
    clearCollectionError();
    refreshCollections();
  }
  return changed;
}

function startDNSReadModelSubscription(since = null) {
  if (unsubscribeDNSReadModels) {
    unsubscribeDNSReadModels();
    unsubscribeDNSReadModels = null;
  }

  const eoseRelays = new Set();
  dnsState.connection.eoseRelays = [];
  setCollectionLoading(true);

  unsubscribeDNSReadModels = nostr.subscribe(dnsReadModelFilters(dnsState.connection.servicePubkey, { since }), {
    onEvent: (event) => {
      applyDNSReadModelEvent(event);
    },
    onEose: (relay) => {
      if (relay) eoseRelays.add(relay);
      dnsState.connection.eoseRelays = Array.from(eoseRelays);
      dnsState.connection.lastEoseAt = new Date().toISOString();
      if (connectedRelayCount === 0 || eoseRelays.size >= connectedRelayCount) {
        setCollectionLoading(false);
        dnsState.connection.status = 'live';
        markCollectionLoaded();
      }
    },
    onClosed: (reason = '', relay = '', meta = {}) => {
      const message = `DNS Nostr subscription closed${relay ? ` on ${relay}` : ''}: ${reason || 'closed'}`;
      dnsState.error.subscription = message;
      dnsState.connection.lastClosed = { reason: String(reason || ''), relay, terminal: meta?.terminal !== false, source: meta?.source || 'closed' };
      dnsState.connection.relayHealth = {
        ...dnsState.connection.relayHealth,
        ...(relay ? { [relay]: `closed: ${reason || 'closed'}` } : {})
      };
      if (meta?.terminal === false) {
        dnsState.connection.status = 'reconnecting';
        return;
      }
      if (String(reason).toLowerCase().includes('auth')) {
        dnsState.connection.status = 'auth-required';
        setCollectionLoading(false);
        return;
      }
      dnsState.connection.status = 'degraded';
    },
    onAuth: (challenge = '', relay = '') => {
      const reason = challenge || 'auth-required';
      dnsState.error.subscription = `DNS Nostr subscription requires AUTH${relay ? ` on ${relay}` : ''}: ${reason}`;
      dnsState.connection.lastClosed = { reason, relay, terminal: true, source: 'auth' };
      dnsState.connection.status = 'auth-required';
      setCollectionLoading(false);
    }
  });
}

export async function connect(relayUrl, pubkey) {
  if (!browser) return { ok: false, reason: 'not_browser' };

  disconnect({ resetData: false });
  clearCollectionError();
  setCollectionLoading(true);
  dnsState.connection.status = 'connecting';

  try {
    const { relays, servicePubkey } = await resolveSubscriptionConfig(relayUrl, pubkey);
    dnsState.connection.relays = relays;
    dnsState.connection.servicePubkey = servicePubkey;

    await queryRelayMetadata(relays);

    nostr.setRelays(relays, false);
    const summary = await nostr.connect(relays, { force: true });
    connectedRelayCount = Number(summary?.connected || 0);
    if (connectedRelayCount === 0) {
      throw new Error('Unable to connect to any DNS Nostr relay');
    }

    dnsState.connection.connected = true;
    dnsState.connection.status = 'subscribing';
    startDNSReadModelSubscription();
    return { ok: true, relays, servicePubkey };
  } catch (error) {
    const message = error?.message || String(error);
    dnsState.connection.status = 'error';
    dnsState.connection.connected = false;
    setCollectionLoading(false);
    setCollectionError(message);
    return { ok: false, reason: message };
  }
}

export function disconnect({ resetData = false } = {}) {
  if (unsubscribeDNSReadModels) {
    unsubscribeDNSReadModels();
    unsubscribeDNSReadModels = null;
  }
  connectedRelayCount = 0;
  dnsState.connection.connected = false;
  dnsState.connection.status = 'disconnected';
  dnsState.connection.eoseRelays = [];
  setCollectionLoading(false);

  if (resetData) {
    replaceableEvents.clear();
    seenEventIds.clear();
    zoneMap.clear();
    endpointMap.clear();
    policyMap.clear();
    backendMap.clear();
    refreshCollections();
  }
}

function nextCommandRunId(command) {
  commandRunSeq += 1;
  return `dns-${command}-${commandRunSeq}`;
}

function pushCommandRun(run) {
  dnsState.commandRuns = [run, ...dnsState.commandRuns].slice(0, 50);
  return run;
}

function commandErrorMessage(error) {
  return error?.message || String(error || 'DNS command failed');
}

export async function startDNSCommandRun(command, payload = {}, { tags = [], signal } = {}) {
  const run = pushCommandRun({
    id: nextCommandRunId(command),
    command,
    phase: 'publishing',
    requestEventId: '',
    publishOk: [],
    acceptedRelays: [],
    rejectedRelays: [],
    statusEvents: [],
    result: null,
    error: null,
    payload,
    startedAt: Date.now(),
    completedAt: null
  });

  try {
    await bootstrapControlplane();
    const tracker = await startDNSCommand({
      command,
      payload,
      tags,
      signal,
      onStatus: (status) => {
        run.statusEvents = [...run.statusEvents, status];
        run.phase = 'processing';
      },
      onClosed: (error) => {
        run.error = commandErrorMessage(error);
      }
    });

    run.phase = 'published';
    run.requestEventId = tracker.requestEventId;
    run.publishOk = tracker.ok;
    run.acceptedRelays = tracker.acceptedRelays;
    run.rejectedRelays = tracker.rejectedRelays;

    run.result = tracker.result.then((result) => {
      run.result = result;
      run.phase = dnsResultIsFailure(result) ? 'failed' : 'completed';
      if (dnsResultIsFailure(result)) run.error = result.error || result.message || 'DNS command failed';
      run.completedAt = Date.now();
      return result;
    }).catch((error) => {
      run.phase = 'error';
      run.error = commandErrorMessage(error);
      run.completedAt = Date.now();
      throw error;
    });

    return run;
  } catch (error) {
    run.phase = String(commandErrorMessage(error)).includes('publish rejected') ? 'rejected' : 'error';
    run.error = commandErrorMessage(error);
    run.completedAt = Date.now();
    throw error;
  }
}

export function createDNSZone(payload, options = {}) {
  return startDNSCommandRun(DNS_COMMANDS.ZONE_CREATE, payload, options);
}

export function applyDNSPolicy(payload, options = {}) {
  return startDNSCommandRun(DNS_COMMANDS.POLICY_APPLY, payload, options);
}

export function overrideDNSRecord(payload, options = {}) {
  return startDNSCommandRun(DNS_COMMANDS.RECORD_OVERRIDE, payload, options);
}

export function remediateDNSDrift(payload = {}, options = {}) {
  return startDNSCommandRun(DNS_COMMANDS.DRIFT_REMEDIATE, payload, options);
}

export function seedDnsState({ zones = [], endpoints = [], driftEvents = [], policies = [], backends = [] } = {}) {
  replaceArray(dnsState.zones, Array.isArray(zones) ? zones : []);
  replaceArray(dnsState.endpoints, Array.isArray(endpoints) ? endpoints : []);
  replaceArray(dnsState.driftEvents, Array.isArray(driftEvents) ? driftEvents : []);
  replaceArray(dnsState.policies, Array.isArray(policies) ? policies : []);
  replaceArray(dnsState.backends, Array.isArray(backends) ? backends : []);
}

export function resetDnsReadModels() {
  disconnect({ resetData: true });
  clearCollectionError();
  dnsState.connection.status = 'idle';
  dnsState.connection.relays = [];
  dnsState.connection.servicePubkey = '';
  dnsState.connection.relayHealth = {};
  dnsState.connection.metadata = {};
  dnsState.connection.lastEoseAt = null;
  dnsState.connection.lastEventAt = null;
  dnsState.connection.lastClosed = null;
}

export function bootstrapDnsDashboard({ relays = null, servicePubkey = '', systemInfo = null } = {}) {
  const resolvedRelays = relays || systemInfo?.nostr?.browser_relays || systemInfo?.nostr?.sidecar_url || [];
  const resolvedPubkey = servicePubkey || systemInfo?.nostr?.service_pubkey || '';
  return connect(resolvedRelays, resolvedPubkey);
}

export function reconnectDnsDashboard(options = {}) {
  disconnect({ resetData: false });
  return bootstrapDnsDashboard(options);
}

export function applyDnsEvent(event) {
  return applyDNSReadModelEvent(event);
}

export function resetDnsStore() {
  resetDnsReadModels();
  resetDnsCommandRuns();
}

export function resetDnsCommandRuns() {
  dnsState.commandRuns = [];
  commandRunSeq = 0;
}
