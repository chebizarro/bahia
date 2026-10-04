import { loadSystemInfo } from './system.svelte.js';
import { toWebSocketUrl } from '../nostr/pool-utils.js';
import { resolveBrowserRelays } from './discovery.svelte.js';
import {
  nostr,
  BAHIA_STATE_SCHEMAS,
  CASCADIA_CONTROLPLANE_STATE,
  DNS_ENDPOINT_TOPIC,
  WORKER_STATE_TOPIC,
  getDTag,
  getTagValue,
  parseJsonContent,
  upsertReplaceableEvent
} from '../nostr/client.js';
import { controlStateSchema, workerRecordId } from '../nostr/cp-state.js';

// Producer contract: DNS endpoints are canonical 30900 records (schema
// bahia.cp-state.v1, legacy_kind 31976, deleted=true|false, t=dns-endpoint)
// from the projector; worker state is the canonical 30900 record the control
// plane's worker-state publisher emits (schema bahia.cp-state.v1, legacy_kind
// 32000, deleted=true|false, t=worker-state, content = the full worker with
// fips_overlay_addr, fips_endpoints and mesh_health). controlStateSchema
// resolves both to their family schema.
const CAS_STATE_KIND = CASCADIA_CONTROLPLANE_STATE;
const DNS_ENDPOINT_SCHEMA = BAHIA_STATE_SCHEMAS.DNS_ENDPOINT_STATE;
const WORKER_STATE_SCHEMA = BAHIA_STATE_SCHEMAS.WORKER_STATE;
const READ_MODEL_LIMIT = 1000;
const MAX_HEALTHY_RTT_NS = 1_000_000_000;
const MAX_PROJECTABLE_RTT_NS = 5_000_000_000;
const MAX_HEALTHY_LOSS = 0.05;
const MAX_PROJECTABLE_LOSS = 0.5;

export const fipsMeshState = $state({
  status: 'idle',
  ready: false,
  bootstrapComplete: false,
  connected: false,
  loading: false,
  error: null,
  relays: [],
  servicePubkey: '',
  lastEoseAt: null,
  resubscribeAttempts: 0,
  lastClosedReason: null,
  lastEventAt: null,
  lastClosed: null,
  nodes: [],
  endpoints: []
});

export const meshNodes = fipsMeshState.nodes;
export const meshEndpoints = fipsMeshState.endpoints;

const endpointEvents = new Map();
const workerEvents = new Map();
const endpointMap = new Map();
const workerMap = new Map();
const seenEventIds = new Set();

let bootstrapPromise = null;
let liveUnsubscribe = null;
let connectedUnsubscribe = null;
let lastConnected = false;

function replaceArray(target, values) {
  target.length = 0;
  target.push(...values);
}

function authorFilter() {
  return fipsMeshState.servicePubkey ? { authors: [fipsMeshState.servicePubkey] } : {};
}

export function fipsMeshReadModelFilters() {
  const scopedAuthor = authorFilter();
  return [
    // #t is single-letter, so relays index it. Tombstones carry the same
    // topic as live records; mesh membership and the family are re-checked
    // locally per record.
    { kinds: [CAS_STATE_KIND], '#t': [DNS_ENDPOINT_TOPIC], limit: READ_MODEL_LIMIT, ...scopedAuthor },
    { kinds: [CAS_STATE_KIND], '#t': [WORKER_STATE_TOPIC], limit: READ_MODEL_LIMIT, ...scopedAuthor }
  ];
}

function tagValues(event, name) {
  return Array.isArray(event?.tags)
    ? event.tags.filter((tag) => Array.isArray(tag) && tag[0] === name && tag[1]).map((tag) => tag[1])
    : [];
}

function trimString(value) {
  return typeof value === 'string' ? value.trim() : '';
}

function asArray(value) {
  return Array.isArray(value) ? value.filter((item) => item !== null && item !== undefined) : [];
}

// Read models are only trusted from the configured Bahia service author; with
// no service pubkey configured nothing is accepted.
function isCanonicalAuthor(event) {
  return Boolean(fipsMeshState.servicePubkey) && event?.pubkey === fipsMeshState.servicePubkey;
}

function contentWithMeta(event) {
  const content = parseJsonContent(event, {});
  return {
    ...content,
    nostr_event_id: event.id,
    nostr_pubkey: event.pubkey,
    nostr_created_at: event.created_at,
    d: getDTag(event)
  };
}

function isDNSEndpointRecord(event, content) {
  return event?.kind === CAS_STATE_KIND && controlStateSchema(event, content) === DNS_ENDPOINT_SCHEMA;
}

function isWorkerStateRecord(event, content) {
  return event?.kind === CAS_STATE_KIND && controlStateSchema(event, content) === WORKER_STATE_SCHEMA;
}

// isMeshEndpointEvent reports whether a live DNS endpoint record is part of
// the FIPS mesh (tombstones carry no mesh markers and are handled by d).
export function isMeshEndpointEvent(event) {
  const content = parseJsonContent(event, {});
  if (!isDNSEndpointRecord(event, content)) return false;
  const family = trimString(getTagValue(event, 'family', content.family || '')).toLowerCase();
  const meshTags = tagValues(event, 'mesh').map((value) => value.toLowerCase());
  const source = trimString(content.source || getTagValue(event, 'source', '')).toLowerCase();
  const metadata = content.metadata && typeof content.metadata === 'object' ? content.metadata : {};
  const metadataMesh = trimString(metadata.mesh || metadata.overlay || metadata.network || '').toLowerCase();
  return family === 'mesh' || meshTags.includes('fips') || source.includes('fips') || metadataMesh === 'fips';
}

function endpointCoordinate(event) {
  return trimString(getDTag(event));
}

function workerPubkeyFromEndpoint(event, content) {
  return trimString(
    content.worker_pubkey ||
    content.workerPubkey ||
    getTagValue(event, 'worker', '') ||
    getTagValue(event, 'npub', '') ||
    content.npub ||
    ''
  );
}

function normalizeEndpoint(event) {
  const content = contentWithMeta(event);
  const coordinate = endpointCoordinate(event);
  if (!coordinate) return null;
  const workerPubkey = workerPubkeyFromEndpoint(event, content);
  const metadata = content.metadata && typeof content.metadata === 'object' ? content.metadata : {};
  const endpoint = {
    id: content.id || coordinate,
    coordinate,
    family: getTagValue(event, 'family', content.family || ''),
    source: content.source || getTagValue(event, 'source', ''),
    fqdn: content.fqdn || getTagValue(event, 'dns', ''),
    hostname: content.fqdn || getTagValue(event, 'dns', ''),
    name: content.name || '',
    zone: content.zone || '',
    address: content.address || getTagValue(event, 'addr', ''),
    port: content.port ?? getTagValue(event, 'port', ''),
    protocol: content.protocol || getTagValue(event, 'proto', ''),
    health: classifyHealth({ endpoint: content }),
    rawHealth: content.health || getTagValue(event, 'health', ''),
    driftStatus: content.drift_status || content.driftStatus || '',
    projectionStatus: metadata.projection_status || metadata.projectionStatus || content.projection_status || '',
    gatingReason: metadata.gating_reason || metadata.gatingReason || content.gating_reason || '',
    npub: workerPubkey,
    pubkey: workerPubkey,
    workerPubkey,
    capabilities: asArray(content.capabilities),
    metadata,
    materializedAt: content.materialized_at || content.materializedAt || '',
    nostrEventId: event.id,
    nostrPubkey: event.pubkey,
    nostrCreatedAt: event.created_at,
    event
  };
  endpoint.transportEndpoints = endpoint.protocol || endpoint.port
    ? [{ transport: endpoint.protocol || 'dns', address: endpoint.address, port: endpoint.port || null }]
    : [];
  return endpoint;
}

function durationToNanoseconds(value) {
  if (typeof value === 'number' && Number.isFinite(value)) return value;
  if (typeof value !== 'string') return 0;
  const trimmed = value.trim();
  if (/^\d+(\.\d+)?$/.test(trimmed)) return Number(trimmed);
  const match = trimmed.match(/^(\d+(?:\.\d+)?)(ns|us|µs|ms|s)$/);
  if (!match) return 0;
  const amount = Number(match[1]);
  switch (match[2]) {
    case 's': return amount * 1_000_000_000;
    case 'ms': return amount * 1_000_000;
    case 'us':
    case 'µs': return amount * 1_000;
    default: return amount;
  }
}

export function classifyHealth({ worker = null, endpoint = null } = {}) {
  const explicit = trimString(endpoint?.health || endpoint?.rawHealth || '').toLowerCase();
  if (['healthy', 'degraded', 'unhealthy', 'unknown'].includes(explicit)) return explicit;

  const status = trimString(worker?.status || '').toLowerCase();
  if (status === 'offline' || status === 'disabled') return 'unhealthy';

  const health = worker?.mesh_health || worker?.meshHealth || null;
  if (!health) {
    if (explicit === 'offline' || explicit === 'failed') return 'unhealthy';
    if (explicit === 'stale' || status === 'stale') return 'degraded';
    if (explicit === 'online' || status === 'online') return 'healthy';
    return endpoint?.address || worker?.fips_overlay_addr || worker?.fipsOverlayAddr ? 'healthy' : 'unknown';
  }

  const loss = Number(health.loss ?? 0);
  const rtt = durationToNanoseconds(health.rtt ?? health.RTT ?? 0);
  if (loss > MAX_PROJECTABLE_LOSS || rtt > MAX_PROJECTABLE_RTT_NS) return 'unhealthy';
  if (status === 'stale' || loss > MAX_HEALTHY_LOSS || rtt > MAX_HEALTHY_RTT_NS) return 'degraded';
  return 'healthy';
}

function normalizeWorker(event) {
  const content = contentWithMeta(event);
  const pubkey = trimString(content.pubkey || content.worker_pubkey || getTagValue(event, 'worker', '') || workerRecordId(event));
  if (!pubkey) return null;
  const fipsEndpoints = asArray(content.fips_endpoints || content.fipsEndpoints).map((endpoint) => ({
    transport: endpoint.transport || endpoint.Transport || '',
    address: endpoint.address || endpoint.Address || ''
  })).filter((endpoint) => endpoint.address);
  return {
    pubkey,
    npub: pubkey,
    name: content.name || pubkey.slice(0, 12),
    description: content.description || '',
    status: content.status || getTagValue(event, 'status', ''),
    schedulingState: content.scheduling_state || content.schedulingState || getTagValue(event, 'scheduling_state', ''),
    overlayAddress: content.fips_overlay_addr || content.fipsOverlayAddr || '',
    fipsOverlayAddr: content.fips_overlay_addr || content.fipsOverlayAddr || '',
    transportEndpoints: fipsEndpoints,
    fipsEndpoints,
    meshHealth: content.mesh_health || content.meshHealth || null,
    health: classifyHealth({ worker: content }),
    labels: content.labels || {},
    capabilities: content.capabilities || {},
    mlCapabilities: content.ml_capabilities || content.mlCapabilities || {},
    projectionStatus: content.projection_status || '',
    gatingReason: content.gating_reason || '',
    nostrEventId: event.id,
    nostrPubkey: event.pubkey,
    nostrCreatedAt: event.created_at,
    event
  };
}

function refreshCollections() {
  const endpoints = Array.from(endpointMap.values()).sort((a, b) => String(a.fqdn || a.coordinate).localeCompare(String(b.fqdn || b.coordinate)));
  const nodesByPubkey = new Map();

  for (const worker of workerMap.values()) {
    if (!worker.overlayAddress && worker.fipsEndpoints.length === 0 && worker.health === 'unknown') continue;
    // workerMap is keyed by coordinate: a worker can have its canonical
    // worker:state:<pubkey> record and a migrated per-event record. The newest
    // signed record describes the worker, whatever order they arrived in.
    const current = nodesByPubkey.get(worker.pubkey);
    if (current && Number(current.nostrCreatedAt || 0) >= Number(worker.nostrCreatedAt || 0)) continue;
    nodesByPubkey.set(worker.pubkey, {
      ...worker,
      endpoints: [],
      dnsHostnames: [],
      health: classifyHealth({ worker: { status: worker.status, mesh_health: worker.meshHealth, fips_overlay_addr: worker.overlayAddress } })
    });
  }

  for (const endpoint of endpoints) {
    const key = endpoint.workerPubkey || endpoint.pubkey || endpoint.coordinate;
    const existing = nodesByPubkey.get(key) || {
      pubkey: endpoint.workerPubkey || '',
      npub: endpoint.npub || endpoint.workerPubkey || '',
      name: endpoint.name || endpoint.fqdn || key,
      status: '',
      overlayAddress: endpoint.address || '',
      fipsOverlayAddr: endpoint.address || '',
      transportEndpoints: [],
      fipsEndpoints: [],
      meshHealth: null,
      endpoints: [],
      dnsHostnames: [],
      projectionStatus: endpoint.projectionStatus || '',
      gatingReason: endpoint.gatingReason || ''
    };
    existing.endpoints.push(endpoint);
    if (endpoint.fqdn && !existing.dnsHostnames.includes(endpoint.fqdn)) existing.dnsHostnames.push(endpoint.fqdn);
    if (!existing.overlayAddress && endpoint.address) {
      existing.overlayAddress = endpoint.address;
      existing.fipsOverlayAddr = endpoint.address;
    }
    if (endpoint.transportEndpoints.length > 0) existing.transportEndpoints.push(...endpoint.transportEndpoints);
    existing.health = classifyHealth({ worker: { status: existing.status, mesh_health: existing.meshHealth, fips_overlay_addr: existing.overlayAddress }, endpoint });
    if (!existing.projectionStatus) existing.projectionStatus = endpoint.projectionStatus || '';
    if (!existing.gatingReason) existing.gatingReason = endpoint.gatingReason || '';
    nodesByPubkey.set(key, existing);
  }

  replaceArray(meshEndpoints, endpoints);
  replaceArray(meshNodes, Array.from(nodesByPubkey.values()).sort((a, b) => String(a.name || a.pubkey).localeCompare(String(b.name || b.pubkey))));
}

// applyEndpointEvent keeps the newest record per (kind, pubkey, d), lowest id
// on a created_at tie. Tombstones and non-mesh records are retained too, so an
// older live record replayed later cannot resurrect the coordinate.
function applyEndpointEvent(event) {
  const coordinate = endpointCoordinate(event);
  if (!coordinate) return false;
  const result = upsertReplaceableEvent(endpointEvents, event);
  if (!result.accepted) return false;
  if (result.deleted || !isMeshEndpointEvent(event)) {
    const removed = endpointMap.delete(coordinate);
    if (removed) refreshCollections();
    return result.deleted || removed;
  }
  const endpoint = normalizeEndpoint(event);
  if (!endpoint) return false;
  endpointMap.set(coordinate, endpoint);
  refreshCollections();
  return true;
}

function applyWorkerEvent(event) {
  const d = getDTag(event);
  if (!d) return false;
  const result = upsertReplaceableEvent(workerEvents, event);
  if (!result.accepted) return false;
  if (result.deleted) {
    workerMap.delete(d);
    refreshCollections();
    return true;
  }
  const worker = normalizeWorker(event);
  if (!worker) return false;
  workerMap.set(d, worker);
  refreshCollections();
  return true;
}

export function applyFipsMeshEvent(event) {
  if (!event?.id || seenEventIds.has(event.id)) return false;
  if (!isCanonicalAuthor(event)) return false;
  const content = parseJsonContent(event, {});
  let accepted = false;
  if (isDNSEndpointRecord(event, content)) accepted = applyEndpointEvent(event);
  else if (isWorkerStateRecord(event, content)) accepted = applyWorkerEvent(event);
  else return false;
  seenEventIds.add(event.id);
  if (accepted) fipsMeshState.lastEventAt = Date.now();
  return accepted;
}

function subscribeToConnectionState() {
  if (connectedUnsubscribe) return;
  connectedUnsubscribe = nostr.connected.subscribe((connected) => {
    fipsMeshState.connected = connected;
    if (lastConnected && !connected && fipsMeshState.status === 'live') fipsMeshState.status = 'disconnected';
    if (!lastConnected && connected && fipsMeshState.ready) fipsMeshState.status = 'live';
    lastConnected = connected;
  });
}

function startSubscription() {
  if (liveUnsubscribe) liveUnsubscribe();
  liveUnsubscribe = nostr.subscribeWithRecovery(fipsMeshReadModelFilters(), {
    onEvent: (event) => applyFipsMeshEvent(event),
    onEose: () => {
      fipsMeshState.bootstrapComplete = true;
      fipsMeshState.ready = true;
      fipsMeshState.status = 'live';
    },
    onHealth: (health) => Object.assign(fipsMeshState, health),
    onClosed: (reason = '', relay = '', meta = {}) => {
      fipsMeshState.lastClosed = { reason: String(reason || ''), relay, terminal: meta?.terminal !== false, source: meta?.source || 'closed' };
      if (meta?.terminal === false) {
        fipsMeshState.status = meta?.disconnected ? 'disconnected' : 'reconnecting';
      } else if (fipsMeshState.status === 'live') {
        fipsMeshState.status = 'degraded';
      }
    }
  });
}

export function resetFipsMeshStore() {
  if (liveUnsubscribe) liveUnsubscribe();
  if (connectedUnsubscribe) connectedUnsubscribe();
  liveUnsubscribe = null;
  connectedUnsubscribe = null;
  bootstrapPromise = null;
  lastConnected = false;
  endpointEvents.clear();
  workerEvents.clear();
  endpointMap.clear();
  workerMap.clear();
  seenEventIds.clear();
  replaceArray(meshNodes, []);
  replaceArray(meshEndpoints, []);
  fipsMeshState.status = 'idle';
  fipsMeshState.ready = false;
  fipsMeshState.bootstrapComplete = false;
  fipsMeshState.connected = false;
  fipsMeshState.loading = false;
  fipsMeshState.error = null;
  fipsMeshState.relays = [];
  fipsMeshState.servicePubkey = '';
  fipsMeshState.lastEoseAt = null;
  fipsMeshState.resubscribeAttempts = 0;
  fipsMeshState.lastClosedReason = null;
  fipsMeshState.lastEventAt = null;
  fipsMeshState.lastClosed = null;
}

export async function bootstrapFipsMesh({ relays = null, servicePubkey = null, systemInfo = null } = {}) {
  if (bootstrapPromise) return bootstrapPromise;

  bootstrapPromise = (async () => {
    fipsMeshState.loading = true;
    fipsMeshState.error = null;
    try {
      fipsMeshState.status = 'discovering';
      const info = systemInfo || await loadSystemInfo();
      const resolvedRelays = Array.isArray(relays) && relays.length > 0 ? relays.map(toWebSocketUrl).filter(Boolean) : resolveBrowserRelays(info);
      const resolvedServicePubkey = servicePubkey || info?.nostr?.service_pubkey || '';
      fipsMeshState.relays = resolvedRelays;
      fipsMeshState.servicePubkey = resolvedServicePubkey;
      if (resolvedRelays.length === 0) throw new Error('No browser Nostr relays available for FIPS mesh read models');
      if (!resolvedServicePubkey) throw new Error('No Bahia service pubkey configured for FIPS mesh read models');

      subscribeToConnectionState();
      fipsMeshState.status = 'connecting';
      nostr.setRelays(resolvedRelays, false);
      await nostr.connect(resolvedRelays, { force: true });

      fipsMeshState.status = 'bootstrapping';
      startSubscription();
      return { ok: true, nodes: meshNodes, endpoints: meshEndpoints };
    } catch (error) {
      fipsMeshState.status = 'error';
      fipsMeshState.error = error?.message || 'FIPS mesh bootstrap failed';
      return { ok: false, reason: fipsMeshState.error };
    } finally {
      fipsMeshState.loading = false;
      bootstrapPromise = null;
    }
  })();

  return bootstrapPromise;
}

export function disconnectFipsMesh() {
  if (liveUnsubscribe) liveUnsubscribe();
  liveUnsubscribe = null;
  fipsMeshState.status = fipsMeshState.ready ? 'disconnected' : 'idle';
}
