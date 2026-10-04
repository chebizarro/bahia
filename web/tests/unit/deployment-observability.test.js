import { describe, expect, it } from 'vitest';
import { deploymentCanRollback, deploymentHealthFailed } from '../../src/lib/deployment-observability.js';
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
