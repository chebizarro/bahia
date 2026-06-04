export const SCHEDULING_STATES = ['active', 'cordoned', 'draining', 'maintenance', 'disabled'];

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

export const WORKER_CONTEXTVM_OPERATIONS = {
  [WORKER_COMMANDS.CLEANUP_REQUEST]: 'worker/cleanup',
  [WORKER_COMMANDS.CORDON]: 'worker/cordon',
  [WORKER_COMMANDS.UNCORDON]: 'worker/uncordon',
  [WORKER_COMMANDS.DRAIN]: 'worker/drain',
  [WORKER_COMMANDS.UNDRAIN]: 'worker/undrain',
  [WORKER_COMMANDS.MAINTENANCE_ENTER]: 'worker/maintenance-enter',
  [WORKER_COMMANDS.MAINTENANCE_EXIT]: 'worker/maintenance-exit',
  [WORKER_COMMANDS.LABELS_UPDATE]: 'worker/labels-update'
};

export function workerOperation(action) {
  const operation = WORKER_CONTEXTVM_OPERATIONS[action?.command];
  if (!operation) throw new Error(`Unsupported worker command ${action?.command || ''}`.trim());
  return operation;
}

export function workerCommandTags(action, worker, key) {
  return [
    ['d', key],
    ['worker', worker.pubkey],
    ['command', action.command]
  ];
}

export function workerCommandContent(action, worker, key, reason, requesterPubkey, labels = null, cleanupMode = null, source = 'web.workers.list') {
  const content = {
    worker_pubkey: worker.pubkey,
    reason: reason || '',
    idempotency_key: key,
    operator_metadata: {
      source: source || 'web.workers.list',
      requested_by: requesterPubkey || ''
    }
  };
  if (labels) content.labels = labels;
  if (cleanupMode) content.cleanup_mode = cleanupMode;
  return content;
}

export function workerCommandPublishPayload({ action, worker, key, reason = '', requesterPubkey = '', labels = null, cleanupMode = null, source = 'web.workers.list' }) {
  return {
    operation: workerOperation(action),
    tags: workerCommandTags(action, worker, key),
    content: workerCommandContent(action, worker, key, reason, requesterPubkey, labels, cleanupMode, source)
  };
}
