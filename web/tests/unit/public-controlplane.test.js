import { describe, it, expect, beforeEach, vi } from 'vitest';

const requestEncryptedResultMock = vi.hoisted(() => vi.fn());
const publishEncryptedRequestMock = vi.hoisted(() => vi.fn());
const bootstrapMock = vi.hoisted(() => vi.fn());
const gotoMock = vi.hoisted(() => vi.fn());

vi.mock('$app/navigation', () => ({
  goto: gotoMock
}));

vi.mock('$lib/nostr/encrypted-controlplane.js', () => ({
  requestEncryptedResult: requestEncryptedResultMock,
  publishEncryptedRequest: publishEncryptedRequestMock
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
    publishEncryptedRequestMock.mockResolvedValue({
      requestEventId: 'req-1',
      event: { id: 'req-1' },
      ok: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      acceptedRelays: [{ relay: 'ws://relay.test', sent: true, accepted: true, message: '' }],
      rejectedRelays: []
    });
    requestEncryptedResultMock.mockResolvedValue({
      requestEventId: 'req-1',
      result: { status: 'ok' }
    });
    api = await import('../../src/lib/stores/public-controlplane.svelte.js');
  });

  it('creates services through canonical ContextVM encrypted requests', async () => {
    const payload = {
      name: 'api',
      repo_url: '',
      artifact_repo: 'ghcr.io/example/api',
      runtime_type: 'docker',
      default_branch: 'main'
    };

    await api.createService(payload);

    expect(bootstrapMock).toHaveBeenCalledTimes(1);
    expect(requestEncryptedResultMock).toHaveBeenCalledWith({
      operation: 'service/create',
      payload,
      tags: [],
      signal: undefined,
      timeoutMs: undefined
    });
  });

  it('creates deployment intents with service/environment/artifact routing tags', async () => {
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1');

    expect(requestEncryptedResultMock).toHaveBeenCalledWith({
      operation: 'service/deploy',
      tags: [['service', 'svc-1'], ['environment', 'env-1'], ['artifact', 'artifact-1']],
      payload: {
        service_id: 'svc-1',
        environment_id: 'env-1',
        artifact_id: 'artifact-1'
      },
      signal: undefined,
      timeoutMs: undefined
    });
  });

  it('approves deployment intents through canonical ContextVM approval requests', async () => {
    await api.approveDeploymentIntent('intent-1');

    expect(requestEncryptedResultMock).toHaveBeenCalledWith({
      operation: 'approval/approve',
      tags: [['intent', 'intent-1'], ['decision', 'approve']],
      payload: { intent_id: 'intent-1', decision: 'approve' },
      signal: undefined,
      timeoutMs: undefined
    });
  });

  it('creates LLM routes and releases through canonical ContextVM operations', async () => {
    const routePayload = {
      name: 'chat-prod',
      description: 'Public chat completions route',
      gateway_config: {
        public_model: 'bahia/chat',
        path: '/v1/models/chat-prod'
      }
    };
    await api.createLLMRoute(routePayload);
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith({
      operation: 'llm/route-create',
      tags: [],
      payload: routePayload,
      signal: undefined,
      timeoutMs: undefined
    });

    const releasePayload = {
      route_id: 'llm-route-1',
      version: 'v1',
      model_ref: 'hf://meta-llama/Llama-3',
      model_source: 'huggingface'
    };
    await api.registerLLMRelease(releasePayload);
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith({
      operation: 'llm/release-register',
      tags: [['route', 'llm-route-1']],
      payload: releasePayload,
      signal: undefined,
      timeoutMs: undefined
    });
  });

  it('requests LLM deploys and rollbacks through canonical ContextVM lifecycle methods', async () => {
    requestEncryptedResultMock.mockResolvedValueOnce({
      requestEventId: 'req-1',
      resultEvent: {
        id: 'result-llm-deploy',
        kind: 25910,
        tags: [['e', 'req-1'], ['status', 'success'], ['step', 'completed']],
        content: JSON.stringify({ status: 'success', step: 'completed', message: 'completed' })
      }
    });

    const deployResult = await api.requestLLMDeploy({
      route_id: 'llm-route-1',
      environment_id: 'env-prod',
      release_id: 'llm-release-1',
      requested_by: 'f'.repeat(64)
    });

    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith({
      operation: 'llm/deploy',
      tags: [['route', 'llm-route-1'], ['environment', 'env-prod'], ['release', 'llm-release-1']],
      payload: {
        route_id: 'llm-route-1',
        environment_id: 'env-prod',
        release_id: 'llm-release-1',
        requested_by: 'f'.repeat(64)
      }
    });
    expect(deployResult).toMatchObject({
      requestEventId: 'req-1',
      event: { id: 'result-llm-deploy', kind: 25910 }
    });

    requestEncryptedResultMock.mockResolvedValueOnce({
      requestEventId: 'req-2',
      resultEvent: {
        id: 'result-llm-rollback',
        kind: 25910,
        tags: [['e', 'req-2'], ['status', 'success'], ['step', 'completed']],
        content: JSON.stringify({ status: 'success', step: 'completed', message: 'rollback completed' })
      }
    });

    const rollbackResult = await api.requestLLMRollback({
      route_id: 'llm-route-1',
      environment_id: 'env-prod',
      requested_by: 'f'.repeat(64)
    });
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith({
      operation: 'llm/rollback',
      tags: [['route', 'llm-route-1'], ['environment', 'env-prod']],
      payload: {
        route_id: 'llm-route-1',
        environment_id: 'env-prod',
        requested_by: 'f'.repeat(64)
      }
    });
    expect(rollbackResult).toMatchObject({ requestEventId: 'req-2', event: { id: 'result-llm-rollback', kind: 25910 } });
  });

  it('generates artifact SBOMs through canonical ContextVM encrypted requests', async () => {
    await api.generateArtifactSBOM({
      id: 'artifact-1',
      name: 'registry.example.com/acme/api',
      image_repo: 'registry.example.com/acme/api',
      image_tag: '1.2.3',
      digest: 'sha256:abc123'
    });

    expect(publishEncryptedRequestMock).toHaveBeenCalledWith({
      operation: 'sbom/generate',
      tags: [
        ['domain', 'sbom'],
        ['operation', 'sbom/generate'],
        ['subject_type', 'artifact'],
        ['artifact', 'artifact-1'],
        ['subject', 'sha256:abc123'],
        ['generator', 'syft']
      ],
      payload: {
        idempotencyKey: 'web.sbom.generate:artifact:artifact-1:sha256:abc123:spdx,cyclonedx:syft',
        subject: {
          type: 'artifact',
          id: 'artifact-1',
          display_name: 'registry.example.com/acme/api',
          digest: 'sha256:abc123'
        },
        source: {
          kind: 'oci-image',
          locator: 'registry.example.com/acme/api@sha256:abc123'
        },
        formats: ['spdx', 'cyclonedx'],
        generator: 'syft',
        storage: 'blossom'
      },
      signal: undefined
    });
  });

  it('imports artifact SBOMs through publish-only ContextVM encrypted requests', async () => {
    await api.importArtifactSBOM({
      id: 'artifact-1',
      name: 'registry.example.com/acme/api',
      digest: 'sha256:abc123'
    }, {
      format: 'cyclonedx',
      payloadBase64: 'eyJib21Gb3JtYXQiOiAiQ3ljbG9uZURYIn0=',
      generator: { id: 'external-tool', version: '1.0.0' }
    });

    expect(publishEncryptedRequestMock).toHaveBeenCalledWith({
      operation: 'sbom/import',
      tags: [
        ['domain', 'sbom'],
        ['operation', 'sbom/import'],
        ['subject_type', 'artifact'],
        ['artifact', 'artifact-1'],
        ['subject', 'sha256:abc123'],
        ['format', 'cyclonedx'],
        ['generator', 'external-tool']
      ],
      payload: {
        idempotencyKey: 'web.sbom.import:artifact:artifact-1:sha256:abc123:cyclonedx:inline:26:eyJib21Gb3JtYXQiOiAiQ3lj:YXQiOiAiQ3ljbG9uZURYIn0=:external-tool',
        subject: {
          type: 'artifact',
          id: 'artifact-1',
          display_name: 'registry.example.com/acme/api',
          digest: 'sha256:abc123'
        },
        format: 'cyclonedx',
        payloadBase64: 'eyJib21Gb3JtYXQiOiAiQ3ljbG9uZURYIn0=',
        storage: 'blossom',
        generator: { id: 'external-tool', version: '1.0.0' }
      },
      signal: undefined
    });
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('rejects oversized inline artifact SBOM imports before publishing', () => {
    const oversized = 'a'.repeat(Math.ceil(((api.MAX_CONTEXTVM_INLINE_SBOM_BYTES + 1) * 4) / 3));
    expect(() => api.importArtifactSBOM({ id: 'artifact-1', digest: 'sha256:abc123' }, { format: 'spdx', payloadBase64: oversized })).toThrow('use a Blossom or REST compatibility import reference');
    expect(publishEncryptedRequestMock).not.toHaveBeenCalled();
  });

  it('rejects artifact SBOM generation without an immutable digest', () => {
    expect(() => api.generateArtifactSBOM({ id: 'artifact-1', image_repo: 'registry.example.com/acme/api' })).toThrow('artifact digest is required');
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('does not use artifact display names as OCI image locators', () => {
    expect(() => api.generateArtifactSBOM({
      id: 'artifact-1',
      name: 'nostrodomo',
      digest: 'sha256:abc123'
    })).toThrow('artifact image repository or OCI image ref is required');
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('uses service artifact repositories when artifact projections omit image_repo', async () => {
    await api.generateArtifactSBOM({
      id: 'artifact-1',
      name: 'nostrodomo',
      service_artifact_repo: 'ghcr.io/example/nostrodomo',
      image_tag: '2026.06.14',
      digest: 'sha256:abc123'
    });

    expect(publishEncryptedRequestMock).toHaveBeenCalledWith(expect.objectContaining({
      operation: 'sbom/generate',
      payload: expect.objectContaining({
        source: { kind: 'oci-image', locator: 'ghcr.io/example/nostrodomo@sha256:abc123' }
      })
    }));
  });

  it('publishes backup mutation and operation helpers through registered ContextVM methods', async () => {
    await api.registerBackupRepository({ name: 'archive', backend: 'kopia', repository_uri: 'kopia://archive', idempotency_key: 'repo-1' });
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({
      operation: 'backup/repository-register',
      tags: expect.arrayContaining([['d', 'repo-1'], ['repository', 'archive']]),
      payload: expect.objectContaining({ name: 'archive', backend: 'kopia', repository_uri: 'kopia://archive', idempotency_key: 'repo-1' })
    }));

    await api.applyBackupPolicy({ name: 'verified', require_verification: true, verification_mode: 'kopia_snapshot_verify', idempotency_key: 'policy-1' });
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({ operation: 'backup/policy-apply' }));

    await api.applyBackupRecipe({ name: 'daily', version: 'v1', backend: 'kopia', repository_id: 'repo-id', target_ref: 'fs:/srv/app', idempotency_key: 'recipe-1' });
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({ operation: 'backup/recipe-apply' }));

    await api.applyBackupDefinition({ name: 'daily-app', repository_id: 'repo-id', policy_id: 'policy-id', recipe_id: 'recipe-id', idempotency_key: 'definition-1' });
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({ operation: 'backup/definition-apply' }));

    await api.requestBackupRun({ id: 'recipe-id', name: 'daily', version: 'v1' });
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({ operation: 'backup/run' }));

    await api.requestBackupVerification({ id: 'run-id', verification_mode: 'kopia_snapshot_verify' });
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({ operation: 'backup/verification' }));

    await api.requestBackupRestore({ id: 'run-id' }, 'fs:/restore');
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({ operation: 'backup/restore' }));

    await api.requestBackupRetention({ repository_id: 'repo-id', policy_id: 'policy-id', dry_run: true });
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({ operation: 'backup/retention' }));
  });

  it('evaluates deployment policy through ContextVM and unwraps successful payload envelopes', async () => {
    requestEncryptedResultMock.mockResolvedValueOnce({
      requestEventId: 'req-1',
      result: {
        status: 'success',
        payload: {
          allowed: true,
          warnings: 0,
          blockers: 0,
          results: [{ policy_id: 'sig-required', passed: true }]
        }
      }
    });

    const result = await api.evaluatePolicy({ artifact_id: 'artifact-1', environment_id: 'env-1' });

    expect(requestEncryptedResultMock).toHaveBeenCalledWith({
      operation: 'policy/evaluate',
      tags: [['artifact', 'artifact-1'], ['environment', 'env-1']],
      payload: { artifact_id: 'artifact-1', environment_id: 'env-1' },
      signal: undefined,
      timeoutMs: undefined
    });
    expect(result).toMatchObject({
      allowed: true,
      warnings: 0,
      blockers: 0,
      results: [{ policy_id: 'sig-required', passed: true }]
    });
  });

  it('surfaces terminal error results from ContextVM command replies', async () => {
    requestEncryptedResultMock.mockResolvedValueOnce({
      requestEventId: 'req-1',
      resultEvent: {
        id: 'result-error',
        kind: 25910,
        tags: [['e', 'req-1'], ['status', 'failed'], ['error', 'policy blocked']],
        content: JSON.stringify({ status: 'failed', error: 'policy blocked' })
      }
    });

    await expect(api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1')).rejects.toThrow('policy blocked');
  });
});
