import { services, upsertServiceProjection } from './services.svelte.js';
import { environments } from './environments.svelte.js';
import {
  states,
  llmRoutes,
  llmRouteStates,
  artifacts,
  builds,
  deploymentIntents,
  deploymentRuns,
  policies,
  packageRepositories,
  packageArtifacts,
  packagePromotions
} from './deployments.svelte.js';
import {
  workers,
  workerAssignments,
  workerDrainStatuses,
  workerEligibilityPreviews,
  workerCleanupExecutions
} from './workers.svelte.js';
import {
  backupRepositories,
  backupPolicies,
  backupRecipes,
  backupDefinitions,
  backupRuns,
  backupVerifications,
  backupRestores,
  backupRetentionRuns,
  backupRuntimeObservations
} from './backup.svelte.js';
import { mlModels, mlModelVersions, mlEndpoints, mlEndpointStates } from './ml.svelte.js';
import { events } from './activity.svelte.js';
import { sbomRefs, sbomAvailability, sbomRefsByArtifact, getSBOMRefsForArtifact, hasSBOMForArtifact, sbomArtifactIds } from './sbom.svelte.js';
import { browser } from '$app/environment';

export { services, upsertServiceProjection } from './services.svelte.js';
export { environments } from './environments.svelte.js';
export {
  states,
  llmRoutes,
  llmRouteStates,
  artifacts,
  builds,
  deploymentIntents,
  deploymentRuns,
  policies,
  packageRepositories,
  packageArtifacts,
  packagePromotions
} from './deployments.svelte.js';
export {
  workers,
  workerAssignments,
  workerDrainStatuses,
  workerEligibilityPreviews,
  workerCleanupExecutions
} from './workers.svelte.js';
export {
  backupRepositories,
  backupPolicies,
  backupRecipes,
  backupDefinitions,
  backupRuns,
  backupVerifications,
  backupRestores,
  backupRetentionRuns,
  backupRuntimeObservations
} from './backup.svelte.js';
export { mlModels, mlModelVersions, mlEndpoints, mlEndpointStates } from './ml.svelte.js';
export { events } from './activity.svelte.js';
export { sbomRefs, sbomAvailability, sbomRefsByArtifact, getSBOMRefsForArtifact, hasSBOMForArtifact, sbomArtifactIds } from './sbom.svelte.js';

import { resetServices, refreshServices } from './services.svelte.js';
import { resetEnvironments, refreshEnvironments } from './environments.svelte.js';
import { resetDeployments, refreshDeployments } from './deployments.svelte.js';
import { resetWorkers, refreshWorkers } from './workers.svelte.js';
import { resetBackup, refreshBackup } from './backup.svelte.js';
import { resetML, refreshML } from './ml.svelte.js';
import { resetActivity, refreshActivity } from './activity.svelte.js';
import { resetSBOM, refreshSBOM } from './sbom.svelte.js';

const CONTROLPLANE_SNAPSHOT_KEY = 'bahia_controlplane_snapshot_v1';
const CONTROLPLANE_SNAPSHOT_TTL_MS = 15 * 60 * 1000;
let persistTimer = null;

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

export function setAllLoading(value) {
  loading.services = value;
  loading.environments = value;
  loading.states = value;
  loading.workers = value;
}

export function resetCollections() {
  resetServices();
  resetEnvironments();
  resetDeployments();
  resetWorkers();
  resetBackup();
  resetML();
  resetActivity();
  resetSBOM();
  setAllLoading(false);
}

export function refreshCollections() {
  refreshServices();
  refreshEnvironments();
  refreshDeployments();
  refreshWorkers();
  refreshBackup();
  refreshML();
  refreshActivity();
  refreshSBOM();
}

function replaceSnapshotArray(target, values) {
  target.length = 0;
  if (Array.isArray(values)) target.push(...values);
}

export function controlplaneSnapshot() {
  return {
    schema: CONTROLPLANE_SNAPSHOT_KEY,
    cachedAt: Date.now(),
    collections: {
      services: Array.from(services),
      environments: Array.from(environments),
      states: Array.from(states),
      llmRoutes: Array.from(llmRoutes),
      llmRouteStates: Array.from(llmRouteStates),
      artifacts: Array.from(artifacts),
      builds: Array.from(builds),
      deploymentIntents: Array.from(deploymentIntents),
      deploymentRuns: Array.from(deploymentRuns),
      policies: Array.from(policies),
      packageRepositories: Array.from(packageRepositories),
      packageArtifacts: Array.from(packageArtifacts),
      packagePromotions: Array.from(packagePromotions),
      workers: Array.from(workers),
      workerAssignments: Array.from(workerAssignments),
      workerDrainStatuses: Array.from(workerDrainStatuses),
      workerEligibilityPreviews: Array.from(workerEligibilityPreviews),
      workerCleanupExecutions: Array.from(workerCleanupExecutions),
      events: Array.from(events),
      sbomRefs: Array.from(sbomRefs),
      sbomAvailability: Array.from(sbomAvailability),
      backupRepositories: Array.from(backupRepositories),
      backupPolicies: Array.from(backupPolicies),
      backupRecipes: Array.from(backupRecipes),
      backupDefinitions: Array.from(backupDefinitions),
      backupRuns: Array.from(backupRuns),
      backupVerifications: Array.from(backupVerifications),
      backupRestores: Array.from(backupRestores),
      backupRetentionRuns: Array.from(backupRetentionRuns),
      backupRuntimeObservations: Array.from(backupRuntimeObservations),
      mlModels: Array.from(mlModels),
      mlModelVersions: Array.from(mlModelVersions),
      mlEndpoints: Array.from(mlEndpoints),
      mlEndpointStates: Array.from(mlEndpointStates)
    }
  };
}

export function hydrateCachedCollections() {
  if (!browser || typeof localStorage?.getItem !== 'function') return false;

  try {
    const raw = localStorage.getItem(CONTROLPLANE_SNAPSHOT_KEY);
    if (!raw) return false;

    const snapshot = JSON.parse(raw);
    const age = Date.now() - Number(snapshot?.cachedAt || 0);
    if (snapshot?.schema !== CONTROLPLANE_SNAPSHOT_KEY || age > CONTROLPLANE_SNAPSHOT_TTL_MS) return false;

    const cached = snapshot.collections || {};
    replaceSnapshotArray(services, cached.services);
    replaceSnapshotArray(environments, cached.environments);
    replaceSnapshotArray(states, cached.states);
    replaceSnapshotArray(llmRoutes, cached.llmRoutes);
    replaceSnapshotArray(llmRouteStates, cached.llmRouteStates);
    replaceSnapshotArray(artifacts, cached.artifacts);
    replaceSnapshotArray(builds, cached.builds);
    replaceSnapshotArray(deploymentIntents, cached.deploymentIntents);
    replaceSnapshotArray(deploymentRuns, cached.deploymentRuns);
    replaceSnapshotArray(policies, cached.policies);
    replaceSnapshotArray(packageRepositories, cached.packageRepositories);
    replaceSnapshotArray(packageArtifacts, cached.packageArtifacts);
    replaceSnapshotArray(packagePromotions, cached.packagePromotions);
    replaceSnapshotArray(workers, cached.workers);
    replaceSnapshotArray(workerAssignments, cached.workerAssignments);
    replaceSnapshotArray(workerDrainStatuses, cached.workerDrainStatuses);
    replaceSnapshotArray(workerEligibilityPreviews, cached.workerEligibilityPreviews);
    replaceSnapshotArray(workerCleanupExecutions, cached.workerCleanupExecutions);
    replaceSnapshotArray(events, cached.events);
    replaceSnapshotArray(sbomRefs, cached.sbomRefs);
    replaceSnapshotArray(sbomAvailability, cached.sbomAvailability);
    replaceSnapshotArray(backupRepositories, cached.backupRepositories);
    replaceSnapshotArray(backupPolicies, cached.backupPolicies);
    replaceSnapshotArray(backupRecipes, cached.backupRecipes);
    replaceSnapshotArray(backupDefinitions, cached.backupDefinitions);
    replaceSnapshotArray(backupRuns, cached.backupRuns);
    replaceSnapshotArray(backupVerifications, cached.backupVerifications);
    replaceSnapshotArray(backupRestores, cached.backupRestores);
    replaceSnapshotArray(backupRetentionRuns, cached.backupRetentionRuns);
    replaceSnapshotArray(backupRuntimeObservations, cached.backupRuntimeObservations);
    replaceSnapshotArray(mlModels, cached.mlModels);
    replaceSnapshotArray(mlModelVersions, cached.mlModelVersions);
    replaceSnapshotArray(mlEndpoints, cached.mlEndpoints);
    replaceSnapshotArray(mlEndpointStates, cached.mlEndpointStates);
    return true;
  } catch (error) {
    console.warn('Failed to hydrate cached controlplane snapshot:', error);
    return false;
  }
}

export function persistCachedCollections() {
  if (!browser || typeof localStorage?.setItem !== 'function') return false;

  try {
    localStorage.setItem(CONTROLPLANE_SNAPSHOT_KEY, JSON.stringify(controlplaneSnapshot()));
    return true;
  } catch (error) {
    console.warn('Failed to persist controlplane snapshot:', error);
    return false;
  }
}

export function schedulePersistCachedCollections(delayMs = 150) {
  if (!browser) return;
  if (persistTimer) clearTimeout(persistTimer);
  persistTimer = setTimeout(() => {
    persistTimer = null;
    persistCachedCollections();
  }, delayMs);
}
