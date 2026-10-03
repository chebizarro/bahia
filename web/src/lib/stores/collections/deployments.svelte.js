import {
  applyProjectedEntity,
  selectProjectedEvent,
  contentWithEventMeta,
  getDTag,
  getTagValue,
  isReplaceableTombstone,
  replaceArray,
  sortByNameOrId,
  sortByNewestField
} from './utils.js';
import { upsertReplaceableEvent } from '../../nostr/client.js';
import { getEventStore, getServicePubkey } from '../../nostr/boot.js';
import { CP_STATE_TOPICS } from '../../nostr/kinds.gen.js';
import { createCoreQuery, contentId, scopedStateId, stateProjection } from './core-query.js';

export const states = $state([]);
export const llmRoutes = $state([]);
export const llmRouteStates = $state([]);
export const artifacts = $state([]);
export const builds = $state([]);
export const deploymentIntents = $state([]);
export const deploymentRuns = $state([]);
export const policies = $state([]);
export const packageRepositories = $state([]);
export const packageArtifacts = $state([]);
export const packagePromotions = $state([]);

const llmRouteMap = new Map();
const llmRouteStateMap = new Map();
const artifactMap = new Map();
const buildMap = new Map();
const deploymentIntentMap = new Map();
const deploymentRunMap = new Map();
const deploymentIntentWatermarks = new Map();
const deploymentRunWatermarks = new Map();

const coreQueries = [
  createCoreQuery({ topic: CP_STATE_TOPICS.SERVICE_STATE, target: states, identity: scopedStateId, project: stateProjection }),
  createCoreQuery({ topic: CP_STATE_TOPICS.POLICY_REGISTRY, target: policies, identity: event => contentId(event, 'id', 'policy_id') }),
  createCoreQuery({ topic: CP_STATE_TOPICS.PACKAGE_REPOSITORY, target: packageRepositories, identity: event => contentId(event, 'id', 'repository_id') }),
  createCoreQuery({ topic: CP_STATE_TOPICS.PACKAGE_ARTIFACT, target: packageArtifacts, identity: event => contentId(event, 'id', 'artifact_id'), sort: sortByNewestField(['created_at']) }),
  createCoreQuery({ topic: CP_STATE_TOPICS.PACKAGE_PROMOTION, target: packagePromotions, identity: event => contentId(event, 'id', 'promotion_id'), sort: sortByNewestField(['promoted_at', 'published_at', 'created_at']) })
];

export function initCoreDeploymentStoreBindings() {
  const store = getEventStore();
  const servicePubkey = getServicePubkey();
  for (const query of coreQueries) query.bind(store, servicePubkey);
}
export function teardownCoreDeploymentStoreBindings() {
  for (const query of coreQueries) query.unbind();
}

export function resetDeployments() {
  [llmRouteMap, llmRouteStateMap, artifactMap, buildMap, deploymentIntentMap, deploymentRunMap]
    .forEach((map) => map.clear());
  [deploymentIntentWatermarks, deploymentRunWatermarks].forEach((map) => map.clear());
  [llmRoutes, llmRouteStates, artifacts, builds, deploymentIntents, deploymentRuns]
    .forEach((array) => { array.length = 0; });
  for (const query of coreQueries) query.reset();
}

export function refreshDeployments() {
  replaceArray(llmRoutes, Array.from(llmRouteMap.values()).sort(sortByNameOrId));
  replaceArray(llmRouteStates, Array.from(llmRouteStateMap.values()).sort(sortByNameOrId));
  replaceArray(artifacts, Array.from(artifactMap.values()).sort(sortByNewestField(['created_at'])));
  replaceArray(builds, Array.from(buildMap.values()).sort(sortByNewestField(['created_at'])));
  replaceArray(deploymentIntents, Array.from(deploymentIntentMap.values()).sort(sortByNewestField(['created_at'])));
  replaceArray(deploymentRuns, Array.from(deploymentRunMap.values()).sort(sortByNewestField(['created_at'])));
}

function applyScopedState(event, targetMap, replaceableEvents, scopeTags, watermarks = null) {
  const content = contentWithEventMeta(event);
  const dTag = getDTag(event);
  const values = Object.fromEntries(scopeTags.map(([field, tag]) => [field, content[field] || getTagValue(event, tag)]));
  const composed = Object.values(values).every(Boolean) ? Object.values(values).join(':') : '';
  // Logical scope wins over relay d-tags so legacy and corrected coordinates
  // deterministically converge on one row after reconnect.
  const id = composed || content.id || dTag;
  if (!id) return false;

  const winner = selectProjectedEvent(event, replaceableEvents, id, watermarks);
  if (!winner) return false;
  event = winner;
  const winnerContent = contentWithEventMeta(winner);
  const winnerValues = Object.fromEntries(scopeTags.map(([field, tag]) => [field, winnerContent[field] || getTagValue(winner, tag)]));
  if (isReplaceableTombstone(event)) {
    targetMap.delete(id);
  } else {
    targetMap.set(id, { ...winnerContent, ...winnerValues, id });
  }
  return true;
}

function applyLLMRouteEvent(event, replaceableEvents) {
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

export const deploymentApplicators = {
  llmRoute: applyLLMRouteEvent,
  llmRouteState: (event, replaceableEvents) => applyScopedState(event, llmRouteStateMap, replaceableEvents, [['route_id', 'route'], ['environment_id', 'environment']]),
  artifact: (event, replaceableEvents) => applyProjectedEntity(event, artifactMap, replaceableEvents, ['id', 'artifact_id']),
  build: (event, replaceableEvents) => applyProjectedEntity(event, buildMap, replaceableEvents, ['id', 'build_id']),
  intent: (event, replaceableEvents) => applyProjectedEntity(event, deploymentIntentMap, replaceableEvents, ['id', 'intent_id'], deploymentIntentWatermarks),
  run: (event, replaceableEvents) => applyProjectedEntity(event, deploymentRunMap, replaceableEvents, ['id', 'run_id'], deploymentRunWatermarks)
};
