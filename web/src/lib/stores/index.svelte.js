import { isAuthenticated, currentUser } from './auth.js';
import { systemInfo, loadSystemInfo, currentSystemInfo } from './system.svelte.js';
export { discoveryState, discoverSystemInfo, resetDiscoveryStore } from './discovery.svelte.js';
export { fipsMeshState, meshNodes, meshEndpoints, bootstrapFipsMesh, disconnectFipsMesh, resetFipsMeshStore, classifyHealth } from './fips-mesh.svelte.js';
import {
  services,
  environments,
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
  packagePromotions,
  workers,
  workerAssignments,
  workerDrainStatuses,
  workerEligibilityPreviews,
  workerCleanupExecutions,
  workerJobs,
  operations,
  events,
  backupRepositories,
  backupPolicies,
  backupRecipes,
  backupDefinitions,
  backupRuns,
  backupVerifications,
  backupRestores,
  backupRetentionRuns,
  backupRuntimeObservations,
  backupAttestations,
  mlModels,
  mlModelVersions,
  mlEndpoints,
  mlEndpointStates,
  controlplaneConnection,
  bootstrapControlplane,
  manualRetry,
  disconnectControlplane,
  upsertServiceProjection
} from './controlplane.svelte.js';

// Re-export theme store
export { theme, toggleTheme } from './theme.js';

// Auth state (compat exports)
export { isAuthenticated, currentUser };

// Shared public bootstrap/system state
export { systemInfo, loadSystemInfo, currentSystemInfo };

// Nostr-backed dashboard/read-model state
export { operationsForEntity, operationsForDomain } from './collections/index.svelte.js';
export { services, environments, states, llmRoutes, llmRouteStates, artifacts, builds, deploymentIntents, deploymentRuns, policies, packageRepositories, packageArtifacts, packagePromotions, workers, workerAssignments, workerDrainStatuses, workerEligibilityPreviews, workerCleanupExecutions, workerJobs, operations, events, backupRepositories, backupPolicies, backupRecipes, backupDefinitions, backupRuns, backupVerifications, backupRestores, backupRetentionRuns, backupRuntimeObservations, backupAttestations, mlModels, mlModelVersions, mlEndpoints, mlEndpointStates, controlplaneConnection, bootstrapControlplane, manualRetry, disconnectControlplane, upsertServiceProjection };

// Derived state helpers
export function driftedStates() {
  return states.filter((s) => s.drift_status === 'drifted');
}

export function serviceCount() {
  return services.length;
}

export function envCount() {
  return environments.length;
}

export function driftCount() {
  return driftedStates().length;
}

export function workerCount() {
  return workers.length;
}
