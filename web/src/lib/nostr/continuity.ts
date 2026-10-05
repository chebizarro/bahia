import {
  BAHIA_STATE_SCHEMAS,
  CASCADIA_CONTROLPLANE_STATE,
  CONTINUITY_PROFILE,
  CONTINUITY_STATUS,
  FAILOVER_POLICY,
  FAILOVER_REQUEST,
  HEARTBEAT_OBSERVATION,
  RECOVERY_PROGRESS,
  RECOVERY_REQUEST,
  RECOVERY_WORKFLOW,
  REPLICATION_POLICY,
  STANDBY_NODE_DEFINITION,
  WORKER_STATE_TOPIC,
  dedupeReplaceableEvents,
  getDTag,
  getTagValue,
  getTagValues,
  parseJsonContent
} from '$lib/nostr/client.js';
import { controlStateSchema, workerRecordId } from '$lib/nostr/cp-state.js';
import { getEventStore, getPool, getRelayUrls, getServicePubkey, onStoreRefresh } from '$lib/nostr/boot.js';
import { toWebSocketUrl } from '$lib/nostr/pool-utils.js';
import { authState } from '$lib/stores/auth.js';
import { subscribeWithPagedBackfill } from '$lib/nostr/store-first-backfill.js';
import type { ContinuityAssessmentDTO, ContinuityRunDTO, ContinuityServiceStatusDTO } from '$lib/types/continuity';

const CONTINUITY_PAGE_SIZE = 500;
const CONTINUITY_STATUS_TAG = 'continuity';
const CONTINUITY_STATUS_READ_MODEL_TAG = 'continuity-status';
const WORKER_STATE_SCHEMA = BAHIA_STATE_SCHEMAS.WORKER_STATE;

export const CONTINUITY_READ_MODEL_KINDS = [CONTINUITY_STATUS, RECOVERY_PROGRESS];
export const CONTINUITY_DEFINITION_KINDS = [
  CONTINUITY_PROFILE,
  FAILOVER_POLICY,
  STANDBY_NODE_DEFINITION,
  REPLICATION_POLICY,
  RECOVERY_WORKFLOW
];
export const CONTINUITY_COMMAND_KINDS = [FAILOVER_REQUEST, RECOVERY_REQUEST];
export const CONTINUITY_TOPOLOGY_SOURCE_KINDS = [
  ...CONTINUITY_DEFINITION_KINDS,
  ...CONTINUITY_COMMAND_KINDS,
  HEARTBEAT_OBSERVATION,
  CASCADIA_CONTROLPLANE_STATE
];

export interface ContinuityNostrEvent {
  id?: string;
  kind?: number;
  pubkey?: string;
  created_at?: number;
  tags?: string[][];
  content?: string;
}

export interface ContinuityRequestDTO {
  id: string;
  request_type: 'failover' | 'recovery';
  service_key: string;
  worker_pubkey: string;
  reason: string;
  created_at: string;
  pubkey: string;
}

interface ContinuityProfileDefinition {
  serviceKey: string;
  primaryWorkerPubKey: string;
  profiles: Set<string>;
}

interface ContinuityRecipeDefinition {
  serviceKey: string;
  recipeKind: 'failover' | 'recovery' | string;
}

interface ContinuityStandbyDefinition {
  serviceKey: string;
  workerPubKey: string;
  profiles: Set<string>;
}

interface ContinuityTopologyState {
  statuses: ContinuityServiceStatusDTO[];
  profiles: Map<string, ContinuityProfileDefinition>;
  failoverRecipes: Set<string>;
  recoveryRecipes: Set<string>;
  standbys: ContinuityStandbyDefinition[];
  replicationPolicies: Set<string>;
  healthyWorkers: Set<string>;
}

function text(value: unknown): string {
  return String(value || '').trim();
}

function numberValue(value: unknown): number {
  const parsed = Number(value || 0);
  return Number.isFinite(parsed) ? parsed : 0;
}

function eventTagValues(event: ContinuityNostrEvent, name: string): string[] {
  return getTagValues(event, name).map(text).filter(Boolean);
}

function eventTagValue(event: ContinuityNostrEvent, name: string, fallback = ''): string {
  return text(getTagValue(event, name, fallback));
}

function contentObject(event: ContinuityNostrEvent): Record<string, any> {
  const parsed = parseJsonContent(event, {});
  return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
}

function newestFirst(left: ContinuityNostrEvent, right: ContinuityNostrEvent): number {
  const created = numberValue(right.created_at) - numberValue(left.created_at);
  if (created !== 0) return created;
  return String(right.id || '').localeCompare(String(left.id || ''));
}

// Service projections and cp-state are anchored to the deployment service key.
// Definitions/commands are operator-authored, so only the authenticated
// operator's own key is trusted here. Heartbeats are worker-authored and their
// keys must be named by a service-signed worker-state record.
export function continuityNostrFilters({ serviceAuthors = [], operatorAuthors = [], workerAuthors = [] }: {
  serviceAuthors?: string[]; operatorAuthors?: string[]; workerAuthors?: string[];
} = {}) {
  return [
    { kinds: [CONTINUITY_STATUS], '#t': [CONTINUITY_STATUS_TAG, CONTINUITY_STATUS_READ_MODEL_TAG], authors: serviceAuthors },
    { kinds: [RECOVERY_PROGRESS], '#t': [CONTINUITY_STATUS_TAG, 'recovery-progress'], authors: serviceAuthors },
    { kinds: CONTINUITY_DEFINITION_KINDS, authors: operatorAuthors },
    { kinds: CONTINUITY_COMMAND_KINDS, authors: operatorAuthors },
    { kinds: [HEARTBEAT_OBSERVATION], '#t': ['continuity-heartbeat'], authors: workerAuthors },
    { kinds: [CASCADIA_CONTROLPLANE_STATE], '#t': [WORKER_STATE_TOPIC], authors: serviceAuthors }
  ].filter((filter) => filter.authors.length > 0);
}

function trustedContinuityAuthors(store: any, service: string) {
  const workerStates = store.query({ kinds: [CASCADIA_CONTROLPLANE_STATE], authors: [service], '#t': [WORKER_STATE_TOPIC] });
  const workerAuthors: string[] = [...new Set<string>(workerStates.map((event: ContinuityNostrEvent) => {
    const content = contentObject(event);
    return text(content.worker_pubkey) || text(content.pubkey) || eventTagValue(event, 'worker') || workerRecordId(event, content);
  }).filter((key: string) => /^[0-9a-f]{64}$/.test(key)))];
  const operator = authState.status === 'authenticated' && /^[0-9a-f]{64}$/.test(authState.pubkey || '') ? authState.pubkey : '';
  return { serviceAuthors: [service], operatorAuthors: operator ? [operator] : [], workerAuthors };
}

export function continuityEventsFromStore(store = getEventStore(), service = getServicePubkey()): ContinuityNostrEvent[] {
  if (!store || !service) return [];
  const filters = continuityNostrFilters(trustedContinuityAuthors(store, service));
  const events = new Map<string, ContinuityNostrEvent>();
  for (const filter of filters) for (const event of store.query(filter)) if (event.id) events.set(event.id, event);
  return [...events.values()].sort(newestFirst);
}

let binding: { stop: () => void; sync: () => void; store: any } | null = null;
export function initContinuityStoreBinding() {
  const store = getEventStore();
  const pool = getPool();
  const service = getServicePubkey();
  if (!store || !pool || !service) return;
  if (binding?.store === store) { binding.sync(); return; }
  binding?.stop();
  const relays = [...new Set(getRelayUrls().map(toWebSocketUrl).filter(Boolean))];
  const active = new Map<string, () => void>();
  const sync = () => {
    const filters = continuityNostrFilters(trustedContinuityAuthors(store, service));
    const wanted = new Set<string>();
    filters.forEach((filter, index) => {
      const key = `continuity:${index}:${filter.authors.join(',')}`;
      wanted.add(key);
      if (active.has(key) || !relays.length) return;
      active.set(key, subscribeWithPagedBackfill({ pool, store, relays, filter, key, pageSize: CONTINUITY_PAGE_SIZE }));
    });
    for (const [key, stop] of active) if (!wanted.has(key)) { stop(); active.delete(key); }
  };
  sync();
  const offRefresh = onStoreRefresh(sync);
  binding = { store, sync, stop: () => { offRefresh(); for (const stop of active.values()) stop(); active.clear(); } };
}
export function teardownContinuityStoreBinding() {
  binding?.stop();
  binding = null;
}

export function continuityRequestsFromEvents(events: ContinuityNostrEvent[] = []): ContinuityRequestDTO[] {
  const seen = new Set<string>();
  return events
    .filter((event) => CONTINUITY_COMMAND_KINDS.includes(Number(event?.kind)))
    .filter((event) => {
      const id = text(event.id);
      if (!id || seen.has(id)) return false;
      seen.add(id);
      return true;
    })
    .map((event): ContinuityRequestDTO => {
      const content = contentObject(event);
      const requestType = event.kind === FAILOVER_REQUEST ? 'failover' : 'recovery';
      return {
        id: text(event.id),
        request_type: requestType,
        service_key: continuityEventServiceKey(event, content),
        worker_pubkey: text(content.worker_pubkey) || eventTagValue(event, 'worker') || eventTagValue(event, 'p'),
        reason: text(content.reason) || eventTagValue(event, 'reason'),
        created_at: eventCreatedAtISO(event),
        pubkey: text(event.pubkey)
      };
    })
    .sort((left, right) => right.created_at.localeCompare(left.created_at) || right.id.localeCompare(left.id));
}

function continuityDashboardSnapshot(events: ContinuityNostrEvent[], ready: boolean, error: string | null = null) {
  const statuses = continuityStatusesFromEvents(events);
  return {
    statuses,
    assessments: deriveContinuityAssessments(events, statuses),
    requests: continuityRequestsFromEvents(events),
    events,
    ready,
    error
  };
}

export async function subscribeToContinuityDashboard({ onUpdate }: {
  onUpdate?: (snapshot: ReturnType<typeof continuityDashboardSnapshot>) => void;
} = {}): Promise<() => void> {
  // Page listeners observe the app-lifetime store binding; navigation never
  // opens or closes a relay REQ.
  const publish = () => onUpdate?.(continuityDashboardSnapshot(continuityEventsFromStore(), true));
  publish();
  return onStoreRefresh(publish);
}

export function continuityStatusesFromEvents(events: ContinuityNostrEvent[] = []): ContinuityServiceStatusDTO[] {
  const statusEvents = dedupeReplaceableEvents(
    events.filter((event) => event?.kind === CONTINUITY_STATUS && continuityEventServiceKey(event))
  ) as ContinuityNostrEvent[];

  return statusEvents
    .map(parseContinuityStatusEvent)
    .filter((status): status is ContinuityServiceStatusDTO => Boolean(status))
    .sort((left, right) => left.service_key.localeCompare(right.service_key));
}

export function parseContinuityStatusEvent(event: ContinuityNostrEvent): ContinuityServiceStatusDTO | null {
  if (!event || event.kind !== CONTINUITY_STATUS) return null;
  const content = contentObject(event);
  const serviceKey = continuityEventServiceKey(event, content);
  if (!serviceKey) return null;

  const run = parseContinuityRun(event, content);
  return {
    service_key: serviceKey,
    active_profile: text(content.active_profile) || eventTagValue(event, 'profile') || 'offline',
    operation_state: text(content.operation_state) || eventTagValue(event, 'operation_state') || 'steady',
    primary_worker_pubkey: text(content.primary_worker_pubkey) || eventTagValue(event, 'primary_worker'),
    active_worker_pubkey: text(content.active_worker_pubkey) || eventTagValue(event, 'active_worker'),
    standby_worker_pubkey: text(content.standby_worker_pubkey) || eventTagValue(event, 'standby_worker') || undefined,
    reason: text(content.reason) || undefined,
    changed_at: text(content.changed_at) || eventCreatedAtISO(event),
    ...(run ? { current_run: run } : {})
  };
}

function parseContinuityRun(event: ContinuityNostrEvent, content: Record<string, any>): ContinuityRunDTO | null {
  const raw = content.current_run && typeof content.current_run === 'object' ? content.current_run : null;
  const id = text(raw?.id) || text(content.current_run_id) || eventTagValue(event, 'run');
  if (!id) return null;
  return {
    id,
    step_index: numberValue(raw?.step_index ?? content.current_step_index ?? eventTagValue(event, 'step')),
    step_count: numberValue(raw?.step_count ?? content.current_step_count ?? eventTagValue(event, 'step_count')),
    step_action: text(raw?.step_action) || text(content.current_step_action) || eventTagValue(event, 'action')
  };
}

function eventCreatedAtISO(event: ContinuityNostrEvent): string {
  const created = numberValue(event.created_at);
  if (created <= 0) return '';
  return new Date(created * 1000).toISOString();
}

function continuityEventServiceKey(event: ContinuityNostrEvent, content: Record<string, any> = contentObject(event)): string {
  return text(content.service_key) || eventTagValue(event, 'service') || serviceFromDTag(getDTag(event));
}

function serviceFromDTag(dTag: string): string {
  const value = text(dTag);
  for (const prefix of ['continuity-status:', 'recovery-progress:', 'continuity-profile:', 'failover-policy:', 'recovery-workflow:', 'standby-node:', 'replication-policy:']) {
    if (value.startsWith(prefix)) return value.slice(prefix.length).split(':')[0];
  }
  return '';
}

export function deriveContinuityAssessments(
  events: ContinuityNostrEvent[] = [],
  statusInput: ContinuityServiceStatusDTO[] = continuityStatusesFromEvents(events),
  failedWorkerPubKey = ''
): ContinuityAssessmentDTO[] {
  const state = buildTopologyState(events, statusInput);
  const failedWorker = text(failedWorkerPubKey);
  const serviceKeys = new Set<string>();

  for (const status of state.statuses) serviceKeys.add(status.service_key);
  for (const key of state.profiles.keys()) serviceKeys.add(key);
  for (const standby of state.standbys) serviceKeys.add(standby.serviceKey);
  for (const key of state.failoverRecipes) serviceKeys.add(key);
  for (const key of state.recoveryRecipes) serviceKeys.add(key);
  for (const key of state.replicationPolicies) serviceKeys.add(key);

  return [...serviceKeys]
    .filter(Boolean)
    .sort((left, right) => left.localeCompare(right))
    .map((serviceKey) => assessService(serviceKey, state, failedWorker));
}

export function simulateWorkerFailureFromEvents(
  workerPubKey: string,
  events: ContinuityNostrEvent[] = [],
  statuses: ContinuityServiceStatusDTO[] = continuityStatusesFromEvents(events)
): ContinuityAssessmentDTO[] {
  const failedWorker = text(workerPubKey);
  if (!failedWorker) throw new Error('worker_pubkey is required');
  return deriveContinuityAssessments(events, statuses, failedWorker);
}

function buildTopologyState(events: ContinuityNostrEvent[], statuses: ContinuityServiceStatusDTO[]): ContinuityTopologyState {
  const profiles = new Map<string, ContinuityProfileDefinition>();
  const failoverRecipes = new Set<string>();
  const recoveryRecipes = new Set<string>();
  const standbys = new Map<string, ContinuityStandbyDefinition>();
  const replicationPolicies = new Set<string>();
  const healthyWorkers = new Set<string>();

  const latestDefinitionEvents = dedupeReplaceableEvents(
    events.filter((event) => CONTINUITY_DEFINITION_KINDS.includes(Number(event?.kind)))
  ) as ContinuityNostrEvent[];

  for (const event of latestDefinitionEvents) {
    if (event.kind === CONTINUITY_PROFILE) {
      const profile = parseContinuityProfile(event);
      if (profile) profiles.set(profile.serviceKey, profile);
      continue;
    }
    if (event.kind === FAILOVER_POLICY || event.kind === RECOVERY_WORKFLOW) {
      const recipe = parseContinuityRecipe(event);
      if (recipe?.serviceKey) {
        if (event.kind === FAILOVER_POLICY || recipe.recipeKind === 'failover') failoverRecipes.add(recipe.serviceKey);
        if (event.kind === RECOVERY_WORKFLOW || recipe.recipeKind === 'recovery') recoveryRecipes.add(recipe.serviceKey);
      }
      continue;
    }
    if (event.kind === STANDBY_NODE_DEFINITION) {
      const standby = parseStandbyDefinition(event);
      if (standby) standbys.set(`${standby.serviceKey}:${standby.workerPubKey}`, standby);
      continue;
    }
    if (event.kind === REPLICATION_POLICY) {
      const serviceKey = continuityEventServiceKey(event);
      if (serviceKey) replicationPolicies.add(serviceKey);
    }
  }

  // Only each worker's newest live state counts: superseded records and
  // tombstones must not keep standby assignments alive.
  const latestWorkerStateEvents = dedupeReplaceableEvents(events.filter(isWorkerStateRecord)) as ContinuityNostrEvent[];
  for (const event of latestWorkerStateEvents) {
    for (const standby of standbyDefinitionsFromWorkerState(event)) {
      standbys.set(`${standby.serviceKey}:${standby.workerPubKey}`, standby);
    }
  }

  for (const event of events) {
    const healthyWorker = healthyHeartbeatWorker(event);
    if (healthyWorker) healthyWorkers.add(healthyWorker);
  }

  return { statuses, profiles, failoverRecipes, recoveryRecipes, standbys: [...standbys.values()], replicationPolicies, healthyWorkers };
}

function parseContinuityProfile(event: ContinuityNostrEvent): ContinuityProfileDefinition | null {
  const content = contentObject(event);
  const serviceKey = continuityEventServiceKey(event, content);
  if (!serviceKey) return null;
  const rawProfiles = content.profiles && typeof content.profiles === 'object' ? Object.keys(content.profiles) : eventTagValues(event, 'profile');
  return {
    serviceKey,
    primaryWorkerPubKey: text(content.primary_worker_pubkey) || eventTagValue(event, 'primary'),
    profiles: new Set(rawProfiles.map(text).filter(Boolean))
  };
}

function parseContinuityRecipe(event: ContinuityNostrEvent): ContinuityRecipeDefinition | null {
  const content = contentObject(event);
  const serviceKey = continuityEventServiceKey(event, content);
  if (!serviceKey) return null;
  return {
    serviceKey,
    recipeKind: text(content.kind) || eventTagValue(event, 'recipe-kind')
  };
}

function parseStandbyDefinition(event: ContinuityNostrEvent): ContinuityStandbyDefinition | null {
  const content = contentObject(event);
  const serviceKey = continuityEventServiceKey(event, content);
  const workerPubKey = text(content.worker_pubkey) || eventTagValue(event, 'worker') || eventTagValue(event, 'p');
  if (!serviceKey || !workerPubKey) return null;
  const contentProfiles = Array.isArray(content.profiles) ? content.profiles.map(text) : [];
  return {
    serviceKey,
    workerPubKey,
    profiles: new Set([...contentProfiles, ...eventTagValues(event, 'profile')].map(text).filter(Boolean))
  };
}

function isWorkerStateRecord(event: ContinuityNostrEvent): boolean {
  return event?.kind === CASCADIA_CONTROLPLANE_STATE && controlStateSchema(event, contentObject(event)) === WORKER_STATE_SCHEMA;
}

function standbyDefinitionsFromWorkerState(event: ContinuityNostrEvent): ContinuityStandbyDefinition[] {
  const content = contentObject(event);

  const workerPubKey = text(content.worker_pubkey) || text(content.pubkey) || eventTagValue(event, 'worker') || workerRecordId(event, content);
  if (!workerPubKey || !Array.isArray(content.standby_assignments)) return [];
  return content.standby_assignments
    .map((assignment: any) => {
      const serviceKey = text(assignment?.service_key);
      if (!serviceKey) return null;
      const profiles = Array.isArray(assignment?.supported_profiles) ? assignment.supported_profiles.map(text) : [];
      return { serviceKey, workerPubKey, profiles: new Set(profiles.filter(Boolean)) };
    })
    .filter((value: ContinuityStandbyDefinition | null): value is ContinuityStandbyDefinition => Boolean(value));
}

// A heartbeat is fresh until its NIP-40 expiration (seconds). Heartbeats from
// producers that predate NIP-40 carry expires_after_ms relative to created_at.
function heartbeatExpiresAtMs(event: ContinuityNostrEvent, content: Record<string, any>): number {
  const expiration = numberValue(eventTagValue(event, 'expiration'));
  if (expiration > 0) return expiration * 1000;
  const expiresAfterMs = numberValue(eventTagValue(event, 'expires_after_ms') || content.expires_after_ms);
  const createdAt = numberValue(event.created_at);
  if (expiresAfterMs > 0 && createdAt > 0) return createdAt * 1000 + expiresAfterMs;
  return Number.POSITIVE_INFINITY;
}

function healthyHeartbeatWorker(event: ContinuityNostrEvent): string {
  if (event?.kind !== HEARTBEAT_OBSERVATION) return '';
  const domain = eventTagValue(event, 'domain') || text(contentObject(event).domain);
  if (domain !== 'continuity') return '';
  const content = contentObject(event);
  const status = (eventTagValue(event, 'status') || text(content.status) || 'online').toLowerCase();
  if (!['online', 'fresh', 'healthy'].includes(status)) return '';

  if (Date.now() >= heartbeatExpiresAtMs(event, content)) return '';
  return eventTagValue(event, 'worker') || eventTagValue(event, 'p') || text(content.worker_pubkey) || text(event.pubkey);
}

function assessService(serviceKey: string, state: ContinuityTopologyState, failedWorker: string): ContinuityAssessmentDTO {
  const status = state.statuses.find((candidate) => candidate.service_key === serviceKey);
  const profile = state.profiles.get(serviceKey);
  const currentWorker = text(status?.active_worker_pubkey) || text(profile?.primaryWorkerPubKey) || text(status?.primary_worker_pubkey);
  const standbys = state.standbys.filter((standby) =>
    standby.serviceKey === serviceKey && standby.workerPubKey !== failedWorker && standby.workerPubKey !== currentWorker
  );
  const selectedStandby = standbys.find((standby) => state.healthyWorkers.has(standby.workerPubKey)) || standbys[0] || null;
  const selectedProfiles = selectedStandby ? [...selectedStandby.profiles] : [];
  const profileAllows = profile?.profiles?.size ? selectedProfiles.filter((mode) => profile.profiles.has(mode)) : selectedProfiles;
  const heartbeatActive = selectedStandby ? state.healthyWorkers.has(selectedStandby.workerPubKey) : false;
  const replicationConfigured = state.replicationPolicies.has(serviceKey);

  return {
    service_key: serviceKey,
    survivability: survivabilityFor(profileAllows, { hasStandby: Boolean(selectedStandby), heartbeatActive, replicationConfigured }),
    has_failover_recipe: state.failoverRecipes.has(serviceKey),
    has_recovery_recipe: state.recoveryRecipes.has(serviceKey),
    standby_count: standbys.length,
    replication_configured: replicationConfigured,
    heartbeat_active: heartbeatActive
  };
}

function survivabilityFor(
  profiles: string[],
  { hasStandby, heartbeatActive, replicationConfigured }: { hasStandby: boolean; heartbeatActive: boolean; replicationConfigured: boolean }
): string {
  if (!hasStandby || !heartbeatActive || !replicationConfigured) return 'unsatisfied';
  const normalized = new Set(profiles.map((profile) => profile.toLowerCase()));
  if (normalized.has('full')) return 'survivable';
  if (normalized.has('degraded')) return 'degraded_only';
  if (normalized.has('emergency')) return 'emergency_only';
  return 'unsatisfied';
}
