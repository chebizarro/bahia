import { KINDS } from '$lib/nostr/client.js';

export const SCHEDULING_STATES = ['active', 'cordoned', 'draining', 'maintenance', 'disabled'];

export const WORKER_KINDS = {
  CLEANUP_REQUEST: KINDS.BAHIA_REQUEST_WORKER_CLEANUP,
  CORDON_REQUEST: KINDS.BAHIA_REQUEST_WORKER_CORDON,
  UNCORDON_REQUEST: KINDS.BAHIA_REQUEST_WORKER_UNCORDON,
  DRAIN_REQUEST: KINDS.BAHIA_REQUEST_WORKER_DRAIN,
  UNDRAIN_REQUEST: KINDS.BAHIA_REQUEST_WORKER_UNDRAIN,
  MAINTENANCE_ENTER_REQUEST: KINDS.BAHIA_REQUEST_WORKER_MAINTENANCE_ENTER,
  MAINTENANCE_EXIT_REQUEST: KINDS.BAHIA_REQUEST_WORKER_MAINTENANCE_EXIT,
  LABELS_UPDATE_REQUEST: KINDS.BAHIA_REQUEST_WORKER_LABELS_UPDATE,
  RESULT: KINDS.BAHIA_WORKER_RESULT
};

export const WORKER_COMMANDS = {
  CLEANUP_REQUEST: 'worker.cleanup.request',
  CORDON: 'worker.cordon.request',
  UNCORDON: 'worker.uncordon.request',
  DRAIN: 'worker.drain.request',
  UNDRAIN: 'worker.undrain.request',
  MAINTENANCE_ENTER: 'worker.maintenance.enter.request',
  MAINTENANCE_EXIT: 'worker.maintenance.exit.request',
  LABELS_UPDATE: 'worker.labels.update.request'
};

export function workerCommandTags(action, worker, key) {
  return [
    ['d', key],
    ['worker', worker.pubkey],
    ['command', action.command]
  ];
}

export function workerCommandContent(action, worker, key, reason, requesterPubkey, labels = null, cleanupMode = null) {
  const content = {
    worker_pubkey: worker.pubkey,
    reason: reason || '',
    idempotency_key: key,
    operator_metadata: {
      source: 'web.workers.list',
      requested_by: requesterPubkey || ''
    }
  };
  if (labels) content.labels = labels;
  if (cleanupMode) content.cleanup_mode = cleanupMode;
  return content;
}

export function workerCommandPublishPayload({ action, worker, key, reason = '', requesterPubkey = '', labels = null, cleanupMode = null }) {
  return {
    kind: action.kind,
    tags: workerCommandTags(action, worker, key),
    content: workerCommandContent(action, worker, key, reason, requesterPubkey, labels, cleanupMode),
    resultKinds: [WORKER_KINDS.RESULT]
  };
}
