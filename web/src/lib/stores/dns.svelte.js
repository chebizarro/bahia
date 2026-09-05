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
  replaceableKey,
  shouldAcceptReplaceableEvent,
  upsertReplaceableEvent,
  validateInboundNostrEvent
} from '$lib/nostr/client.js';

import {
  CASCADIA_CONTROLPLANE_STATE,
  DNS_BACKEND_REGISTER_RESULT,
  DNS_DRIFT_REMEDIATE_RESULT,
  DNS_OPERATION_STATUS,
  DNS_POLICY_APPLY_RESULT,
  DNS_RECORD_OVERRIDE_RESULT,
  DNS_STATE_SCHEMAS,
  DNS_ZONE_CREATE_RESULT
} from '$lib/nostr/kinds.gen.js';

export const DNS_READ_MODEL_SCHEMAS = DNS_STATE_SCHEMAS;
export const DNS_READ_MODEL_KINDS = Object.freeze({
  STATE: CASCADIA_CONTROLPLANE_STATE
});

const DNS_SCHEMA_LIST = Object.values(DNS_READ_MODEL_SCHEMAS);
const DNS_READ_MODEL_LIMIT = 5000;
const DNS_OPERATION_BACKFILL_SECONDS = 7 * 24 * 60 * 60;
const DNS_OPERATION_LIMIT = 1000;
export const DNS_OPERATION_KINDS = Object.freeze([
  DNS_OPERATION_STATUS,
  DNS_ZONE_CREATE_RESULT,
  DNS_POLICY_APPLY_RESULT,
  DNS_RECORD_OVERRIDE_RESULT,
  DNS_DRIFT_REMEDIATE_RESULT,
  DNS_BACKEND_REGISTER_RESULT
]);
const NIP66_RELAY_MONITOR_ANNOUNCEMENT = 10166;
const NIP66_RELAY_DISCOVERY = 30166;

export const DNS_AVAILABILITY = Object.freeze({
  LOADING: 'loading',
  DISABLED: 'disabled',
  ACTIVE: 'active'
});

export const dnsState = $state({
  availability: DNS_AVAILABILITY.LOADING,
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
    trustedRelayMonitors: [],
    relayMonitorAnnouncements: {},
    relayMonitorMetadata: {},
    relayMonitorErrors: [],
    eoseRelays: [],
    lastEoseAt: null,
    resubscribeAttempts: 0,
    lastClosedReason: null,
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
const relayMonitorEvents = new Map();
const seenRelayMonitorEventIds = new Set();

let unsubscribeDNSReadModels = null;
let unsubscribeRelayMonitors = null;
let commandRunSeq = 0;
let connectedRelayCount = 0;

function replaceArray(target, values) {
  target.length = 0;
  target.push(...values);
}

function sortByNameOrId(a, b) {
  return String(a.name || a.fqdn || a.id || '').localeCompare(String(b.name || b.fqdn || b.id || ''));
}

function refreshAvailability() {
  const caughtUp = dnsState.connection.status === 'live' && !dnsState.loading.subscription;
  if (!caughtUp) {
    dnsState.availability = DNS_AVAILABILITY.LOADING;
    return;
  }
  dnsState.availability = dnsState.zones.length === 0 && dnsState.backends.length === 0
    ? DNS_AVAILABILITY.DISABLED
    : DNS_AVAILABILITY.ACTIVE;
}

function refreshCollections() {
  replaceArray(dnsState.zones, Array.from(zoneMap.values()).sort(sortByNameOrId));
  replaceArray(dnsState.endpoints, Array.from(endpointMap.values()).sort(sortByNameOrId));
  replaceArray(dnsState.policies, Array.from(policyMap.values()).sort(sortByNameOrId));
  replaceArray(dnsState.backends, Array.from(backendMap.values()).sort(sortByNameOrId));
  replaceArray(dnsState.driftEvents, buildDriftEvents());
  refreshAvailability();
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

function markCollectionLoaded(schema) {
  const now = Date.now();
  if (!schema || schema === DNS_READ_MODEL_SCHEMAS.ZONE) dnsState.lastLoadedAt.zones = now;
  if (!schema || schema === DNS_READ_MODEL_SCHEMAS.ENDPOINT) dnsState.lastLoadedAt.endpoints = now;
  if (!schema || schema === DNS_READ_MODEL_SCHEMAS.POLICY) dnsState.lastLoadedAt.policies = now;
  if (!schema || schema === DNS_READ_MODEL_SCHEMAS.BACKEND) dnsState.lastLoadedAt.backends = now;
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

function normalizeSupportedNips(value) {
  if (!Array.isArray(value)) return { supported_nips: [], warnings: ['supported_nips missing or not an array'] };
  const supported = [];
  const warnings = [];
  for (const entry of value) {
    const number = typeof entry === 'number' ? entry : Number(entry);
    if (!Number.isInteger(number) || number <= 0) {
      warnings.push(`invalid supported_nips value ${String(entry)}`);
      continue;
    }
    if (!supported.includes(number)) supported.push(number);
  }
  return { supported_nips: supported, warnings };
}

function relayLimitationWarnings(limitation = {}) {
  const warnings = [];
  if (limitation?.auth_required === true) warnings.push('auth-required');
  if (limitation?.payment_required === true) warnings.push('payment-required');
  if (limitation?.restricted_writes === true) warnings.push('restricted-writes');
  if (Number.isInteger(limitation?.max_limit) && limitation.max_limit > 0) warnings.push(`max-limit:${limitation.max_limit}`);
  return warnings;
}

function normalizeNIP11Metadata(metadata) {
  if (!metadata || typeof metadata !== 'object' || Array.isArray(metadata)) {
    return { ok: false, status: 'metadata-malformed', error: 'NIP-11 metadata must be a JSON object' };
  }
  const { supported_nips, warnings } = normalizeSupportedNips(metadata.supported_nips || []);
  const limitation = metadata.limitation && typeof metadata.limitation === 'object' && !Array.isArray(metadata.limitation)
    ? metadata.limitation
    : {};
  const limiting = relayLimitationWarnings(limitation);
  const allWarnings = [...warnings, ...limiting];
  const normalized = {
    ...metadata,
    supported_nips,
    advisory_warnings: allWarnings,
    advisory_limitations: {
      auth_required: limitation.auth_required === true,
      payment_required: limitation.payment_required === true,
      restricted_writes: limitation.restricted_writes === true,
      max_limit: Number.isInteger(limitation.max_limit) ? limitation.max_limit : null
    }
  };
  if (warnings.length > 0) {
    return { ok: false, status: 'metadata-malformed', error: warnings.join('; '), metadata: normalized };
  }
  if (limiting.length > 0) {
    return { ok: true, status: 'metadata-limited', metadata: normalized };
  }
  return { ok: true, status: 'metadata-ok', metadata: normalized };
}

async function queryRelayMetadata(relays) {
  const results = await Promise.all(relays.map(async (relay) => {
    const url = relayMetadataUrl(relay);
    if (!url || typeof fetch !== 'function') {
      return [relay, { ok: false, status: 'metadata-unavailable', error: 'NIP-11 metadata fetch unavailable in this runtime' }];
    }

    try {
      const response = await fetch(url, { headers: { Accept: 'application/nostr+json' } });
      if (!response.ok) {
        return [relay, { ok: false, status: 'metadata-unavailable', error: `NIP-11 metadata HTTP ${response.status}` }];
      }
      const metadata = await response.json();
      return [relay, normalizeNIP11Metadata(metadata)];
    } catch (error) {
      return [relay, { ok: false, status: 'metadata-unavailable', error: error?.message || String(error) }];
    }
  }));

  const health = {};
  const metadata = {};
  for (const [relay, result] of results) {
    health[relay] = result.ok ? result.status : `${result.status}: ${result.error}`;
    if (result.metadata) metadata[relay] = result.metadata;
  }
  dnsState.connection.relayHealth = health;
  dnsState.connection.metadata = metadata;
  return results;
}

function normalizePubkeyList(value) {
  const values = Array.isArray(value) ? value : String(value || '').split(',');
  return Array.from(new Set(values.map((entry) => String(entry || '').trim().toLowerCase()).filter((entry) => /^[0-9a-f]{64}$/.test(entry))));
}

async function resolveSubscriptionConfig(relayUrl, pubkey, options = {}) {
  let relays = normalizeRelayList(relayUrl);
  let servicePubkey = String(pubkey || '').trim();
  let trustedRelayMonitors = normalizePubkeyList(options.trustedRelayMonitorPubkeys || options.trustedRelayMonitors || []);

  if ((relays.length === 0 || !servicePubkey || trustedRelayMonitors.length === 0) && browser) {
    const info = await loadSystemInfo();
    if (relays.length === 0) {
      relays = normalizeRelayList(info?.nostr?.browser_relays || info?.nostr?.sidecar_url || []);
    }
    servicePubkey = servicePubkey || String(info?.nostr?.service_pubkey || '').trim();
    if (trustedRelayMonitors.length === 0) {
      trustedRelayMonitors = normalizePubkeyList(info?.nostr?.trusted_relay_monitor_pubkeys || info?.nostr?.relay_monitor_pubkeys || []);
    }
  }

  if (relays.length === 0) throw new Error('No DNS Nostr relay configured');
  if (!servicePubkey) throw new Error('No Bahia service pubkey configured for DNS read-model subscription');

  return { relays, servicePubkey, trustedRelayMonitors };
}

export function dnsReadModelFilters(pubkey = dnsState.connection.servicePubkey, { since = null } = {}) {
  const servicePubkey = String(pubkey || '').trim();
  const authorFilter = servicePubkey ? { authors: [servicePubkey] } : {};
  const temporal = since ? { since } : { limit: DNS_READ_MODEL_LIMIT };
  return [
    { kinds: [CASCADIA_CONTROLPLANE_STATE], '#domain': ['dns'], '#schema': DNS_SCHEMA_LIST, ...temporal, ...authorFilter },
    {
      kinds: DNS_OPERATION_KINDS,
      since: since || Math.floor(Date.now() / 1000) - DNS_OPERATION_BACKFILL_SECONDS,
      limit: DNS_OPERATION_LIMIT,
      ...authorFilter
    }
  ];
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

function dnsEventSchema(event, content) {
  return getTagValue(event, 'schema', content.schema || '');
}

function validateDNSReadModelEvent(event, content) {
  if (!event?.id || typeof event.kind !== 'number') return 'DNS read-model event is missing id or kind';
  if (event.kind !== CASCADIA_CONTROLPLANE_STATE) return `Unsupported DNS read-model kind ${event.kind}; expected canonical CAS state kind`;
  if (getTagValue(event, 'domain', content.domain || '') !== 'dns') return 'DNS read-model event is missing required dns domain tag';
  const schema = dnsEventSchema(event, content);
  if (!DNS_SCHEMA_LIST.includes(schema)) return `Unsupported DNS read-model schema ${schema || '(missing)'}`;
  if (dnsState.connection.servicePubkey && event.pubkey !== dnsState.connection.servicePubkey) {
    return 'DNS read-model event author does not match configured Bahia service pubkey';
  }
  if (!getDTag(event)) return 'DNS read-model event is missing required d tag';
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

  const schema = dnsEventSchema(event, content);
  let changed = false;
  switch (schema) {
    case DNS_READ_MODEL_SCHEMAS.ZONE:
      changed = applyToKindMap(event, content, zoneMap, normalizeZoneEvent);
      break;
    case DNS_READ_MODEL_SCHEMAS.ENDPOINT:
      changed = applyToKindMap(event, content, endpointMap, normalizeEndpointEvent);
      break;
    case DNS_READ_MODEL_SCHEMAS.POLICY:
      changed = applyToKindMap(event, content, policyMap, normalizePolicyEvent);
      break;
    case DNS_READ_MODEL_SCHEMAS.BACKEND:
      changed = applyToKindMap(event, content, backendMap, normalizeBackendEvent);
      break;
  }

  seenEventIds.add(event.id);
  if (changed) {
    dnsState.connection.lastEventAt = new Date().toISOString();
    markCollectionLoaded(schema);
    clearCollectionError();
    refreshCollections();
  }
  return changed;
}

function dnsOperationCommand(kind, content) {
  const byKind = {
    [DNS_ZONE_CREATE_RESULT]: DNS_COMMANDS.ZONE_CREATE,
    [DNS_POLICY_APPLY_RESULT]: DNS_COMMANDS.POLICY_APPLY,
    [DNS_RECORD_OVERRIDE_RESULT]: DNS_COMMANDS.RECORD_OVERRIDE,
    [DNS_DRIFT_REMEDIATE_RESULT]: DNS_COMMANDS.DRIFT_REMEDIATE,
    [DNS_BACKEND_REGISTER_RESULT]: 'backend_register'
  };
  return content.command || content.operation || byKind[kind] || 'dns_operation';
}

function dnsOperationStatus(event, content) {
  return String(getTagValue(event, 'status') || content.status || (event.kind === DNS_OPERATION_STATUS ? 'processing' : 'completed')).toLowerCase();
}

function dnsOperationSnapshot(event, content, status) {
  return {
    ...content,
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    created_at: event.created_at,
    status,
    step: getTagValue(event, 'step') || content.step,
    message: content.message
  };
}

export function applyDNSOperationEvent(event) {
  if (!event?.id || !DNS_OPERATION_KINDS.includes(event.kind) || seenEventIds.has(event.id)) return false;
  if (dnsState.connection.servicePubkey && event.pubkey !== dnsState.connection.servicePubkey) return false;
  const requestEventId = getTagValue(event, 'e');
  if (!requestEventId) return false;

  let content;
  try {
    content = parseEventContent(event);
  } catch (error) {
    dnsState.error.subscription = error?.message || String(error);
    return false;
  }

  const status = dnsOperationStatus(event, content);
  const snapshot = dnsOperationSnapshot(event, content, status);
  let run = dnsState.commandRuns.find((candidate) => candidate.requestEventId === requestEventId);
  if (!run) {
    run = {
      id: requestEventId,
      command: dnsOperationCommand(event.kind, content),
      phase: 'published',
      requestEventId,
      publishOk: [],
      acceptedRelays: [],
      rejectedRelays: [],
      statusEvents: [],
      result: null,
      error: null,
      payload: {},
      startedAt: (event.created_at || 0) * 1000,
      completedAt: null,
      relayFed: true
    };
  }

  if (event.kind === DNS_OPERATION_STATUS) {
    if (!run.statusEvents.some((statusEvent) => statusEvent.id === event.id)) {
      run.statusEvents = [...run.statusEvents, snapshot];
    }
    if (!run.result) run.phase = status;
  } else {
    run.result = snapshot;
    run.phase = dnsResultIsFailure(snapshot) ? 'failed' : 'completed';
    run.error = dnsResultIsFailure(snapshot) ? snapshot.error || snapshot.message || 'DNS command failed' : null;
    run.completedAt = (event.created_at || 0) * 1000;
  }

  seenEventIds.add(event.id);
  dnsState.commandRuns = [run, ...dnsState.commandRuns.filter((candidate) => candidate !== run)].slice(0, 50);
  dnsState.connection.lastEventAt = new Date().toISOString();
  return true;
}

export function relayMonitorFilters(relays = dnsState.connection.relays, trustedRelayMonitors = dnsState.connection.trustedRelayMonitors) {
  const relayList = normalizeRelayList(relays);
  const monitors = normalizePubkeyList(trustedRelayMonitors);
  if (relayList.length === 0 || monitors.length === 0) return [];
  return [
    { kinds: [NIP66_RELAY_MONITOR_ANNOUNCEMENT], authors: monitors, limit: Math.max(monitors.length, 1) },
    { kinds: [NIP66_RELAY_DISCOVERY], authors: monitors, '#d': relayList, limit: Math.max(relayList.length * monitors.length, 1) }
  ];
}

function relayMonitorTagValues(event, tagName) {
  return Array.isArray(event?.tags)
    ? event.tags.filter((tag) => Array.isArray(tag) && tag[0] === tagName && tag[1]).map((tag) => tag.slice(1))
    : [];
}

function parseRelayMonitorContent(event) {
  if (!event?.content) return {};
  const parsed = JSON.parse(event.content);
  if (!parsed || typeof parsed !== 'object' || Array.isArray(parsed)) {
    throw new Error('NIP-66 content must be a JSON object when present');
  }
  return parsed;
}

function relayMonitorRequirements(event) {
  return relayMonitorTagValues(event, 'R').map((values) => values[0]).filter(Boolean);
}

function relayMonitorLimitWarnings(requirements) {
  return requirements
    .filter((requirement) => !String(requirement).startsWith('!'))
    .filter((requirement) => ['auth', 'payment', 'writes', 'pow'].includes(String(requirement)));
}

export function applyRelayMonitorEvent(event) {
  if (!event?.id || seenRelayMonitorEventIds.has(event.id)) return false;
  const trusted = new Set(normalizePubkeyList(dnsState.connection.trustedRelayMonitors));
  if (!trusted.has(String(event.pubkey || '').toLowerCase())) return false;
  if (![NIP66_RELAY_MONITOR_ANNOUNCEMENT, NIP66_RELAY_DISCOVERY].includes(event.kind)) return false;
  const monitorKey = replaceableKey(event);
  if (!monitorKey || !shouldAcceptReplaceableEvent(relayMonitorEvents.get(monitorKey), event)) return false;

  let content;
  try {
    content = parseRelayMonitorContent(event);
  } catch (error) {
    dnsState.connection.relayMonitorErrors = [
      { event_id: event.id, pubkey: event.pubkey, reason: error?.message || String(error) },
      ...dnsState.connection.relayMonitorErrors
    ].slice(0, 20);
    seenRelayMonitorEventIds.add(event.id);
    return false;
  }

  if (event.kind === NIP66_RELAY_MONITOR_ANNOUNCEMENT) {
    dnsState.connection.relayMonitorAnnouncements = {
      ...dnsState.connection.relayMonitorAnnouncements,
      [event.pubkey]: {
        pubkey: event.pubkey,
        frequency: getTagValue(event, 'frequency'),
        checks: getTagValues(event, 'c'),
        timeouts: relayMonitorTagValues(event, 'timeout'),
        geohashes: getTagValues(event, 'g'),
        content,
        observed_at: new Date().toISOString()
      }
    };
    relayMonitorEvents.set(monitorKey, event);
    seenRelayMonitorEventIds.add(event.id);
    return true;
  }

  const relay = normalizeRelayUrl(getDTag(event));
  const configuredRelays = new Set(normalizeRelayList(dnsState.connection.relays));
  if (!relay || !configuredRelays.has(relay)) return false;

  const requirements = relayMonitorRequirements(event);
  const warnings = relayMonitorLimitWarnings(requirements);
  const supportedNips = getTagValues(event, 'N').map((value) => Number(value)).filter((value) => Number.isInteger(value) && value > 0);
  const rtt = Object.fromEntries(['rtt-open', 'rtt-read', 'rtt-write']
    .map((tag) => [tag, Number(getTagValue(event, tag))])
    .filter(([, value]) => Number.isFinite(value) && value >= 0));
  const monitorMetadata = {
    relay,
    monitor_pubkey: event.pubkey,
    event_id: event.id,
    created_at: event.created_at,
    requirements,
    warnings,
    supported_nips: supportedNips,
    rtt,
    network: getTagValue(event, 'n'),
    relay_types: getTagValues(event, 'T'),
    topics: getTagValues(event, 't'),
    geohashes: getTagValues(event, 'g'),
    content,
    advisory_only: true,
    observed_at: new Date().toISOString()
  };

  dnsState.connection.relayMonitorMetadata = {
    ...dnsState.connection.relayMonitorMetadata,
    [relay]: monitorMetadata
  };
  dnsState.connection.relayHealth = {
    ...dnsState.connection.relayHealth,
    [relay]: warnings.length > 0 ? `monitor-limited: ${warnings.join(',')}` : 'monitor-ok'
  };
  relayMonitorEvents.set(monitorKey, event);
  seenRelayMonitorEventIds.add(event.id);
  return true;
}

function startRelayMonitorSubscription() {
  if (unsubscribeRelayMonitors) {
    unsubscribeRelayMonitors();
    unsubscribeRelayMonitors = null;
  }
  const filters = relayMonitorFilters();
  if (filters.length === 0) return;
  unsubscribeRelayMonitors = nostr.subscribeWithRecovery(filters, {
    onEvent: (event) => {
      validateInboundNostrEvent(event)
        .then(() => applyRelayMonitorEvent(event))
        .catch((error) => {
          dnsState.connection.relayMonitorErrors = [
            { event_id: event?.id || '', pubkey: event?.pubkey || '', reason: error?.message || String(error) },
            ...dnsState.connection.relayMonitorErrors
          ].slice(0, 20);
        });
    },
    onClosed: (reason = '', relay = '') => {
      dnsState.connection.relayMonitorErrors = [
        { relay, reason: String(reason || 'closed'), source: 'closed' },
        ...dnsState.connection.relayMonitorErrors
      ].slice(0, 20);
    },
    onAuth: (challenge = '', relay = '') => {
      dnsState.connection.relayMonitorErrors = [
        { relay, reason: challenge || 'auth-required', source: 'auth' },
        ...dnsState.connection.relayMonitorErrors
      ].slice(0, 20);
    }
  });
}

function startDNSReadModelSubscription(since = null) {
  if (unsubscribeDNSReadModels) {
    unsubscribeDNSReadModels();
    unsubscribeDNSReadModels = null;
  }

  const eoseRelays = new Set();
  dnsState.connection.eoseRelays = [];
  setCollectionLoading(true);

  unsubscribeDNSReadModels = nostr.subscribeWithRecovery(dnsReadModelFilters(dnsState.connection.servicePubkey, { since }), {
    onEvent: (event) => {
      if (DNS_OPERATION_KINDS.includes(event?.kind)) {
        applyDNSOperationEvent(event);
      } else {
        applyDNSReadModelEvent(event);
      }
    },
    onEose: (relay) => {
      if (relay) eoseRelays.add(relay);
      dnsState.connection.eoseRelays = Array.from(eoseRelays);
      if (connectedRelayCount === 0 || eoseRelays.size >= connectedRelayCount) {
        setCollectionLoading(false);
        dnsState.connection.status = 'live';
        refreshAvailability();
        markCollectionLoaded();
      }
    },
    onHealth: (health) => Object.assign(dnsState.connection, health),
    onClosed: (reason = '', relay = '', meta = {}) => {
      const message = `DNS Nostr subscription closed${relay ? ` on ${relay}` : ''}: ${reason || 'closed'}`;
      dnsState.error.subscription = message;
      dnsState.connection.lastClosed = { reason: String(reason || ''), relay, terminal: meta?.terminal !== false, source: meta?.source || 'closed' };
      dnsState.connection.relayHealth = {
        ...dnsState.connection.relayHealth,
        ...(relay ? { [relay]: `closed: ${reason || 'closed'}` } : {})
      };
      if (meta?.terminal === false) {
        dnsState.connection.status = meta?.disconnected ? 'disconnected' : 'reconnecting';
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

export async function connect(relayUrl, pubkey, options = {}) {
  if (!browser) return { ok: false, reason: 'not_browser' };

  disconnect({ resetData: false });
  clearCollectionError();
  setCollectionLoading(true);
  dnsState.connection.status = 'connecting';

  try {
    const { relays, servicePubkey, trustedRelayMonitors } = await resolveSubscriptionConfig(relayUrl, pubkey, options);
    dnsState.connection.relays = relays;
    dnsState.connection.servicePubkey = servicePubkey;
    dnsState.connection.trustedRelayMonitors = trustedRelayMonitors;
    relayMonitorEvents.clear();
    seenRelayMonitorEventIds.clear();
    dnsState.connection.relayMonitorAnnouncements = {};
    dnsState.connection.relayMonitorMetadata = {};
    dnsState.connection.relayMonitorErrors = [];

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
    startRelayMonitorSubscription();
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
  if (unsubscribeRelayMonitors) {
    unsubscribeRelayMonitors();
    unsubscribeRelayMonitors = null;
  }
  connectedRelayCount = 0;
  dnsState.connection.connected = false;
  dnsState.connection.status = 'disconnected';
  dnsState.connection.eoseRelays = [];
  setCollectionLoading(false);
  refreshAvailability();

  if (resetData) {
    replaceableEvents.clear();
    seenEventIds.clear();
    zoneMap.clear();
    endpointMap.clear();
    policyMap.clear();
    backendMap.clear();
    relayMonitorEvents.clear();
    seenRelayMonitorEventIds.clear();
    dnsState.connection.relayMonitorAnnouncements = {};
    dnsState.connection.relayMonitorMetadata = {};
    dnsState.connection.relayMonitorErrors = [];
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
  refreshAvailability();
}

export function resetDnsReadModels() {
  disconnect({ resetData: true });
  clearCollectionError();
  dnsState.connection.status = 'idle';
  dnsState.connection.relays = [];
  dnsState.connection.servicePubkey = '';
  dnsState.connection.relayHealth = {};
  dnsState.connection.metadata = {};
  dnsState.connection.trustedRelayMonitors = [];
  dnsState.connection.relayMonitorAnnouncements = {};
  dnsState.connection.relayMonitorMetadata = {};
  dnsState.connection.relayMonitorErrors = [];
  relayMonitorEvents.clear();
  seenRelayMonitorEventIds.clear();
  dnsState.connection.lastEoseAt = null;
  dnsState.connection.resubscribeAttempts = 0;
  dnsState.connection.lastClosedReason = null;
  dnsState.connection.lastEventAt = null;
  dnsState.connection.lastClosed = null;
}

export function bootstrapDnsDashboard({ relays = null, servicePubkey = '', systemInfo = null, trustedRelayMonitorPubkeys = null } = {}) {
  const resolvedRelays = relays || systemInfo?.nostr?.browser_relays || systemInfo?.nostr?.sidecar_url || [];
  const resolvedPubkey = servicePubkey || systemInfo?.nostr?.service_pubkey || '';
  const resolvedMonitors = trustedRelayMonitorPubkeys || systemInfo?.nostr?.trusted_relay_monitor_pubkeys || systemInfo?.nostr?.relay_monitor_pubkeys || [];
  return connect(resolvedRelays, resolvedPubkey, { trustedRelayMonitorPubkeys: resolvedMonitors });
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
