import { getEventStore, getServicePubkey } from '../../nostr/boot.js';
import { CP_STATE_TOPICS } from '../../nostr/kinds.gen.js';
import { createCoreQuery, contentId, scopedStateId, stateProjection } from './core-query.js';
import { compareProjectionVersions, contentWithEventMeta, getTagValue, projectionVersion, sortByNewestField } from './utils.js';

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

function scopedRouteStateId(event) {
  const content = contentWithEventMeta(event);
  const route = content.route_id || getTagValue(event, 'route');
  const environment = content.environment_id || getTagValue(event, 'environment');
  return route && environment ? `${route}:${environment}` : contentId(event, 'id');
}

function routeStateProjection(event, id) {
  const content = contentWithEventMeta(event);
  return {
    ...content,
    route_id: content.route_id || getTagValue(event, 'route'),
    environment_id: content.environment_id || getTagValue(event, 'environment'),
    id
  };
}

function logicalNewer(left, right) {
  return !right || compareProjectionVersions(
    projectionVersion(contentWithEventMeta(left), left),
    projectionVersion(contentWithEventMeta(right), right)
  ) > 0;
}

const queries = [
  createCoreQuery({ topic: CP_STATE_TOPICS.SERVICE_STATE, target: states, identity: scopedStateId, project: stateProjection }),
  createCoreQuery({ topic: CP_STATE_TOPICS.LLM_ROUTE, target: llmRoutes, identity: event => contentId(event, 'id', 'route_id'), project: (event, id) => ({ ...contentWithEventMeta(event), id, route_id: id }) }),
  createCoreQuery({ topic: CP_STATE_TOPICS.LLM_STATE, target: llmRouteStates, identity: scopedRouteStateId, project: routeStateProjection }),
  createCoreQuery({ topic: CP_STATE_TOPICS.ARTIFACT_REGISTRY, target: artifacts, identity: event => contentId(event, 'id', 'artifact_id'), sort: sortByNewestField(['created_at']) }),
  createCoreQuery({ topic: CP_STATE_TOPICS.BUILD_REGISTRY, target: builds, identity: event => contentId(event, 'id', 'build_id'), sort: sortByNewestField(['created_at']) }),
  createCoreQuery({ topic: CP_STATE_TOPICS.DEPLOYMENT_INTENT, target: deploymentIntents, identity: event => contentId(event, 'id', 'intent_id'), sort: sortByNewestField(['created_at']), logicalNewer }),
  createCoreQuery({ topic: CP_STATE_TOPICS.DEPLOYMENT_RUN, target: deploymentRuns, identity: event => contentId(event, 'id', 'run_id'), sort: sortByNewestField(['created_at']), logicalNewer }),
  createCoreQuery({ topic: CP_STATE_TOPICS.POLICY_REGISTRY, target: policies, identity: event => contentId(event, 'id', 'policy_id') }),
  createCoreQuery({ topic: CP_STATE_TOPICS.PACKAGE_REPOSITORY, target: packageRepositories, identity: event => contentId(event, 'id', 'repository_id') }),
  createCoreQuery({ topic: CP_STATE_TOPICS.PACKAGE_ARTIFACT, target: packageArtifacts, identity: event => contentId(event, 'id', 'artifact_id'), sort: sortByNewestField(['created_at']) }),
  createCoreQuery({ topic: CP_STATE_TOPICS.PACKAGE_PROMOTION, target: packagePromotions, identity: event => contentId(event, 'id', 'promotion_id'), sort: sortByNewestField(['promoted_at', 'published_at', 'created_at']) })
];

export function initCoreDeploymentStoreBindings() {
  const store = getEventStore();
  const servicePubkey = getServicePubkey();
  for (const query of queries) query.bind(store, servicePubkey);
}
export function teardownCoreDeploymentStoreBindings() {
  for (const query of queries) query.unbind();
}
export function resetDeployments() {
  for (const query of queries) query.reset();
}
