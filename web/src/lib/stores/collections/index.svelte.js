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
  workerEligibilityPreviews
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

import { resetServices, refreshServices } from './services.svelte.js';
import { resetEnvironments, refreshEnvironments } from './environments.svelte.js';
import { resetDeployments, refreshDeployments } from './deployments.svelte.js';
import { resetWorkers, refreshWorkers } from './workers.svelte.js';
import { resetBackup, refreshBackup } from './backup.svelte.js';
import { resetML, refreshML } from './ml.svelte.js';
import { resetActivity, refreshActivity } from './activity.svelte.js';

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
}
