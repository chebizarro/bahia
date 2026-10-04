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

const WORKER_INTENT_OPERATIONS = {
  [WORKER_COMMANDS.CLEANUP_REQUEST]: 'cleanup',
  [WORKER_COMMANDS.CORDON]: 'cordon',
  [WORKER_COMMANDS.UNCORDON]: 'uncordon',
  [WORKER_COMMANDS.DRAIN]: 'drain',
  [WORKER_COMMANDS.UNDRAIN]: 'undrain',
  [WORKER_COMMANDS.MAINTENANCE_ENTER]: 'maintenance-enter',
  [WORKER_COMMANDS.MAINTENANCE_EXIT]: 'maintenance-exit',
  [WORKER_COMMANDS.LABELS_UPDATE]: 'labels-update'
};

export function workerOperation(action) {
  const op = WORKER_INTENT_OPERATIONS[action?.command];
  if (!op) throw new Error(`Unsupported worker action ${action?.command || ''}`.trim());
  return op;
}
