import { BACKUP_RUN_ATTESTATION, BACKUP_VERIFICATION_ATTESTATION, CP_STATE_TOPICS } from '../../nostr/kinds.gen.js';
import { getEventStore, getServicePubkey, onStoreRefresh } from '../../nostr/boot.js';
import { projectTopic } from './store-query.js';
import { getTagValue, parseJsonContent, replaceArray, sortByNameOrId, sortByNewestField } from './utils.js';

export const backupRepositories = $state([]);
export const backupPolicies = $state([]);
export const backupRecipes = $state([]);
export const backupDefinitions = $state([]);
export const backupRuns = $state([]);
export const backupVerifications = $state([]);
export const backupRestores = $state([]);
export const backupRetentionRuns = $state([]);
export const backupRuntimeObservations = $state([]);
export const backupAttestations = $state([]);
export const BACKUP_ATTESTATION_KINDS = Object.freeze([BACKUP_RUN_ATTESTATION, BACKUP_VERIFICATION_ATTESTATION]);
let unsubscribe = null;

const collections = [backupRepositories, backupPolicies, backupRecipes, backupDefinitions, backupRuns,
  backupVerifications, backupRestores, backupRetentionRuns, backupRuntimeObservations, backupAttestations];

export function resetBackup() { for (const rows of collections) rows.length = 0; }

export function refreshBackup() {
  const specs = [
    [backupRepositories, CP_STATE_TOPICS.BACKUP_REPOSITORY, ['id', 'repository_id'], sortByNameOrId],
    [backupPolicies, CP_STATE_TOPICS.BACKUP_POLICY, ['id', 'policy_id'], sortByNameOrId],
    [backupRecipes, CP_STATE_TOPICS.BACKUP_RECIPE, ['id', 'recipe_id'], sortByNameOrId],
    [backupDefinitions, CP_STATE_TOPICS.BACKUP_DEFINITION, ['id', 'definition_id'], sortByNameOrId],
    [backupRuns, CP_STATE_TOPICS.BACKUP_RUN, ['id', 'run_id'], sortByNewestField(['created_at', 'started_at'])],
    [backupVerifications, CP_STATE_TOPICS.BACKUP_VERIFICATION, ['id', 'verification_id', 'backup_run_id'], sortByNewestField(['created_at', 'verified_at'])],
    [backupRestores, CP_STATE_TOPICS.BACKUP_RESTORE, ['id', 'restore_id'], sortByNewestField(['created_at', 'started_at'])],
    [backupRetentionRuns, CP_STATE_TOPICS.BACKUP_RETENTION, ['id', 'retention_run_id'], sortByNewestField(['created_at', 'started_at'])],
    [backupRuntimeObservations, CP_STATE_TOPICS.BACKUP_RUNTIME_OBSERVATION, ['id', 'scope'], sortByNewestField(['generated_at', 'updated_at'])]
  ];
  for (const [target, topic, ids, sort] of specs) replaceArray(target, projectTopic(topic, ids).sort(sort));
  const store = getEventStore();
  if (!store) return;
  const servicePubkey = getServicePubkey();
  const attestations = store.query({ kinds: BACKUP_ATTESTATION_KINDS, ...(servicePubkey ? { authors: [servicePubkey] } : {}) })
    .map((event) => {
      if (getTagValue(event, 'deleted') === 'true') return null;
      const content = parseJsonContent(event, {});
      const runId = content.backup_run_id || content.run_id || getTagValue(event, 'run');
      return {
        ...content, id: event.id, nostr_event_id: event.id, kind: event.kind, pubkey: event.pubkey,
        created_at: new Date((event.created_at || 0) * 1000).toISOString(),
        attestation_type: event.kind === BACKUP_RUN_ATTESTATION ? 'run' : 'verification',
        backup_run_id: runId || undefined, run_id: runId || undefined,
        verification_id: content.verification_id || getTagValue(event, 'verification') || undefined,
        status: getTagValue(event, 'status') || content.status,
        artifact_id: getTagValue(event, 'artifact') || content.artifact_id,
        signature: content.signature || event.sig
      };
    }).filter(Boolean).sort(sortByNewestField(['created_at', 'attested_at']));
  replaceArray(backupAttestations, attestations);
}

export function initBackupStoreBinding() { if (unsubscribe) return; refreshBackup(); unsubscribe = onStoreRefresh(refreshBackup); }
export function teardownBackupStoreBinding() { unsubscribe?.(); unsubscribe = null; }
