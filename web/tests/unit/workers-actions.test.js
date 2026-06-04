import { describe, expect, it } from 'vitest';
import { WORKER_COMMANDS, WORKER_CONTEXTVM_OPERATIONS, workerCommandPublishPayload } from '../../src/routes/workers/actions.js';

describe('workers action publish payloads', () => {
  it('publishes cleanup requests through the canonical ContextVM worker operation with cleanup tags and content', () => {
    const worker = { pubkey: 'worker-pubkey-1' };
    const action = {
      command: WORKER_COMMANDS.CLEANUP_REQUEST
    };

    expect(WORKER_CONTEXTVM_OPERATIONS[WORKER_COMMANDS.CLEANUP_REQUEST]).toBe('worker/cleanup');

    expect(workerCommandPublishPayload({
      action,
      worker,
      key: 'worker.cleanup.request:worker-pubkey-1:request-1',
      reason: 'reclaim disk pressure',
      requesterPubkey: 'operator-pubkey-1',
      cleanupMode: 'reclaimable_only'
    })).toEqual({
      operation: 'worker/cleanup',
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
      }
    });
  });

  it('allows cleanup requests to identify the publishing UI surface', () => {
    const worker = { pubkey: 'worker-pubkey-1' };
    const action = { command: WORKER_COMMANDS.CLEANUP_REQUEST };

    expect(workerCommandPublishPayload({
      action,
      worker,
      key: 'cleanup-key',
      requesterPubkey: 'operator-pubkey-1',
      cleanupMode: 'aggressive',
      source: 'web.fleet-health'
    }).content.operator_metadata).toEqual({
      source: 'web.fleet-health',
      requested_by: 'operator-pubkey-1'
    });
  });
});
