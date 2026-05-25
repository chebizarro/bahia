import { describe, expect, it } from 'vitest';
import { WORKER_COMMANDS, WORKER_KINDS, workerCommandPublishPayload } from '../../src/routes/workers/actions.js';

describe('workers action publish payloads', () => {
  it('publishes cleanup requests on Bahia control-plane kind 6006 with cleanup tags and content', () => {
    const worker = { pubkey: 'worker-pubkey-1' };
    const action = {
      command: WORKER_COMMANDS.CLEANUP_REQUEST,
      kind: WORKER_KINDS.CLEANUP_REQUEST
    };

    expect(WORKER_KINDS.CLEANUP_REQUEST).toBe(6006);

    expect(workerCommandPublishPayload({
      action,
      worker,
      key: 'worker.cleanup.request:worker-pubkey-1:request-1',
      reason: 'reclaim disk pressure',
      requesterPubkey: 'operator-pubkey-1',
      cleanupMode: 'reclaimable_only'
    })).toEqual({
      kind: 6006,
      tags: [
        ['d', 'worker.cleanup.request:worker-pubkey-1:request-1'],
        ['worker', 'worker-pubkey-1'],
        ['command', 'worker.cleanup.request']
      ],
      content: {
        worker_pubkey: 'worker-pubkey-1',
        reason: 'reclaim disk pressure',
        idempotency_key: 'worker.cleanup.request:worker-pubkey-1:request-1',
        cleanup_mode: 'reclaimable_only',
        operator_metadata: {
          source: 'web.workers.list',
          requested_by: 'operator-pubkey-1'
        }
      },
      resultKinds: [7997]
    });
  });
});
