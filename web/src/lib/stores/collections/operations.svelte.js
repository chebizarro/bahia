import {
  ACTION_RESULT,
  ACTION_STATUS,
  ADOPTION_IMPORT_RESULT,
  ADOPTION_SCAN_RESULT,
  ADOPTION_STATUS,
  BACKUP_OBSERVATION,
  BACKUP_DEFINITION_APPLY_RESULT,
  BACKUP_POLICY_APPLY_RESULT,
  BACKUP_RECIPE_APPLY_RESULT,
  BACKUP_REPOSITORY_PROBE_RESULT,
  BACKUP_REPOSITORY_REGISTER_RESULT,
  BACKUP_RESTORE_APPROVAL_RESULT,
  BACKUP_RESTORE_RESULT,
  BACKUP_RESTORE_STATUS,
  BACKUP_RETENTION_RESULT,
  BACKUP_RUN_RESULT,
  BACKUP_RUN_STATUS,
  BACKUP_VERIFICATION_RESULT,
  BACKUP_VERIFICATION_STATUS,
  DEPLOYMENT_RESULT,
  DEPLOYMENT_STATUS,
  DNS_BACKEND_REGISTER_RESULT,
  DNS_DRIFT_REMEDIATE_RESULT,
  DNS_OPERATION_STATUS,
  DNS_POLICY_APPLY_RESULT,
  DNS_RECORD_OVERRIDE_RESULT,
  DNS_ZONE_CREATE_RESULT,
  ENVIRONMENT_CREATE_RESULT,
  HIVE_CI_WORKFLOW_RESULT,
  HIVE_CI_WORKFLOW_RUN,
  LLM_DEPLOYMENT_RESULT,
  LLM_DEPLOYMENT_STATUS,
  LLM_RELEASE_REGISTER_RESULT,
  LLM_ROUTE_CREATE_RESULT,
  ML_INFERENCE_APPROVAL_RESULT,
  ML_INFERENCE_DEPLOYMENT_APPROVAL,
  ML_INFERENCE_DEPLOY_REQUEST,
  ML_INFERENCE_DEPLOY_RESULT,
  ML_INFERENCE_ROLLBACK_REQUEST,
  ML_INFERENCE_ROLLBACK_RESULT,
  ML_MODEL_IMPORT_REQUEST,
  ML_MODEL_IMPORT_RESULT,
  ML_RECIPE_RUN_REQUEST,
  ML_RECIPE_RUN_RESULT,
  OBSERVATION_RESULT,
  PACKAGE_DRIFT_EVENT,
  PACKAGE_RESULT,
  PACKAGE_STATUS,
  REMEDIATION_RESULT,
  SERVICE_CREATE_RESULT,
  SERVICE_STATUS,
  SOUL_FACTORY_PROVISIONING_RESULT,
  SOUL_FACTORY_PROVISIONING_STATUS,
  TOOL_APPROVAL_RESPONSE,
  TOOL_PROVISION_RESULT,
  TOOL_PROVISION_STATUS,
  WORKER_RESULT,
  WORKER_STATUS,
  getTagValue,
  parseJsonContent
} from '../../nostr/client.js';
import { replaceArray, sortByNewestField } from './utils.js';
import { getEventStore, getPool, getRelayUrls, getServicePubkey } from '../../nostr/boot.js';
import { toWebSocketUrl } from '../../nostr/pool-utils.js';

export const OPERATION_STATUS_KINDS = Object.freeze([
  DNS_OPERATION_STATUS,
  SOUL_FACTORY_PROVISIONING_STATUS,
  DEPLOYMENT_STATUS,
  SERVICE_STATUS,
  ACTION_STATUS,
  LLM_DEPLOYMENT_STATUS,
  TOOL_PROVISION_STATUS,
  ADOPTION_STATUS,
  BACKUP_RUN_STATUS,
  BACKUP_RESTORE_STATUS,
  BACKUP_VERIFICATION_STATUS,
  BACKUP_OBSERVATION,
  PACKAGE_STATUS,
  WORKER_STATUS
]);

// These two legacy reply slots are part of the documented 7971-7979
// subscription range but do not have generated symbolic names.
const LLM_DEPLOYMENT_APPROVAL_RESULT = 7974;
const LLM_ROLLBACK_RESULT = 7975;

export const OPERATION_REQUEST_KINDS = Object.freeze([
  ML_RECIPE_RUN_REQUEST,
  ML_INFERENCE_DEPLOY_REQUEST,
  ML_INFERENCE_DEPLOYMENT_APPROVAL,
  ML_INFERENCE_ROLLBACK_REQUEST,
  ML_MODEL_IMPORT_REQUEST
]);

export const OPERATION_RESULT_KINDS = Object.freeze([
  DNS_ZONE_CREATE_RESULT,
  DNS_POLICY_APPLY_RESULT,
  DNS_RECORD_OVERRIDE_RESULT,
  DNS_DRIFT_REMEDIATE_RESULT,
  DNS_BACKEND_REGISTER_RESULT,
  SOUL_FACTORY_PROVISIONING_RESULT,
  DEPLOYMENT_RESULT,
  ACTION_RESULT,
  SERVICE_CREATE_RESULT,
  ENVIRONMENT_CREATE_RESULT,
  OBSERVATION_RESULT,
  REMEDIATION_RESULT,
  LLM_ROUTE_CREATE_RESULT,
  LLM_RELEASE_REGISTER_RESULT,
  LLM_DEPLOYMENT_RESULT,
  LLM_DEPLOYMENT_APPROVAL_RESULT,
  LLM_ROLLBACK_RESULT,
  ML_RECIPE_RUN_RESULT,
  ML_INFERENCE_DEPLOY_RESULT,
  ML_INFERENCE_APPROVAL_RESULT,
  ML_INFERENCE_ROLLBACK_RESULT,
  ML_MODEL_IMPORT_RESULT,
  BACKUP_RUN_RESULT,
  BACKUP_VERIFICATION_RESULT,
  BACKUP_RESTORE_RESULT,
  BACKUP_RESTORE_APPROVAL_RESULT,
  BACKUP_RETENTION_RESULT,
  BACKUP_REPOSITORY_REGISTER_RESULT,
  BACKUP_POLICY_APPLY_RESULT,
  BACKUP_RECIPE_APPLY_RESULT,
  BACKUP_DEFINITION_APPLY_RESULT,
  BACKUP_REPOSITORY_PROBE_RESULT,
  TOOL_PROVISION_RESULT,
  TOOL_APPROVAL_RESPONSE,
  ADOPTION_SCAN_RESULT,
  ADOPTION_IMPORT_RESULT,
  PACKAGE_RESULT,
  PACKAGE_DRIFT_EVENT,
  WORKER_RESULT
]);

export const HIVE_CI_OPERATION_KINDS = Object.freeze([
  HIVE_CI_WORKFLOW_RUN,
  HIVE_CI_WORKFLOW_RESULT
]);

export const CANONICAL_OPERATION_KINDS = Object.freeze([
  ...OPERATION_REQUEST_KINDS,
  ...OPERATION_STATUS_KINDS.filter((kind) => kind !== SOUL_FACTORY_PROVISIONING_STATUS),
  ...OPERATION_RESULT_KINDS.filter((kind) => kind !== SOUL_FACTORY_PROVISIONING_RESULT)
]);

export const EXTERNAL_OPERATION_KINDS = Object.freeze([
  SOUL_FACTORY_PROVISIONING_STATUS,
  SOUL_FACTORY_PROVISIONING_RESULT,
  ...HIVE_CI_OPERATION_KINDS
]);

export const operations = $state([]);

const operationMap = new Map();
const pendingHiveResultMap = new Map();
const operationEvents = new Map();
const eventOperation = new Map();
const coordinateEvents = new Map();
let boundStore = null;
let unsubscribers = [];
let relayHandles = [];
let renderQueued = false;

const KIND_DOMAINS = new Map([
  [DNS_OPERATION_STATUS, 'dns'],
  [DNS_ZONE_CREATE_RESULT, 'dns'],
  [DNS_POLICY_APPLY_RESULT, 'dns'],
  [DNS_RECORD_OVERRIDE_RESULT, 'dns'],
  [DNS_DRIFT_REMEDIATE_RESULT, 'dns'],
  [DNS_BACKEND_REGISTER_RESULT, 'dns'],
  [SOUL_FACTORY_PROVISIONING_STATUS, 'soulfactory'],
  [SOUL_FACTORY_PROVISIONING_RESULT, 'soulfactory'],
  [DEPLOYMENT_STATUS, 'deployment'],
  [DEPLOYMENT_RESULT, 'deployment'],
  [SERVICE_STATUS, 'service'],
  [SERVICE_CREATE_RESULT, 'service'],
  [ENVIRONMENT_CREATE_RESULT, 'environment'],
  [ACTION_STATUS, 'action'],
  [ACTION_RESULT, 'action'],
  [OBSERVATION_RESULT, 'observation'],
  [REMEDIATION_RESULT, 'remediation'],
  [LLM_DEPLOYMENT_STATUS, 'llm'],
  [LLM_ROUTE_CREATE_RESULT, 'llm'],
  [LLM_RELEASE_REGISTER_RESULT, 'llm'],
  [LLM_DEPLOYMENT_RESULT, 'llm'],
  [LLM_DEPLOYMENT_APPROVAL_RESULT, 'llm'],
  [LLM_ROLLBACK_RESULT, 'llm'],
  ...OPERATION_REQUEST_KINDS.map((kind) => [kind, 'ml']),
  [ML_RECIPE_RUN_RESULT, 'ml'],
  [ML_INFERENCE_DEPLOY_RESULT, 'ml'],
  [ML_INFERENCE_APPROVAL_RESULT, 'ml'],
  [ML_INFERENCE_ROLLBACK_RESULT, 'ml'],
  [ML_MODEL_IMPORT_RESULT, 'ml'],
  [TOOL_PROVISION_STATUS, 'tool'],
  [TOOL_PROVISION_RESULT, 'tool'],
  [TOOL_APPROVAL_RESPONSE, 'tool'],
  [ADOPTION_STATUS, 'adoption'],
  [ADOPTION_SCAN_RESULT, 'adoption'],
  [ADOPTION_IMPORT_RESULT, 'adoption'],
  [BACKUP_RUN_STATUS, 'backup'],
  [BACKUP_RESTORE_STATUS, 'backup'],
  [BACKUP_VERIFICATION_STATUS, 'backup'],
  [BACKUP_OBSERVATION, 'backup'],
  [BACKUP_RUN_RESULT, 'backup'],
  [BACKUP_VERIFICATION_RESULT, 'backup'],
  [BACKUP_RESTORE_RESULT, 'backup'],
  [BACKUP_RESTORE_APPROVAL_RESULT, 'backup'],
  [BACKUP_RETENTION_RESULT, 'backup'],
  [BACKUP_REPOSITORY_REGISTER_RESULT, 'backup'],
  [BACKUP_POLICY_APPLY_RESULT, 'backup'],
  [BACKUP_RECIPE_APPLY_RESULT, 'backup'],
  [BACKUP_DEFINITION_APPLY_RESULT, 'backup'],
  [BACKUP_REPOSITORY_PROBE_RESULT, 'backup'],
  [PACKAGE_STATUS, 'package'],
  [PACKAGE_RESULT, 'package'],
  [PACKAGE_DRIFT_EVENT, 'package'],
  [WORKER_STATUS, 'worker'],
  [WORKER_RESULT, 'worker'],
  [HIVE_CI_WORKFLOW_RUN, 'hive-ci'],
  [HIVE_CI_WORKFLOW_RESULT, 'hive-ci']
]);

const ENTITY_TAG_ALIASES = Object.freeze({
  service: 'service_id',
  environment: 'environment_id',
  artifact: 'artifact_id',
  intent: 'intent_id',
  deployment: 'deployment_id',
  worker: 'worker_pubkey',
  repository: 'repository_id',
  package: 'package_id',
  route: 'route_id',
  model: 'model_id',
  endpoint: 'endpoint_id',
  backend: 'backend_id',
  run: 'run_id',
  backup_run: 'backup_run_id',
  restore: 'restore_id',
  verification: 'verification_id',
  policy: 'policy_id',
  agent: 'agent_id',
  zone: 'zone',
  target: 'target',
  a: 'repo_coordinate',
  commit: 'commit_sha',
  branch: 'branch',
  workflow: 'workflow_path'
});

const ENTITY_PRIORITY = Object.freeze([
  'intent_id',
  'deployment_id',
  'worker_pubkey',
  'backup_run_id',
  'restore_id',
  'verification_id',
  'service_id',
  'environment_id',
  'repository_id',
  'package_id',
  'route_id',
  'model_id',
  'endpoint_id',
  'backend_id',
  'agent_id',
  'zone',
  'target',
  'repo_coordinate'
]);

export const TERMINAL_OPERATION_STATUSES = Object.freeze([
  'success',
  'succeeded',
  'completed',
  'complete',
  'failure',
  'failed',
  'error',
  'rejected',
  'cancelled',
  'canceled',
  'timeout',
  'timed_out'
]);

export function isTerminalOperationStatus(status) {
  return TERMINAL_OPERATION_STATUSES.includes(String(status || '').toLowerCase());
}

export function resetOperations() {
  operationMap.clear();
  pendingHiveResultMap.clear();
  operationEvents.clear();
  eventOperation.clear();
  coordinateEvents.clear();
  renderQueued = false;
  operations.length = 0;
}

export function refreshOperations() {
  if (!renderQueued) return;
  renderQueued = false;
  replaceArray(
    operations,
    Array.from(operationMap.values()).sort(
      sortByNewestField(['updated_at', 'completed_at', 'status_at', 'requested_at'])
    )
  );
}

function scheduleRender() {
  if (renderQueued) return;
  renderQueued = true;
  if (typeof requestAnimationFrame === 'function') requestAnimationFrame(refreshOperations);
  else queueMicrotask(refreshOperations);
}

function eventIso(event) {
  return new Date((event?.created_at || 0) * 1000).toISOString();
}

function pruneUndefined(value) {
  return Object.fromEntries(
    Object.entries(value).filter(([, entry]) => entry !== undefined && entry !== '')
  );
}

function contentObject(event) {
  const parsed = parseJsonContent(event, {});
  return parsed && typeof parsed === 'object' && !Array.isArray(parsed) ? parsed : {};
}

function eventTags(event) {
  const tags = {};
  for (const tag of event?.tags || []) {
    if (!Array.isArray(tag) || tag.length < 2 || !tag[0] || tag[1] === '') continue;
    const [name, value] = tag;
    if (tags[name] === undefined) {
      tags[name] = value;
    } else if (Array.isArray(tags[name])) {
      tags[name].push(value);
    } else {
      tags[name] = [tags[name], value];
    }
  }
  return tags;
}

function entityRefs(event, content) {
  const refs = {};

  for (const [key, value] of Object.entries(content)) {
    if ((key.endsWith('_id') || key.endsWith('_pubkey')) && typeof value === 'string' && value) {
      refs[key] = value;
    }
  }

  for (const [tagName, fieldName] of Object.entries(ENTITY_TAG_ALIASES)) {
    if (refs[fieldName]) continue;
    const value = getTagValue(event, tagName);
    if (value) refs[fieldName] = value;
  }

  return refs;
}

function primaryEntity(refs) {
  for (const field of ENTITY_PRIORITY) {
    if (refs[field]) return { entity_type: field, entity_id: refs[field] };
  }
  return {};
}

function operationDomain(event, content) {
  // Known protocol kinds own their domain; event-controlled tags/content cannot
  // disguise an external result as a different operation family.
  return KIND_DOMAINS.get(event.kind) || getTagValue(event, 'domain') || content.domain || 'unknown';
}

function eventSnapshot(event, content) {
  return {
    id: event.id,
    kind: event.kind,
    pubkey: event.pubkey,
    created_at: event.created_at,
    tags: eventTags(event),
    content
  };
}

function operationSource(kind) {
  if (HIVE_CI_OPERATION_KINDS.includes(kind)) return 'hive-ci';
  if (kind === SOUL_FACTORY_PROVISIONING_STATUS || kind === SOUL_FACTORY_PROVISIONING_RESULT) {
    return 'soulfactory';
  }
  return 'bahia';
}

function operationKey(source, requestEventId) {
  return `${source}:${requestEventId}`;
}

function compareEventVersion(event, snapshot) {
  if (!snapshot) return 1;
  const createdAt = Number(event?.created_at || 0);
  const previousCreatedAt = Number(snapshot.created_at || 0);
  if (createdAt !== previousCreatedAt) return createdAt > previousCreatedAt ? 1 : -1;
  const eventId = String(event?.id || '');
  const previousId = String(snapshot.id || '');
  return eventId === previousId ? 0 : (eventId < previousId ? 1 : -1);
}

function removeFields(target, fields) {
  for (const field of fields) delete target[field];
}

const STATUS_OUTCOME_FIELDS = Object.freeze([
  'status',
  'message',
  'step',
  'phase',
  'success',
  'error',
  'terminal',
  'responder_pubkey',
  'status_event_id',
  'status_event_kind',
  'status_at',
  'status_event'
]);

const RESULT_OUTCOME_FIELDS = Object.freeze([
  'status',
  'message',
  'step',
  'phase',
  'success',
  'error',
  'terminal',
  'responder_pubkey',
  'result_event_id',
  'result_event_kind',
  'result_event',
  'completed_at'
]);

function mergeOperation(requestEventId, patch, event, role) {
  const source = operationSource(event.kind);
  const mapKey = operationKey(source, requestEventId);
  const existing = operationMap.get(mapKey) || {
    id: requestEventId,
    operation_id: requestEventId,
    request_event_id: requestEventId,
    source
  };
  const nextPatch = { ...patch, source };

  if (role === 'status') {
    const staleStatus = compareEventVersion(event, existing.status_event) <= 0;
    const resultAlreadyProjected = Boolean(existing.result_event_id);
    const terminalRegression = existing.terminal && !nextPatch.terminal;
    if (staleStatus || resultAlreadyProjected || terminalRegression) {
      removeFields(nextPatch, STATUS_OUTCOME_FIELDS);
    }
  } else if (role === 'result' && compareEventVersion(event, existing.result_event) <= 0) {
    removeFields(nextPatch, RESULT_OUTCOME_FIELDS);
  }

  const mergedRefs = {
    ...(existing.entity_refs || {}),
    ...(nextPatch.entity_refs || {})
  };
  const merged = {
    ...existing,
    ...pruneUndefined(nextPatch),
    entity_refs: mergedRefs
  };
  const primary = primaryEntity(mergedRefs);
  if (!merged.entity_id || nextPatch.entity_refs) Object.assign(merged, primary);

  const updatedAt = eventIso(event);
  if (!merged.updated_at || updatedAt > merged.updated_at) merged.updated_at = updatedAt;

  operationMap.set(mapKey, merged);
  return true;
}

function statusFromEvent(event, content, fallback = '') {
  return String(getTagValue(event, 'status') || content.status || fallback).toLowerCase();
}

function booleanValue(value) {
  if (value === true || value === 'true') return true;
  if (value === false || value === 'false') return false;
  return undefined;
}

function commonPatch(event, content) {
  const refs = entityRefs(event, content);
  return {
    domain: operationDomain(event, content),
    operation: getTagValue(event, 'operation') || getTagValue(event, 'command') || content.operation || content.command,
    entity_refs: refs,
    ...refs,
    responder_pubkey: event.pubkey
  };
}

/** Addressable ML request: keyed by its signed request event id for result correlation. */
function projectOperationRequest(event) {
  if (!event?.id) return false;

  const content = contentObject(event);
  const status = statusFromEvent(event, content, 'requested');
  return mergeOperation(event.id, {
    ...commonPatch(event, content),
    id: event.id,
    operation_id: event.id,
    request_event_id: event.id,
    status,
    message: content.message,
    requested_at: eventIso(event),
    request_event: eventSnapshot(event, content),
    terminal: isTerminalOperationStatus(status) || undefined
  }, event, 'request');
}

/** Operational 69xx status: correlated by the request event id in the `e` tag. */
function projectOperationStatus(event) {
  const requestEventId = getTagValue(event, 'e');
  if (!requestEventId) return false;

  const content = contentObject(event);
  const status = statusFromEvent(event, content, 'processing');
  return mergeOperation(requestEventId, {
    ...commonPatch(event, content),
    id: requestEventId,
    operation_id: requestEventId,
    request_event_id: requestEventId,
    status,
    step: getTagValue(event, 'step') || content.step,
    message: content.message || event.content,
    status_event_id: event.id,
    status_event_kind: event.kind,
    status_at: eventIso(event),
    status_event: eventSnapshot(event, content),
    terminal: isTerminalOperationStatus(status) || undefined
  }, event, 'status');
}

/** Operational 79xx result: terminal and correlated by the request `e` tag. */
function projectOperationResult(event) {
  const requestEventId = getTagValue(event, 'e');
  if (!requestEventId) return false;

  const content = contentObject(event);
  const taggedSuccess = booleanValue(getTagValue(event, 'success'));
  const contentSuccess = booleanValue(content.success);
  const error = getTagValue(event, 'error') || content.error;
  let status = statusFromEvent(event, content);
  if (!status) {
    status = taggedSuccess === false || contentSuccess === false || error ? 'failed' : 'completed';
  }
  const success = taggedSuccess ?? contentSuccess
    ?? ['success', 'succeeded', 'completed', 'complete'].includes(status);

  return mergeOperation(requestEventId, {
    ...commonPatch(event, content),
    id: requestEventId,
    operation_id: requestEventId,
    request_event_id: requestEventId,
    status,
    message: content.message || (typeof error === 'string' ? error : undefined),
    success,
    error,
    result_event_id: event.id,
    result_event_kind: event.kind,
    result_event: eventSnapshot(event, content),
    terminal: true,
    completed_at: eventIso(event)
  }, event, 'result');
}

/** Hive-CI 5401 run: it is the request-side event and is keyed by its own id. */
function projectHiveCIWorkflowRun(event) {
  const requestEventId = event?.id;
  const publisherPubkey = getTagValue(event, 'publisher');
  if (!requestEventId || !publisherPubkey) return false;

  const content = contentObject(event);
  const mapKey = operationKey('hive-ci', requestEventId);
  const changed = mergeOperation(requestEventId, {
    ...commonPatch(event, content),
    id: requestEventId,
    operation_id: requestEventId,
    request_event_id: requestEventId,
    run_event_id: requestEventId,
    publisher_pubkey: publisherPubkey,
    status: operationMap.get(mapKey)?.status || 'running',
    requested_at: eventIso(event),
    request_event: eventSnapshot(event, content)
  }, event, 'status');

  const pendingResult = pendingHiveResultMap.get(requestEventId);
  if (pendingResult) {
    pendingHiveResultMap.delete(requestEventId);
    if (pendingResult.pubkey === publisherPubkey) projectHiveCIWorkflowResult(pendingResult);
  }
  return changed;
}

/** Hive-CI 5402 result: terminal and correlated to its 5401 run by `e`. */
function projectHiveCIWorkflowResult(event) {
  const requestEventId = getTagValue(event, 'e');
  if (!requestEventId) return false;

  const existing = operationMap.get(operationKey('hive-ci', requestEventId));
  if (!existing?.request_event) {
    const pending = pendingHiveResultMap.get(requestEventId);
    if (!pending || compareEventVersion(event, pending) > 0) pendingHiveResultMap.set(requestEventId, event);
    return true;
  }
  if (event.pubkey !== existing.publisher_pubkey) return false;
  return projectOperationResult(event);
}

const allOperationKinds = Object.freeze([...new Set([...CANONICAL_OPERATION_KINDS, ...EXTERNAL_OPERATION_KINDS])]);

function isTrustedOperation(event, servicePubkey) {
  return EXTERNAL_OPERATION_KINDS.includes(event.kind) || event.pubkey === servicePubkey;
}

function operationIdFor(event) {
  if (OPERATION_REQUEST_KINDS.includes(event.kind) || event.kind === HIVE_CI_WORKFLOW_RUN) return event.id;
  return getTagValue(event, 'e');
}

function operationCoordinate(event) {
  const d = getTagValue(event, 'd');
  return d && event.kind >= 30000 && event.kind < 40000 ? `${event.kind}:${event.pubkey}:${d}` : '';
}

function projectOperation(event) {
  if (OPERATION_REQUEST_KINDS.includes(event.kind)) return projectOperationRequest(event);
  if (OPERATION_STATUS_KINDS.includes(event.kind)) return projectOperationStatus(event);
  if (OPERATION_RESULT_KINDS.includes(event.kind)) return projectOperationResult(event);
  if (event.kind === HIVE_CI_WORKFLOW_RUN) return projectHiveCIWorkflowRun(event);
  if (event.kind === HIVE_CI_WORKFLOW_RESULT) return projectHiveCIWorkflowResult(event);
  return false;
}

function replayOperation(key) {
  operationMap.delete(key);
  pendingHiveResultMap.delete(key.split(':').slice(1).join(':'));
  const events = [...(operationEvents.get(key)?.values() || [])].sort((a, b) => a.created_at - b.created_at || b.id.localeCompare(a.id));
  for (const event of events) projectOperation(event);
}

function ingestOperationEvent(event, servicePubkey, initial = false) {
  if (event.kind === 5) {
    let changed = false;
    for (const [name, target] of event.tags || []) {
      const id = name === 'e' ? target : (name === 'a' ? coordinateEvents.get(target) : null);
      const indexed = id && eventOperation.get(id);
      if (!indexed || indexed.event.pubkey !== event.pubkey || indexed.event.created_at > event.created_at) continue;
      eventOperation.delete(id);
      if (indexed.coordinate) coordinateEvents.delete(indexed.coordinate);
      operationEvents.get(indexed.key)?.delete(id);
      replayOperation(indexed.key);
      changed = true;
    }
    if (changed && !initial) scheduleRender();
    return changed;
  }
  if (!allOperationKinds.includes(event.kind) || !isTrustedOperation(event, servicePubkey)) return false;
  const operationId = operationIdFor(event);
  if (!operationId) return false;
  const key = operationKey(operationSource(event.kind), operationId);
  const coordinate = operationCoordinate(event);
  const priorId = coordinate && coordinateEvents.get(coordinate);
  let replaced = false;
  if (priorId && priorId !== event.id) {
    const prior = eventOperation.get(priorId);
    if (prior && compareEventVersion(event, prior.event) <= 0) return false;
    eventOperation.delete(priorId);
    if (prior) {
      operationEvents.get(prior.key)?.delete(priorId);
      replayOperation(prior.key);
      replaced = true;
    }
  }
  if (eventOperation.has(event.id)) return false;
  eventOperation.set(event.id, { event, key, coordinate });
  if (coordinate) coordinateEvents.set(coordinate, event.id);
  if (!operationEvents.has(key)) operationEvents.set(key, new Map());
  operationEvents.get(key).set(event.id, event);
  const changed = projectOperation(event) || replaced;
  if (changed && !initial) scheduleRender();
  return changed;
}

export function rebuildOperationsFromStore() {
  const store = getEventStore();
  const servicePubkey = getServicePubkey();
  if (!store || !servicePubkey) return;
  resetOperations();
  const events = store.query({ kinds: allOperationKinds })
    .sort((a, b) => a.created_at - b.created_at || b.id.localeCompare(a.id));
  for (const event of events) ingestOperationEvent(event, servicePubkey, true);
  renderQueued = true;
  refreshOperations();
}

export function initOperationStoreBinding() {
  if (boundStore) return;
  const store = getEventStore();
  const servicePubkey = getServicePubkey();
  if (!store || !servicePubkey) return;
  boundStore = store;
  rebuildOperationsFromStore();
  unsubscribers = [
    store.subscribe({ kinds: allOperationKinds }, (event) => ingestOperationEvent(event, servicePubkey)),
    store.subscribe({ kinds: [5] }, (event) => ingestOperationEvent(event, servicePubkey))
  ];
  const pool = getPool();
  const relays = [...new Set(getRelayUrls().map(toWebSocketUrl).filter(Boolean))];
  if (pool && relays.length) relayHandles = [
    pool.subscribe({ relays, filters: [{ kinds: CANONICAL_OPERATION_KINDS, authors: [servicePubkey], since: Math.floor(Date.now() / 1000) - 7 * 24 * 60 * 60, limit: 1000 }], filterKey: 'operations-canonical' }),
    pool.subscribe({ relays, filters: [{ kinds: EXTERNAL_OPERATION_KINDS, since: Math.floor(Date.now() / 1000) - 7 * 24 * 60 * 60, limit: 1000 }], filterKey: 'operations-external' })
  ];
}

export function teardownOperationStoreBinding() {
  for (const unsubscribe of unsubscribers) unsubscribe();
  unsubscribers = [];
  for (const handle of relayHandles) handle.unsubscribe();
  relayHandles = [];
  boundStore = null;
  renderQueued = false;
}

export function operationsForDomain(items, domain) {
  if (!domain) return [];
  return (items || []).filter((operation) => operation.domain === domain);
}

export function operationsForEntity(items, entityType, entityId) {
  if (!entityType || !entityId) return [];
  const normalizedType = ENTITY_TAG_ALIASES[entityType] || entityType;
  return (items || []).filter((operation) => (
    operation.entity_refs?.[normalizedType] === entityId || operation[normalizedType] === entityId
  ));
}
