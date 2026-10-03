import { beforeEach, describe, expect, it } from 'vitest';
import { deploymentCanRollback, deploymentHealthFailed } from '../../src/lib/deployment-observability.js';
import {
  deploymentApplicators,
  deploymentIntents,
  refreshDeployments,
  resetDeployments,
  states
} from '../../src/lib/stores/collections/deployments.svelte.js';

function event({ id, d, createdAt, content, tags = [] }) {
  return {
    id,
    kind: 30900,
    pubkey: 'operator-projector',
    created_at: createdAt,
    tags: [['d', d], ...tags],
    content: JSON.stringify(content)
  };
}

describe('deployment rollback eligibility', () => {
  it('offers rollback when health times out while the last observation is still starting', () => {
    expect(deploymentCanRollback(
      { id: 'failed-intent' },
      { id: 'previous-healthy-intent' },
      {
        status: 'failed',
        health_status: 'starting',
        failure: { code: 'health_check_timeout' }
      },
      { health_status: 'starting' }
    )).toBe(true);
  });

  it('does not offer rollback for a non-terminal starting deployment', () => {
    expect(deploymentHealthFailed({
      status: 'running',
      health_status: 'starting'
    }, {
      health_status: 'starting'
    })).toBe(false);
  });
});

describe('deployment projection convergence', () => {
  let replaceableEvents;

  beforeEach(() => {
    resetDeployments();
    replaceableEvents = new Map();
  });

  it('rejects a delayed duplicate intent projection even when relay time is newer', () => {
    const current = event({
      id: 'b',
      d: 'intent-1',
      createdAt: 10,
      content: { id: 'intent-1', status: 'deployed', updated_at: '2026-08-02T10:00:00Z' }
    });
    const delayed = event({
      id: 'z',
      d: 'legacy:intent-1',
      createdAt: 20,
      content: { id: 'intent-1', status: 'pending', updated_at: '2026-08-02T09:00:00Z' }
    });

    deploymentApplicators.intent(current, replaceableEvents);
    expect(deploymentApplicators.intent(delayed, replaceableEvents)).toBe(false);
    refreshDeployments();

    expect(deploymentIntents).toHaveLength(1);
    expect(deploymentIntents[0].status).toBe('deployed');
  });
});
