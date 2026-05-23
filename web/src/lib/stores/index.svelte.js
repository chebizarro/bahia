import { isAuthenticated, currentUser } from './auth.js';
import { systemInfo, loadSystemInfo, currentSystemInfo } from './system.svelte.js';
export { discoveryState, discoverSystemInfo, resetDiscoveryStore } from './discovery.svelte.js';
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
  mlModels,
  mlModelVersions,
  mlEndpoints,
  mlEndpointStates,
  loading,
  controlplaneConnection,
  bootstrapControlplane,
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
export { services, environments, states, llmRoutes, llmRouteStates, artifacts, builds, deploymentIntents, deploymentRuns, policies, packageRepositories, packageArtifacts, packagePromotions, workers, workerAssignments, workerDrainStatuses, workerEligibilityPreviews, events, backupRepositories, backupPolicies, backupRecipes, backupDefinitions, backupRuns, backupVerifications, backupRestores, backupRetentionRuns, backupRuntimeObservations, mlModels, mlModelVersions, mlEndpoints, mlEndpointStates, loading, controlplaneConnection, bootstrapControlplane, upsertServiceProjection };

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

// In-flight request deduplication for public relay bootstrap paths.
const inFlight = {
  all: null,
  events: null
};

export async function loadServices() { return bootstrapControlplane(); }
export async function loadEnvironments() { return bootstrapControlplane(); }
export async function loadStates() { return bootstrapControlplane(); }
export async function loadWorkers() { return bootstrapControlplane(); }
export async function loadArtifacts() { return bootstrapControlplane(); }
export async function loadBuilds() { return bootstrapControlplane(); }
export async function loadDeploymentIntents() { return bootstrapControlplane(); }
export async function loadDeploymentRuns() { return bootstrapControlplane(); }
export async function loadPolicies() { return bootstrapControlplane(); }
export async function loadPackageRepositories() { return bootstrapControlplane(); }
export async function loadPackageArtifacts() { return bootstrapControlplane(); }
export async function loadPackagePromotions() { return bootstrapControlplane(); }
export async function loadBackupControlplane() { return bootstrapControlplane(); }

export async function loadAll() {
  if (inFlight.all) return inFlight.all;

  inFlight.all = (async () => {
    const result = await bootstrapControlplane();
    if (!result.ok) {
      console.error('Nostr controlplane bootstrap failed:', result.reason);
    }
  })();

  try {
    await inFlight.all;
  } finally {
    inFlight.all = null;
  }
}

export function subscribeToEvents() {
  unsubscribeFromEvents();

  inFlight.events = bootstrapControlplane().then((result) => {
    if (result.ok) return;
    console.error('Nostr controlplane bootstrap failed:', result.reason);
  });
}

export function unsubscribeFromEvents() {
  disconnectControlplane();
  inFlight.events = null;
}
