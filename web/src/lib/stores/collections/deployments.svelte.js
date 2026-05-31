import {
  applyProjectedEntity,
  contentWithEventMeta,
  getDTag,
  getTagValue,
  isReplaceableTombstone,
  replaceArray,
  sortByNameOrId,
  sortByNewestField
} from './utils.js';
import { upsertReplaceableEvent } from '../../nostr/client.js';

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

const stateMap = new Map();
const llmRouteMap = new Map();
const llmRouteStateMap = new Map();
const artifactMap = new Map();
const buildMap = new Map();
const deploymentIntentMap = new Map();
const deploymentRunMap = new Map();
const policyMap = new Map();
const packageRepositoryMap = new Map();
const packageArtifactMap = new Map();
const packagePromotionMap = new Map();

export function resetDeployments() {
  [stateMap, llmRouteMap, llmRouteStateMap, artifactMap, buildMap, deploymentIntentMap,
    deploymentRunMap, policyMap, packageRepositoryMap, packageArtifactMap, packagePromotionMap]
    .forEach((map) => map.clear());
  [states, llmRoutes, llmRouteStates, artifacts, builds, deploymentIntents, deploymentRuns,
    policies, packageRepositories, packageArtifacts, packagePromotions]
    .forEach((array) => { array.length = 0; });
}

export function refreshDeployments() {
  replaceArray(states, Array.from(stateMap.values()).sort(sortByNameOrId));
  replaceArray(llmRoutes, Array.from(llmRouteMap.values()).sort(sortByNameOrId));
  replaceArray(llmRouteStates, Array.from(llmRouteStateMap.values()).sort(sortByNameOrId));
  replaceArray(artifacts, Array.from(artifactMap.values()).sort(sortByNewestField(['created_at'])));
  replaceArray(builds, Array.from(buildMap.values()).sort(sortByNewestField(['created_at'])));
  replaceArray(deploymentIntents, Array.from(deploymentIntentMap.values()).sort(sortByNewestField(['created_at'])));
  replaceArray(deploymentRuns, Array.from(deploymentRunMap.values()).sort(sortByNewestField(['created_at'])));
  replaceArray(policies, Array.from(policyMap.values()).sort(sortByNameOrId));
  replaceArray(packageRepositories, Array.from(packageRepositoryMap.values()).sort(sortByNameOrId));
  replaceArray(packageArtifacts, Array.from(packageArtifactMap.values()).sort(sortByNewestField(['created_at'])));
  replaceArray(packagePromotions, Array.from(packagePromotionMap.values()).sort(sortByNewestField(['promoted_at', 'published_at', 'created_at'])));
}

function applyScopedState(event, targetMap, replaceableEvents, scopeTags) {
  const { accepted, key } = upsertReplaceableEvent(replaceableEvents, event);
  if (!accepted) return false;

  const content = contentWithEventMeta(event);
  const dTag = getDTag(event);
  const values = Object.fromEntries(scopeTags.map(([field, tag]) => [field, content[field] || getTagValue(event, tag)]));
  const composed = Object.values(values).every(Boolean) ? Object.values(values).join(':') : '';
  const id = content.id || dTag || composed || key;
  if (!id) return false;

  if (isReplaceableTombstone(event)) {
    targetMap.delete(id);
  } else {
    targetMap.set(id, { ...content, ...values, id });
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
  serviceState: (event, replaceableEvents) => applyScopedState(event, stateMap, replaceableEvents, [['service_id', 'service'], ['environment_id', 'environment']]),
  llmRoute: applyLLMRouteEvent,
  llmRouteState: (event, replaceableEvents) => applyScopedState(event, llmRouteStateMap, replaceableEvents, [['route_id', 'route'], ['environment_id', 'environment']]),
  artifact: (event, replaceableEvents) => applyProjectedEntity(event, artifactMap, replaceableEvents, ['id', 'artifact_id']),
  build: (event, replaceableEvents) => applyProjectedEntity(event, buildMap, replaceableEvents, ['id', 'build_id']),
  intent: (event, replaceableEvents) => applyProjectedEntity(event, deploymentIntentMap, replaceableEvents, ['id', 'intent_id']),
  run: (event, replaceableEvents) => applyProjectedEntity(event, deploymentRunMap, replaceableEvents, ['id', 'run_id']),
  policy: (event, replaceableEvents) => applyProjectedEntity(event, policyMap, replaceableEvents, ['id', 'policy_id']),
  packageRepository: (event, replaceableEvents) => applyProjectedEntity(event, packageRepositoryMap, replaceableEvents, ['id', 'repository_id']),
  packageArtifact: (event, replaceableEvents) => applyProjectedEntity(event, packageArtifactMap, replaceableEvents, ['id', 'artifact_id']),
  packagePromotion: (event, replaceableEvents) => applyProjectedEntity(event, packagePromotionMap, replaceableEvents, ['id', 'promotion_id'])
};
