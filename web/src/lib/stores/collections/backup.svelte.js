import {
  applyProjectedEntity,
  getDTag,
  getTagValue,
  parseJsonContent,
  replaceArray,
  sortByNameOrId,
  sortByNewestField
} from './utils.js';

export const backupRepositories = $state([]);
export const backupPolicies = $state([]);
export const backupRecipes = $state([]);
export const backupDefinitions = $state([]);
export const backupRuns = $state([]);
export const backupVerifications = $state([]);
export const backupRestores = $state([]);
export const backupRetentionRuns = $state([]);
export const backupRuntimeObservations = $state([]);

const backupRepositoryMap = new Map();
const backupPolicyMap = new Map();
const backupRecipeMap = new Map();
const backupDefinitionMap = new Map();
const backupRunMap = new Map();
const backupVerificationMap = new Map();
const backupRestoreMap = new Map();
const backupRetentionMap = new Map();
const backupRuntimeObservationMap = new Map();

export function resetBackup() {
  [backupRepositoryMap, backupPolicyMap, backupRecipeMap, backupDefinitionMap, backupRunMap,
    backupVerificationMap, backupRestoreMap, backupRetentionMap, backupRuntimeObservationMap]
    .forEach((map) => map.clear());
  [backupRepositories, backupPolicies, backupRecipes, backupDefinitions, backupRuns,
    backupVerifications, backupRestores, backupRetentionRuns, backupRuntimeObservations]
    .forEach((array) => { array.length = 0; });
}

export function refreshBackup() {
  replaceArray(backupRepositories, Array.from(backupRepositoryMap.values()).sort(sortByNameOrId));
  replaceArray(backupPolicies, Array.from(backupPolicyMap.values()).sort(sortByNameOrId));
  replaceArray(backupRecipes, Array.from(backupRecipeMap.values()).sort(sortByNameOrId));
  replaceArray(backupDefinitions, Array.from(backupDefinitionMap.values()).sort(sortByNameOrId));
  replaceArray(backupRuns, Array.from(backupRunMap.values()).sort(sortByNewestField(['created_at', 'started_at'])));
  replaceArray(backupVerifications, Array.from(backupVerificationMap.values()).sort(sortByNewestField(['created_at', 'verified_at'])));
  replaceArray(backupRestores, Array.from(backupRestoreMap.values()).sort(sortByNewestField(['created_at', 'started_at'])));
  replaceArray(backupRetentionRuns, Array.from(backupRetentionMap.values()).sort(sortByNewestField(['created_at', 'started_at'])));
  replaceArray(backupRuntimeObservations, Array.from(backupRuntimeObservationMap.values()).sort(sortByNewestField(['generated_at', 'updated_at'])));
}

export function hasBackupDefinitionShape(event) {
  if (String(getDTag(event) || '').startsWith('backup-definition:')) return true;
  const content = parseJsonContent(event, {});
  return Boolean(content.repository_id || content.recipe_id || content.policy_id ||
    content.schedule_enabled !== undefined || getTagValue(event, 'repository_id') ||
    getTagValue(event, 'recipe_id') || getTagValue(event, 'policy_id'));
}

export function hasBackupPolicyShape(event) {
  if (String(getDTag(event) || '').startsWith('backup-policy:')) return true;
  const content = parseJsonContent(event, {});
  return Boolean(content.require_verification !== undefined || content.verification_mode ||
    getTagValue(event, 'require_verification') || getTagValue(event, 'verification'));
}

export function hasBackupRepositoryShape(event) {
  if (String(getDTag(event) || '').startsWith('backup-repository:')) return true;
  const content = parseJsonContent(event, {});
  return Boolean(content.repository_uri || content.credential_profile || content.backend ||
    getTagValue(event, 'repository_id') || getTagValue(event, 'backend'));
}

export const backupApplicators = {
  repository: (event, replaceableEvents) => applyProjectedEntity(event, backupRepositoryMap, replaceableEvents, ['id', 'repository_id']),
  policy: (event, replaceableEvents) => applyProjectedEntity(event, backupPolicyMap, replaceableEvents, ['id', 'policy_id']),
  recipe: (event, replaceableEvents) => applyProjectedEntity(event, backupRecipeMap, replaceableEvents, ['id', 'recipe_id']),
  definition: (event, replaceableEvents) => applyProjectedEntity(event, backupDefinitionMap, replaceableEvents, ['id', 'definition_id']),
  retention: (event, replaceableEvents) => applyProjectedEntity(event, backupRetentionMap, replaceableEvents, ['id', 'retention_run_id']),
  run: (event, replaceableEvents) => applyProjectedEntity(event, backupRunMap, replaceableEvents, ['id', 'run_id']),
  verification: (event, replaceableEvents) => applyProjectedEntity(event, backupVerificationMap, replaceableEvents, ['id', 'verification_id', 'backup_run_id']),
  restore: (event, replaceableEvents) => applyProjectedEntity(event, backupRestoreMap, replaceableEvents, ['id', 'restore_id']),
  runtimeObservation: (event, replaceableEvents) => applyProjectedEntity(event, backupRuntimeObservationMap, replaceableEvents, ['id', 'scope'])
};
