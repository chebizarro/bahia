import { beforeEach, describe, expect, it } from 'vitest';
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

describe('deployment projection convergence', () => {
  let replaceableEvents;

  beforeEach(() => {
    resetDeployments();
    replaceableEvents = new Map();
  });

  it('merges legacy and corrected service-state coordinates by logical scope and domain time', () => {
    const newer = event({
      id: 'b',
      d: 'service:svc:environment:env',
      createdAt: 10,
      tags: [['service', 'svc'], ['environment', 'env']],
      content: {
        service_id: 'svc',
        environment_id: 'env',
        drift_status: 'in_sync',
        health_status: 'healthy',
        updated_at: '2026-08-02T10:00:00Z'
      }
    });
    const delayedLegacy = event({
      id: 'z',
      d: 'service:svc',
      createdAt: 20,
      tags: [['service', 'svc'], ['environment', 'env']],
      content: {
        service_id: 'svc',
        environment_id: 'env',
        drift_status: 'deploying',
        health_status: 'starting',
        updated_at: '2026-08-02T09:59:00Z'
      }
    });

    expect(deploymentApplicators.serviceState(newer, replaceableEvents)).toBe(true);
    expect(deploymentApplicators.serviceState(delayedLegacy, replaceableEvents)).toBe(false);
    refreshDeployments();

    expect(states).toHaveLength(1);
    expect(states[0]).toMatchObject({
      id: 'svc:env',
      service_id: 'svc',
      environment_id: 'env',
      drift_status: 'in_sync',
      health_status: 'healthy'
    });
  });

  it('keeps a logical tombstone watermark so reconnect replay cannot resurrect stale state', () => {
    const live = event({
      id: 'a',
      d: 'service:svc',
      createdAt: 10,
      tags: [['service', 'svc'], ['environment', 'env']],
      content: { service_id: 'svc', environment_id: 'env', updated_at: '2026-08-02T10:00:00Z' }
    });
    const tombstone = event({
      id: 'b',
      d: 'service:svc:environment:env',
      createdAt: 11,
      tags: [['service', 'svc'], ['environment', 'env'], ['deleted', 'true']],
      content: { deleted: true, service_id: 'svc', environment_id: 'env', updated_at: '2026-08-02T10:01:00Z' }
    });

    deploymentApplicators.serviceState(live, replaceableEvents);
    deploymentApplicators.serviceState(tombstone, replaceableEvents);
    expect(deploymentApplicators.serviceState(live, replaceableEvents)).toBe(false);
    refreshDeployments();

    expect(states).toEqual([]);
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
