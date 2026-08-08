import { describe, expect, it } from 'vitest';
import {
  approvalOutcome,
  operationsForDeploymentRun,
  pendingDeploymentIntents,
  projectLiveDeploymentRun
} from '../../src/routes/operational-activity.js';

describe('live operational activity projections', () => {
  it('projects a deployment 6961 stream and terminal 7961 result onto the run', () => {
    const run = {
      id: 'run-1',
      deployment_intent_id: 'intent-1',
      status: 'queued',
      phases: []
    };
    const operations = [
      {
        operation_id: 'request-1',
        domain: 'deployment',
        entity_refs: { run_id: 'run-1', intent_id: 'intent-1' },
        status: 'succeeded',
        success: true,
        step: 'health-check',
        message: 'Deployment healthy',
        terminal: true,
        status_at: '2026-08-08T10:01:00.000Z',
        completed_at: '2026-08-08T10:02:00.000Z',
        result_event: { content: { status: 'succeeded' } }
      },
      {
        operation_id: 'request-2',
        domain: 'service',
        entity_refs: { service_id: 'service-1' },
        status: 'running',
        status_at: '2026-08-08T10:03:00.000Z'
      }
    ];

    expect(operationsForDeploymentRun(operations, run)).toHaveLength(1);
    expect(projectLiveDeploymentRun(run, operations)).toMatchObject({
      status: 'succeeded',
      current_step: 'health-check',
      status_message: 'Deployment healthy',
      finished_at: '2026-08-08T10:02:00.000Z'
    });
  });

  it('removes a pending intent when a live approval result arrives', () => {
    const intent = { id: 'intent-1', approval_status: 'pending' };
    const result = {
      operation_id: 'approve-request',
      domain: 'action',
      operation: 'deployments.approve',
      entity_refs: { intent_id: 'intent-1' },
      terminal: true,
      success: true,
      result_event: { content: {} }
    };

    expect(approvalOutcome(result)).toBe('approved');
    expect(pendingDeploymentIntents([intent], [result])).toEqual([]);
  });
});
