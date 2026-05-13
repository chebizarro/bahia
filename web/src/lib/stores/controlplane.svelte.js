import { browser } from '$app/environment';
import { get } from 'svelte/store';
import { loadSystemInfo } from './system.svelte.js';
import {
  nostr,
  KINDS,
  BAHIA_READ_MODEL_KINDS,
  BAHIA_STATUS_KINDS,
  BAHIA_AUDIT_KINDS,
  getDTag,
  getTagValue,
  isReplaceableTombstone,
  parseJsonContent,
  upsertReplaceableEvent
} from '../nostr/client.js';

const MAX_ACTIVITY = 100;
const ACTIVITY_BACKFILL_LIMIT = 100;
const READ_MODEL_LIMIT = 1000;
const ACTIVITY_BACKFILL_SECONDS = 7 * 24 * 60 * 60;
const CANONICAL_READ_MODEL_KINDS = BAHIA_READ_MODEL_KINDS.filter((kind) => kind !== KINDS.LOOM_WORKER_AD);

export const controlplaneConnection = $state({
  status: 'idle', // idle | discovering | connecting | bootstrapping | live | error | disconnected
  connected: false,
  ready: false,
  bootstrapComplete: false,
  relays: [],
  servicePubkey: '',
  lastError: null,
  lastEoseAt: null,
  lastEventAt: null,
  reconnects: 0
});

export const services = $state([]);
export const environments = $state([]);
export const states = $state([]);
export const llmRoutes = $state([]);
export const llmRouteStates = $state([]);
export const artifacts = $state([]);
export const builds = $state([]);
export const deploymentIntents = $state([]);
export const deploymentRuns = $state([]);
export const policies = $state([]);
export const workers = $state([]);
export const events = $state([]);

export const loading = $state({
  services: false,
  environments: false,
  states: false,
  artifacts: false,
  builds: false,
  deploymentIntents: false,
  deploymentRuns: false,
  policies: false,
  workers: false
});

const replaceableEvents = new Map();
const seenEventIds = new Set();
const serviceMap = new Map();
const environmentMap = new Map();
const stateMap = new Map();
const llmRouteMap = new Map();
const llmRouteStateMap = new Map();
const artifactMap = new Map();
const buildMap = new Map();
const deploymentIntentMap = new Map();
const deploymentRunMap = new Map();
const policyMap = new Map();
const workerMap = new Map();
const activityMap = new Map();

let bootstrapPromise = null;
let liveUnsubscribe = null;
let connectedUnsubscribe = null;
let lastConnected = false;
let lastBootstrapFailedAt = null;
const BOOTSTRAP_RETRY_INTERVAL_MS = 30_000; // 30 s minimum between failed retries

function setAllLoading(value) {
  loading.services = value;
  loading.environments = value;
  loading.states = value;
  loading.workers = value;
}

function resetArrays() {
  services.length = 0;
  environments.length = 0;
  states.length = 0;
  llmRoutes.length = 0;
  llmRouteStates.length = 0;
  artifacts.length = 0;
  builds.length = 0;
  deploymentIntents.length = 0;
  deploymentRuns.length = 0;
  policies.length = 0;
  workers.length = 0;
  events.length = 0;
}

function replaceArray(target, values) {
  target.length = 0;
  target.push(...values);
}

function sortByNameOrId(a, b) {
  const left = String(a.name || a.id || a.pubkey || '');
  const right = String(b.name || b.id || b.pubkey || '');
  return left.localeCompare(right);
}

function refreshCollections() {
  replaceArray(services, Array.from(serviceMap.values()).sort(sortByNameOrId));
  replaceArray(environments, Array.from(environmentMap.values()).sort(sortByNameOrId));
  replaceArray(states, Array.from(stateMap.values()).sort(sortByNameOrId));
  replaceArray(llmRoutes, Array.from(llmRouteMap.values()).sort(sortByNameOrId));
  replaceArray(llmRouteStates, Array.from(llmRouteStateMap.values()).sort(sortByNameOrId));
  replaceArray(artifacts, Array.from(artifactMap.values()).sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || ''))));
  replaceArray(builds, Array.from(buildMap.values()).sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || ''))));
  replaceArray(deploymentIntents, Array.from(deploymentIntentMap.values()).sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || ''))));
  replaceArray(deploymentRuns, Array.from(deploymentRunMap.values()).sort((a, b) => String(b.created_at || '').localeCompare(String(a.created_at || ''))));
  replaceArray(policies, Array.from(policyMap.values()).sort(sortByNameOrId));
  replaceArray(workers, Array.from(workerMap.values()).sort(sortByNameOrId));
  replaceArray(
    events,
    Array.from(activityMap.values())
      .sort((a, b) => String(b.time || '').localeCompare(String(a.time || '')))
      .slice(0, MAX_ACTIVITY)
  );
}

export function upsertServiceProjection(service) {
  const id = service?.id;
  if (!id) return;

  if (service.deleted) {
    serviceMap.delete(id);
  } else {
    serviceMap.set(id, { ...(serviceMap.get(id) || {}), ...service, id });
  }

  refreshCollections();
}

export function resetControlplaneStore() {
  if (liveUnsubscribe) {
    liveUnsubscribe();
    liveUnsubscribe = null;
  }
  if (connectedUnsubscribe) {
    connectedUnsubscribe();
    connectedUnsubscribe = null;
  }
  bootstrapPromise = null;
  lastBootstrapFailedAt = null;
  replaceableEvents.clear();
  seenEventIds.clear();
  serviceMap.clear();
  environmentMap.clear();
  stateMap.clear();
  llmRouteMap.clear();
  llmRouteStateMap.clear();
  artifactMap.clear();
  buildMap.clear();
  deploymentIntentMap.clear();
  deploymentRunMap.clear();
  policyMap.clear();
  workerMap.clear();
  activityMap.clear();
  resetArrays();
  setAllLoading(false);
  controlplaneConnection.status = 'idle';
  controlplaneConnection.connected = false;
  controlplaneConnection.ready = false;
  controlplaneConnection.bootstrapComplete = false;
  controlplaneConnection.relays = [];
  controlplaneConnection.servicePubkey = '';
  controlplaneConnection.lastError = null;
  controlplaneConnection.lastEoseAt = null;
  controlplaneConnection.lastEventAt = null;
  controlplaneConnection.reconnects = 0;
  lastConnected = false;
}

function normalizeRelayUrl(url) {
  if (!url || typeof url !== 'string') return '';
  if (url.startsWith('ws://') || url.startsWith('wss://')) return url;
  if (url.startsWith('https://')) return `wss://${url.slice('https://'.length)}`;
  if (url.startsWith('http://')) return `ws://${url.slice('http://'.length)}`;
  return url;
}

export function resolveBrowserRelays(systemInfo) {
  const nostrInfo = systemInfo?.nostr || {};
  const relays = [];

  if (Array.isArray(nostrInfo.browser_relays)) {
    relays.push(...nostrInfo.browser_relays);
  }
  if (nostrInfo.sidecar_url) {
    relays.push(nostrInfo.sidecar_url);
  }

  return Array.from(new Set(relays.map(normalizeRelayUrl).filter(Boolean)));
}

function canonicalAuthorFilter() {
  const servicePubkey = controlplaneConnection.servicePubkey;
  return servicePubkey ? { authors: [servicePubkey] } : {};
}

function readModelFilters() {
  const authorFilter = canonicalAuthorFilter();
  return [
    { kinds: CANONICAL_READ_MODEL_KINDS, limit: READ_MODEL_LIMIT, ...authorFilter },
    { kinds: [KINDS.LOOM_WORKER_AD], limit: READ_MODEL_LIMIT },
    {
      kinds: [...BAHIA_AUDIT_KINDS, ...BAHIA_STATUS_KINDS],
      since: Math.floor(Date.now() / 1000) - ACTIVITY_BACKFILL_SECONDS,
      limit: ACTIVITY_BACKFILL_LIMIT,
      ...authorFilter
    }
  ];
}

function liveFilters(since) {
  const authorFilter = canonicalAuthorFilter();
  return [
    { kinds: CANONICAL_READ_MODEL_KINDS, since, ...authorFilter },
    { kinds: [KINDS.LOOM_WORKER_AD], since },
    { kinds: [...BAHIA_AUDIT_KINDS, ...BAHIA_STATUS_KINDS], since, ...authorFilter }
  ];
}

function isCanonicalBahiaKind(kind) {
  return CANONICAL_READ_MODEL_KINDS.includes(kind) ||
    BAHIA_AUDIT_KINDS.includes(kind) ||
    BAHIA_STATUS_KINDS.includes(kind);
}

function shouldAcceptControlplaneEvent(event) {
  const servicePubkey = controlplaneConnection.servicePubkey;
  if (!servicePubkey || !isCanonicalBahiaKind(event.kind)) return true;
  return event.pubkey === servicePubkey;
}

function contentWithEventMeta(event) {
  const content = parseJsonContent(event, {});
  return {
    ...content,
    nostr_event_id: event.id,
    nostr_pubkey: event.pubkey,
    nostr_created_at: event.created_at
  };
}

function applyServiceEvent(event) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const id = content.id || getDTag(event);
  if (!id) return false;

  if (isReplaceableTombstone(event)) {
    serviceMap.delete(id);
  } else {
    serviceMap.set(id, { ...content, id });
  }
  return true;
}

function applyEnvironmentEvent(event) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const id = content.id || getDTag(event);
  if (!id) return false;

  if (isReplaceableTombstone(event)) {
    environmentMap.delete(id);
  } else {
    environmentMap.set(id, { ...content, id });
  }
  return true;
}

function applyStateEvent(event) {
  const { accepted, key } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const dTag = getDTag(event);
  const serviceID = content.service_id || getTagValue(event, 'service');
  const environmentID = content.environment_id || getTagValue(event, 'environment');
  const id = content.id || dTag || (serviceID && environmentID ? `${serviceID}:${environmentID}` : key);
  if (!id) return false;

  if (isReplaceableTombstone(event)) {
    stateMap.delete(id);
  } else {
    stateMap.set(id, {
      ...content,
      id,
      service_id: serviceID,
      environment_id: environmentID
    });
  }
  return true;
}

function applyLLMRouteEvent(event) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const id = content.id || content.route_id || getTagValue(event, 'route') || getDTag(event);
  if (!id) return false;

  if (isReplaceableTombstone(event)) {
    llmRouteMap.delete(id);
  } else {
    llmRouteMap.set(id, { ...content, id, route_id: id });
  }
  return true;
}

function applyLLMRouteStateEvent(event) {
  const { accepted, key } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const dTag = getDTag(event);
  const routeID = content.route_id || getTagValue(event, 'route');
  const environmentID = content.environment_id || getTagValue(event, 'environment');
  const id = content.id || dTag || (routeID && environmentID ? `${routeID}:${environmentID}` : key);
  if (!id) return false;

  if (isReplaceableTombstone(event)) {
    llmRouteStateMap.delete(id);
  } else {
    llmRouteStateMap.set(id, {
      ...content,
      id,
      route_id: routeID,
      environment_id: environmentID
    });
  }
  return true;
}

function applyProjectedEntity(event, targetMap, idKeys = ['id']) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  let id = getDTag(event);
  for (const key of idKeys) {
    if (content[key]) {
      id = content[key];
      break;
    }
  }
  if (!id) return false;

  if (isReplaceableTombstone(event) || content.deleted === true) {
    targetMap.delete(id);
  } else {
    targetMap.set(id, { ...content, id });
  }
  return true;
}

function applyWorkerEvent(event) {
  const { accepted } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const pubkey = event.pubkey;
  if (!pubkey) return false;

  if (isReplaceableTombstone(event)) {
    workerMap.delete(pubkey);
  } else {
    workerMap.set(pubkey, {
      ...content,
      pubkey,
      status: content.status || 'online',
      last_advertisement_at: new Date((event.created_at || 0) * 1000).toISOString()
    });
  }
  return true;
}

function activityType(event, content) {
  if (content.event_type) return content.event_type;
  if (event.kind === KINDS.BAHIA_LLM_DEPLOYMENT_STATUS) return 'llm_deployment.status';
  if (event.kind === KINDS.BAHIA_DEPLOYMENT_STATUS || event.kind === KINDS.BAHIA_SERVICE_STATUS) return 'controlplane.status';
  if (event.kind === KINDS.BAHIA_LLM_ROUTE_CREATE_RESULT || event.kind === KINDS.BAHIA_LLM_RELEASE_REGISTER_RESULT || event.kind === KINDS.BAHIA_LLM_DEPLOYMENT_RESULT) return 'llm_deployment.result';
  if (BAHIA_STATUS_KINDS.includes(event.kind)) return 'controlplane.result';
  return `nostr.kind.${event.kind}`;
}

function activityEntityId(event, content) {
  return content.entity_id ||
    content.service_id ||
    content.route_id ||
    content.release_id ||
    getTagValue(event, 'service') ||
    getTagValue(event, 'route') ||
    getTagValue(event, 'release') ||
    getTagValue(event, 'environment') ||
    getTagValue(event, 'intent') ||
    getTagValue(event, 'run') ||
    getTagValue(event, 'artifact') ||
    getDTag(event) ||
    event.id;
}

function applyActivityEvent(event) {
  if (!event?.id || activityMap.has(event.id)) return false;
  const content = parseJsonContent(event, {});
  const time = new Date((event.created_at || 0) * 1000).toISOString();
  activityMap.set(event.id, {
    id: event.id,
    kind: event.kind,
    type: activityType(event, content),
    entity_id: activityEntityId(event, content),
    data: content.data ?? content,
    time,
    pubkey: event.pubkey,
    nostr_event: event
  });

  if (activityMap.size > MAX_ACTIVITY * 2) {
    const trimmed = Array.from(activityMap.values())
      .sort((a, b) => String(b.time || '').localeCompare(String(a.time || '')))
      .slice(0, MAX_ACTIVITY);
    activityMap.clear();
    for (const activity of trimmed) {
      activityMap.set(activity.id, activity);
    }
  }
  return true;
}

export function applyControlplaneEvent(event) {
  if (!event?.id || typeof event.kind !== 'number') return false;
  if (!shouldAcceptControlplaneEvent(event)) return false;
  if (seenEventIds.has(event.id)) return false;
  seenEventIds.add(event.id);

  let changed = false;
  switch (event.kind) {
    case KINDS.BAHIA_SERVICE_REGISTRY:
      changed = applyServiceEvent(event);
      break;
    case KINDS.BAHIA_ENVIRONMENT_REGISTRY:
      changed = applyEnvironmentEvent(event);
      break;
    case KINDS.BAHIA_SERVICE_STATE:
      changed = applyStateEvent(event);
      break;
    case KINDS.BAHIA_LLM_ROUTE_REGISTRY:
      changed = applyLLMRouteEvent(event);
      break;
    case KINDS.BAHIA_LLM_ROUTE_STATE:
      changed = applyLLMRouteStateEvent(event);
      break;
    case KINDS.BAHIA_ARTIFACT_REGISTRY:
      changed = applyProjectedEntity(event, artifactMap, ['id', 'artifact_id']);
      break;
    case KINDS.BAHIA_BUILD_REGISTRY:
      changed = applyProjectedEntity(event, buildMap, ['id', 'build_id']);
      break;
    case KINDS.BAHIA_DEPLOYMENT_INTENT_REGISTRY:
      changed = applyProjectedEntity(event, deploymentIntentMap, ['id', 'intent_id']);
      break;
    case KINDS.BAHIA_DEPLOYMENT_RUN_REGISTRY:
      changed = applyProjectedEntity(event, deploymentRunMap, ['id', 'run_id']);
      break;
    case KINDS.BAHIA_POLICY_REGISTRY:
      changed = applyProjectedEntity(event, policyMap, ['id', 'policy_id']);
      break;
    case KINDS.LOOM_WORKER_AD:
      changed = applyWorkerEvent(event);
      break;
    default:
      changed = applyActivityEvent(event);
      break;
  }

  if (changed) {
    controlplaneConnection.lastEventAt = new Date().toISOString();
    refreshCollections();
  }
  return changed;
}

function subscribeToConnectionState() {
  if (connectedUnsubscribe) return;
  connectedUnsubscribe = nostr.connected.subscribe((connected) => {
    controlplaneConnection.connected = connected;
    if (lastConnected && !connected && controlplaneConnection.status === 'live') {
      controlplaneConnection.status = 'disconnected';
    }
    if (!lastConnected && connected && controlplaneConnection.bootstrapComplete) {
      controlplaneConnection.reconnects += 1;
      controlplaneConnection.status = 'live';
    }
    lastConnected = connected;
  });
}

function startLiveSubscription(since) {
  if (liveUnsubscribe) {
    liveUnsubscribe();
    liveUnsubscribe = null;
  }

  liveUnsubscribe = nostr.subscribe(liveFilters(since), {
    onEvent: (event) => applyControlplaneEvent(event),
    onClosed: (reason, relay) => {
      controlplaneConnection.lastError = reason || `subscription closed by ${relay}`;
      if (controlplaneConnection.status === 'live') {
        controlplaneConnection.status = 'disconnected';
      }
    }
  });
}

export async function bootstrapControlplane({ force = false } = {}) {
  if (!browser) return { ok: false, reason: 'not_browser' };
  if (bootstrapPromise && !force) return bootstrapPromise;
  if (controlplaneConnection.ready && !force) return { ok: true };

  // Rate-limit retries after failure — prevents hammering relays when config is broken
  if (!force && lastBootstrapFailedAt !== null) {
    const elapsed = Date.now() - lastBootstrapFailedAt;
    if (elapsed < BOOTSTRAP_RETRY_INTERVAL_MS) {
      return { ok: false, reason: controlplaneConnection.lastError || 'bootstrap failed recently, waiting before retry' };
    }
  }
  if (force) lastBootstrapFailedAt = null;

  bootstrapPromise = (async () => {
    const bootstrapSince = Math.floor(Date.now() / 1000);
    controlplaneConnection.status = 'discovering';
    controlplaneConnection.lastError = null;
    setAllLoading(true);

    try {
      const systemInfo = await loadSystemInfo({ force });
      const relays = resolveBrowserRelays(systemInfo);
      controlplaneConnection.relays = relays;
      controlplaneConnection.servicePubkey = systemInfo?.nostr?.service_pubkey || '';

      if (!systemInfo?.features?.relay_read_models) {
        throw new Error('Relay read models are not advertised by Nostr system discovery');
      }

      if (relays.length === 0) {
        throw new Error('No browser Nostr relays advertised by Nostr system discovery');
      }

      controlplaneConnection.status = 'connecting';
      subscribeToConnectionState();
      nostr.setRelays(relays, false);
      await nostr.connect(relays);

      if (!get(nostr.connected)) {
        throw new Error('Unable to connect to any advertised browser relay');
      }

      controlplaneConnection.status = 'bootstrapping';
      const initialEvents = await nostr.queryUntilEose(readModelFilters());
      for (const event of initialEvents) {
        applyControlplaneEvent(event);
      }

      controlplaneConnection.ready = true;
      controlplaneConnection.bootstrapComplete = true;
      controlplaneConnection.lastEoseAt = new Date().toISOString();
      controlplaneConnection.status = 'live';
      startLiveSubscription(bootstrapSince);
      refreshCollections();
      return { ok: true };
    } catch (err) {
      lastBootstrapFailedAt = Date.now(); // rate-limit subsequent retries
      controlplaneConnection.status = 'error';
      controlplaneConnection.ready = false;
      controlplaneConnection.bootstrapComplete = false;
      controlplaneConnection.lastError = err?.message || String(err);
      return {
        ok: false,
        reason: controlplaneConnection.lastError
      };
    } finally {
      setAllLoading(false);
      bootstrapPromise = null;
    }
  })();

  return bootstrapPromise;
}

export function disconnectControlplane() {
  if (liveUnsubscribe) {
    liveUnsubscribe();
    liveUnsubscribe = null;
  }
  controlplaneConnection.status = controlplaneConnection.ready ? 'disconnected' : 'idle';
}
