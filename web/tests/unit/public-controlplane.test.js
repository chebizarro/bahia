import { describe, it, expect, beforeEach, vi } from 'vitest';

const requestResultMock = vi.hoisted(() => vi.fn());
const bootstrapMock = vi.hoisted(() => vi.fn());
const gotoMock = vi.hoisted(() => vi.fn());

vi.mock('$app/navigation', () => ({
  goto: gotoMock
}));

vi.mock('../../src/lib/nostr/controlplane-requests.js', () => ({
  requestResult: requestResultMock
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
    requestResultMock.mockResolvedValue({
      requestEventId: 'req-1',
      resultEvent: {
        id: 'result-1',
        kind: 7963,
        tags: [['e', 'req-1']],
        content: JSON.stringify({ status: 'ok' })
      }
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
    expect(requestResultMock).toHaveBeenCalledWith({
      kind: 5964,
      tags: [],
      content: payload,
      resultKinds: [7963, 7961]
    });
  });

  it('creates deployment intents with service/environment/artifact routing tags', async () => {
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1');

    expect(requestResultMock).toHaveBeenCalledWith({
      kind: 5961,
      tags: [['service', 'svc-1'], ['environment', 'env-1'], ['artifact', 'artifact-1']],
      content: {
        service_id: 'svc-1',
        environment_id: 'env-1',
        artifact_id: 'artifact-1'
      },
      resultKinds: [7961]
    });
  });

  it('approves deployment intents through signer-first approval requests', async () => {
    await api.approveDeploymentIntent('intent-1');

    expect(requestResultMock).toHaveBeenCalledWith({
      kind: 5966,
      tags: [['intent', 'intent-1'], ['decision', 'approve']],
      content: { intent_id: 'intent-1', decision: 'approve' },
      resultKinds: [7962, 7961, 7963, 7964]
    });
  });

  it('creates LLM routes through canonical signer-first route-create requests', async () => {
    const payload = {
      name: 'chat-prod',
      description: 'Public chat completions route',
      gateway_config: {
        public_model: 'bahia/chat',
        path: '/v1/models/chat-prod'
      }
    };

    await api.createLLMRoute(payload);

    expect(requestResultMock).toHaveBeenCalledWith({
      kind: 5971,
      tags: [],
      content: payload,
      resultKinds: [7971]
    });
  });

  it('registers LLM releases through canonical signer-first release-register requests', async () => {
    const payload = {
      route_id: 'llm-route-1',
      version: 'v1',
      model_ref: 'hf://meta-llama/Llama-3',
      model_source: 'huggingface',
      backend_preferences: ['external_api'],
      external_backend: { base_url: 'https://llm.example.com' }
    };

    await api.registerLLMRelease(payload);

    expect(requestResultMock).toHaveBeenCalledWith({
      kind: 5972,
      tags: [['route', 'llm-route-1']],
      content: payload,
      resultKinds: [7972]
    });
  });

  it('requests LLM deploys through canonical signer-first deploy requests and awaits terminal result responses', async () => {
    requestResultMock.mockResolvedValueOnce({
      requestEventId: 'req-1',
      resultEvent: {
        id: 'result-llm-deploy',
        kind: 7973,
        tags: [['e', 'req-1'], ['status', 'success'], ['step', 'completed']],
        content: JSON.stringify({ status: 'success', step: 'completed', message: 'completed' })
      }
    });

    const result = await api.requestLLMDeploy({
      route_id: 'llm-route-1',
      environment_id: 'env-prod',
      release_id: 'llm-release-1',
      requested_by: 'f'.repeat(64)
    });

    expect(requestResultMock).toHaveBeenCalledWith({
      kind: 5973,
      tags: [['route', 'llm-route-1'], ['environment', 'env-prod'], ['release', 'llm-release-1']],
      content: {
        route_id: 'llm-route-1',
        environment_id: 'env-prod',
        release_id: 'llm-release-1',
        requested_by: 'f'.repeat(64)
      },
      resultKinds: [7973]
    });
    expect(result).toMatchObject({
      requestEventId: 'req-1',
      event: {
        id: 'result-llm-deploy',
        kind: 7973
      }
    });
  });

  it('requests LLM rollback through canonical signer-first rollback requests and awaits terminal result responses', async () => {
    requestResultMock.mockResolvedValueOnce({
      requestEventId: 'req-1',
      resultEvent: {
        id: 'result-llm-rollback',
        kind: 7973,
        tags: [['e', 'req-1'], ['status', 'success'], ['step', 'completed']],
        content: JSON.stringify({ status: 'success', step: 'completed', message: 'rollback completed' })
      }
    });

    const result = await api.requestLLMRollback({
      route_id: 'llm-route-1',
      environment_id: 'env-prod',
      requested_by: 'f'.repeat(64)
    });

    expect(requestResultMock).toHaveBeenCalledWith({
      kind: 5975,
      tags: [['route', 'llm-route-1'], ['environment', 'env-prod']],
      content: {
        route_id: 'llm-route-1',
        environment_id: 'env-prod',
        requested_by: 'f'.repeat(64)
      },
      resultKinds: [7973]
    });
    expect(result).toMatchObject({
      requestEventId: 'req-1',
      event: {
        id: 'result-llm-rollback',
        kind: 7973
      }
    });
  });

  it('approves LLM deployment intents through signer-first approval requests', async () => {
    await api.approveLLMDeploymentIntent('llm-intent-1');

    expect(requestResultMock).toHaveBeenCalledWith({
      kind: 5974,
      tags: [['intent', 'llm-intent-1'], ['decision', 'approve']],
      content: { intent_id: 'llm-intent-1', decision: 'approve' },
      resultKinds: [7973]
    });
  });

  it('evaluates deployment policy through signer-first requests and returns parsed payload', async () => {
    requestResultMock.mockResolvedValueOnce({
      requestEventId: 'req-1',
      resultEvent: {
        id: 'result-policy',
        kind: 7962,
        tags: [['e', 'req-1']],
        content: JSON.stringify({
          allowed: true,
          warnings: 0,
          blockers: 0,
          results: [{ policy_id: 'sig-required', passed: true }]
        })
      }
    });

    const result = await api.evaluatePolicy({ artifact_id: 'artifact-1', environment_id: 'env-1' });

    expect(requestResultMock).toHaveBeenCalledWith({
      kind: 5989,
      tags: [['artifact', 'artifact-1'], ['environment', 'env-1']],
      content: { artifact_id: 'artifact-1', environment_id: 'env-1' },
      resultKinds: [7962, 7961]
    });
    expect(result).toMatchObject({
      allowed: true,
      warnings: 0,
      blockers: 0,
      results: [{ policy_id: 'sig-required', passed: true }]
    });
  });

  it('surfaces terminal error results from public command replies', async () => {
    requestResultMock.mockResolvedValueOnce({
      requestEventId: 'req-1',
      resultEvent: {
        id: 'result-error',
        kind: 7961,
        tags: [['e', 'req-1'], ['status', 'failed'], ['error', 'policy blocked']],
        content: JSON.stringify({ status: 'failed', error: 'policy blocked' })
      }
    });

    await expect(api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1')).rejects.toThrow('policy blocked');
  });
});
