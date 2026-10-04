import { describe, expect, it } from 'vitest';
import { WORKER_COMMANDS, workerOperation } from '../../src/routes/workers/actions.js';

describe('workers action intent operations', () => {
  it('maps every supported worker action to its daemon intent op', () => {
    const expected = {
      CLEANUP_REQUEST: 'cleanup', CORDON: 'cordon', UNCORDON: 'uncordon',
      DRAIN: 'drain', UNDRAIN: 'undrain', MAINTENANCE_ENTER: 'maintenance-enter',
      MAINTENANCE_EXIT: 'maintenance-exit', LABELS_UPDATE: 'labels-update'
    };
    for (const [key, op] of Object.entries(expected)) {
      expect(workerOperation({ command: WORKER_COMMANDS[key] })).toBe(op);
    }
    expect(() => workerOperation({ command: 'worker.unknown' })).toThrow(/Unsupported/);
  });
});
