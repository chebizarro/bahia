import { describe, it, expect, beforeEach, vi } from 'vitest';

const publishRequestMock = vi.hoisted(() => vi.fn());
const awaitResultMock = vi.hoisted(() => vi.fn());
const bootstrapMock = vi.hoisted(() => vi.fn());
const gotoMock = vi.hoisted(() => vi.fn());

vi.mock('$app/navigation', () => ({
  goto: gotoMock
}));

vi.mock('../../src/lib/nostr/controlplane-requests.js', () => ({
  publishRequest: publishRequestMock,
  awaitResult: awaitResultMock
}));

vi.mock('../../src/lib/stores/controlplane.svelte.js', () => ({
  bootstrapControlplane: bootstrapMock
}));

describe('public controlplane command helpers', () => {
  let api;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();
    bootstrapMock.mockResolvedValue({ ok: true });
    publishRequestMock.mockResolvedValue({ requestEventId: 'req-1' });
    awaitResultMock.mockResolvedValue({
      id: 'result-1',
      kind: 7963,
      tags: [['e', 'req-1']],
      content: JSON.stringify({ status: 'ok' })
    });
    api = await import('../../src/lib/stores/public-controlplane.svelte.js');
  });

  it('creates services through signer-first public request/result helpers', async () => {
    const payload = {
      name: 'api',
      repo_url: '',
      artifact_repo: 'ghcr.io/example/api',
      runtime_type: 'docker',
      default_branch: 'main'
    };

    await api.createService(payload);

    expect(bootstrapMock).toHaveBeenCalledTimes(1);
    expect(publishRequestMock).toHaveBeenCalledWith({
      kind: 5964,
      tags: [],
      content: payload
    });
    expect(awaitResultMock).toHaveBeenCalledWith({
      requestEventId: 'req-1',
      resultKinds: [7963, 7961]
    });
  });

  it('creates deployment intents with service/environment/artifact routing tags', async () => {
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1');

    expect(publishRequestMock).toHaveBeenCalledWith({
      kind: 5961,
      tags: [['service', 'svc-1'], ['environment', 'env-1'], ['artifact', 'artifact-1']],
      content: {
        service_id: 'svc-1',
        environment_id: 'env-1',
        artifact_id: 'artifact-1'
      }
    });
    expect(awaitResultMock).toHaveBeenCalledWith({
      requestEventId: 'req-1',
      resultKinds: [7961]
    });
  });

  it('approves deployment intents through signer-first approval requests', async () => {
    await api.approveDeploymentIntent('intent-1');

    expect(publishRequestMock).toHaveBeenCalledWith({
      kind: 5966,
      tags: [['intent', 'intent-1'], ['decision', 'approve']],
      content: { intent_id: 'intent-1', decision: 'approve' }
    });
    expect(awaitResultMock).toHaveBeenCalledWith({
      requestEventId: 'req-1',
      resultKinds: [7962, 7961, 7963, 7964]
    });
  });

  it('evaluates deployment policy through signer-first requests and returns parsed payload', async () => {
    awaitResultMock.mockResolvedValueOnce({
      id: 'result-policy',
      kind: 7962,
      tags: [['e', 'req-1']],
      content: JSON.stringify({
        allowed: true,
        warnings: 0,
        blockers: 0,
        results: [{ policy_id: 'sig-required', passed: true }]
      })
    });

    const result = await api.evaluatePolicy({ artifact_id: 'artifact-1', environment_id: 'env-1' });

    expect(publishRequestMock).toHaveBeenCalledWith({
      kind: 5989,
      tags: [['artifact', 'artifact-1'], ['environment', 'env-1']],
      content: { artifact_id: 'artifact-1', environment_id: 'env-1' }
    });
    expect(result).toMatchObject({
      allowed: true,
      warnings: 0,
      blockers: 0,
      results: [{ policy_id: 'sig-required', passed: true }]
    });
  });

  it('surfaces terminal error results from public command replies', async () => {
    awaitResultMock.mockResolvedValueOnce({
      id: 'result-error',
      kind: 7961,
      tags: [['e', 'req-1'], ['status', 'failed'], ['error', 'policy blocked']],
      content: JSON.stringify({ status: 'failed', error: 'policy blocked' })
    });

    await expect(api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1')).rejects.toThrow('policy blocked');
  });
});
