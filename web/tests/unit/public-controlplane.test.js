import { describe, it, expect, beforeEach, vi } from 'vitest';

const requestEncryptedResultMock = vi.hoisted(() => vi.fn());
const publishEncryptedRequestMock = vi.hoisted(() => vi.fn());
const bootstrapMock = vi.hoisted(() => vi.fn());
const publishDomainIntentMock = vi.hoisted(() => vi.fn());
const gotoMock = vi.hoisted(() => vi.fn());
const UUID_V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

vi.mock('$app/navigation', () => ({
  goto: gotoMock
}));

vi.mock('$lib/nostr/encrypted-controlplane.js', () => ({
  CONTEXTVM_MESSAGE_KIND: 25910,
  requestEncryptedResult: requestEncryptedResultMock,
  publishEncryptedRequest: publishEncryptedRequestMock
}));

vi.mock('../../src/lib/stores/controlplane.svelte.js', () => ({
  bootstrapControlplane: bootstrapMock
}));

vi.mock('../../src/lib/stores/domain-intents.svelte.js', () => ({
  publishDomainIntent: publishDomainIntentMock
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
    publishDomainIntentMock.mockImplementation(async request => ({ pending: true, desiredState: request.content,
      intentId: 'intent-1', coordinate: request.coordinate }));
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
      payload: { ...payload, id: expect.stringMatching(UUID_V7) },
      tags: [],
      kind: 25910,
      resultKinds: [25910],
      signal: undefined,
      timeoutMs: undefined
    });
  });

  it('mints a client entity id for service and environment creates when absent (bahia-irsry.35)', async () => {
    await api.createService({ name: 'api', artifact_repo: 'ghcr.io/example/api' });
    await api.createEnvironment({ org_id: 'org-1', name: 'staging' });

    const [serviceCall, environmentCall] = requestEncryptedResultMock.mock.calls.map(([request]) => request);
    expect(serviceCall.operation).toBe('service/create');
    expect(serviceCall.payload.id).toMatch(UUID_V7);
    expect(environmentCall.operation).toBe('environment/create');
    expect(environmentCall.payload.id).toMatch(UUID_V7);
    expect(environmentCall.payload.id).not.toBe(serviceCall.payload.id);
  });

  it('mints a client entity id for policy and LLM route creates and keeps a supplied one (bahia-irsry.42)', async () => {
    const id = '01920d4e-7b3a-7c3d-9f2e-0123456789ab';
    await api.createPolicy({ name: 'sig', rules: [{ type: 'require_signature' }], enforcement: 'block', enabled: true });
    await api.createPolicy({ id, name: 'sig', rules: [{ type: 'require_signature' }], enforcement: 'block', enabled: true });
    await api.createLLMRoute({ name: 'chat' });
    await api.createLLMRoute({ id, name: 'chat' });

    const calls = requestEncryptedResultMock.mock.calls.map(([request]) => request);
    expect(calls.map((call) => call.operation)).toEqual(['policy/create', 'policy/create']);
    expect(calls[0].payload.id).toMatch(UUID_V7);
    expect(calls[1].payload.id).toBe(id);
    expect(publishDomainIntentMock.mock.calls[0][0].content.id).toMatch(UUID_V7);
    expect(publishDomainIntentMock.mock.calls[1][0].content.id).toBe(id);
    await expect(api.createPolicy({ id: 'Not-A-UUID', name: 'x', rules: [] })).rejects.toThrow(/Invalid entity id/);
  });

  it('sends a caller-minted entity id unchanged so retries stay idempotent', async () => {
    const id = '01920d4e-7b3a-7c3d-9f2e-0123456789ab';
    await api.createService({ id, name: 'api', artifact_repo: 'ghcr.io/example/api' });
    await api.createService({ id, name: 'api', artifact_repo: 'ghcr.io/example/api' });

    expect(requestEncryptedResultMock.mock.calls.map(([request]) => request.payload.id)).toEqual([id, id]);
    await expect(api.createEnvironment({ id: 'Not-A-UUID', name: 'prod' })).rejects.toThrow(/Invalid entity id/);
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
      kind: 25910,
      resultKinds: [25910],
      signal: undefined,
      timeoutMs: undefined
    });
  });

  it('targets deployment intents at an explicit deployment unit', async () => {
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1', 'unit-max');

    expect(requestEncryptedResultMock).toHaveBeenCalledWith({
      operation: 'service/deploy',
      tags: [['service', 'svc-1'], ['environment', 'env-1'], ['unit', 'unit-max'], ['artifact', 'artifact-1']],
      payload: {
        service_id: 'svc-1',
        environment_id: 'env-1',
        deployment_unit_id: 'unit-max',
        artifact_id: 'artifact-1'
      },
      kind: 25910,
      resultKinds: [25910],
      signal: undefined,
      timeoutMs: undefined
    });
  });

  it('previews proposed managed desired state through the configured signer', async () => {
    const managed = { schema_version: '1', service_name: 'web', restart_policy: 'unless-stopped', pull_policy: 'always' };
    requestEncryptedResultMock.mockResolvedValueOnce({
      requestEventId: 'preview-1',
      result: { status: 'ok', payload: { desired_state_hash: `sha256:${'a'.repeat(64)}` } }
    });

    const result = await api.previewServiceDeployment({
      service_id: 'svc-1',
      environment_id: 'env-1',
      deployment_unit_id: 'unit-1',
      artifact_id: 'artifact-1',
      managed_runtime_config: managed
    });

    expect(result.desired_state_hash).toBe(`sha256:${'a'.repeat(64)}`);
    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith({
      operation: 'service/deploy-preview',
      tags: [['service', 'svc-1'], ['environment', 'env-1'], ['unit', 'unit-1'], ['artifact', 'artifact-1']],
      payload: {
        service_id: 'svc-1',
        environment_id: 'env-1',
        deployment_unit_id: 'unit-1',
        artifact_id: 'artifact-1',
        managed_runtime_config: managed
      },
      kind: 25910,
      resultKinds: [25910],
      signal: undefined,
      timeoutMs: undefined
    });
  });

  it('signs the displayed desired-state hash into an idempotent deploy request', async () => {
    const hash = `sha256:${'b'.repeat(64)}`;
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1', 'unit-1', hash);

    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith({
      operation: 'service/deploy',
      tags: [['service', 'svc-1'], ['environment', 'env-1'], ['unit', 'unit-1'], ['artifact', 'artifact-1']],
      payload: {
        service_id: 'svc-1',
        environment_id: 'env-1',
        deployment_unit_id: 'unit-1',
        artifact_id: 'artifact-1',
        expected_desired_state_hash: hash,
        idempotency_key: hash
      },
      kind: 25910,
      resultKinds: [25910],
      signal: undefined,
      timeoutMs: undefined,
      requestId: hash
    });
  });

  it('submits the reviewed public route in the signed idempotent deploy request', async () => {
    const hash = `sha256:${'d'.repeat(64)}`;
    const publicRoute = {
      hostname: 'arcana.example.com',
      upstream_scheme: 'http',
      upstream_port: 8080,
      health_path: '/healthz',
      tls: 'managed'
    };

    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1', 'unit-1', hash, publicRoute);

    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({
      operation: 'service/deploy',
      payload: expect.objectContaining({
        expected_desired_state_hash: hash,
        idempotency_key: hash,
        public_route: publicRoute
      }),
      requestId: hash
    }));
  });

  it('uses the desired-state hash to idempotently persist managed service configuration', async () => {
    const hash = `sha256:${'c'.repeat(64)}`;
    await api.updateService('svc-1', {
      expected_updated_at: '2026-08-29T12:00:00Z',
      managed_runtime_config: { schema_version: '1', service_name: 'web' },
      idempotency_key: hash
    });

    expect(requestEncryptedResultMock).toHaveBeenLastCalledWith(expect.objectContaining({
      operation: 'service/update',
      requestId: hash,
      payload: expect.objectContaining({ id: 'svc-1', expected_updated_at: '2026-08-29T12:00:00Z', idempotency_key: hash })
    }));
  });

  it('approves deployment intents through canonical ContextVM approval requests', async () => {
    await api.approveDeploymentIntent('intent-1');

    expect(requestEncryptedResultMock).toHaveBeenCalledWith({
      operation: 'approval/approve',
      tags: [['intent', 'intent-1'], ['decision', 'approve']],
      payload: { intent_id: 'intent-1', decision: 'approve' },
      kind: 25910,
      resultKinds: [25910],
      signal: undefined,
      timeoutMs: undefined
    });
  });

  it('creates LLM routes and releases through signed intents', async () => {
    const routePayload = {
      name: 'chat-prod',
      description: 'Public chat completions route',
      gateway_config: {
        public_model: 'bahia/chat',
        path: '/v1/models/chat-prod'
      }
    };
    await api.createLLMRoute(routePayload);
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({
      domain: 'llm', op: 'create', coordinate: expect.stringMatching(UUID_V7),
      content: { ...routePayload, id: expect.stringMatching(UUID_V7) }
    }));

    const releasePayload = {
      route_id: '01920d4e-7b3a-7c3d-9f2e-0123456789ab',
      version: 'v1',
      model_ref: 'hf://meta-llama/Llama-3',
      model_source: 'huggingface'
    };
    await api.registerLLMRelease(releasePayload);
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({
      domain: 'llm', op: 'release-register', coordinate: expect.stringMatching(/^llm-release:/),
      content: { ...releasePayload, id: expect.stringMatching(UUID_V7) }
    }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
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
      },
      kind: 25910,
      resultKinds: [25910]
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
      },
      kind: 25910,
      resultKinds: [25910]
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
      kind: 25910,
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
      kind: 25910,
      signal: undefined
    });
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('rejects oversized inline artifact SBOM imports before publishing', () => {
    expect(api.MAX_CONTEXTVM_INLINE_SBOM_BYTES).toBe(360 * 1024);
    const oversized = Buffer.alloc(api.MAX_CONTEXTVM_INLINE_SBOM_BYTES + 1).toString('base64');
    expect(() => api.importArtifactSBOM({ id: 'artifact-1', digest: 'sha256:abc123' }, { format: 'spdx', payloadBase64: oversized }))
      .toThrow('Inline SBOM imports are limited to 368640 bytes (360 KiB); upload larger SBOM files to Blossom and import them by location.');
    expect(publishEncryptedRequestMock).not.toHaveBeenCalled();
  });

  it('publishes an inline artifact SBOM of exactly the inline limit', async () => {
    const atLimit = Buffer.alloc(api.MAX_CONTEXTVM_INLINE_SBOM_BYTES).toString('base64');
    await api.importArtifactSBOM({ id: 'artifact-1', digest: 'sha256:abc123' }, { format: 'spdx', payloadBase64: atLimit });
    expect(publishEncryptedRequestMock).toHaveBeenCalledTimes(1);
    expect(publishEncryptedRequestMock.mock.calls[0][0].payload.payloadBase64).toBe(atLimit);
  });

  it('imports artifact SBOMs of any size by Blossom location', async () => {
    const uri = `https://blossom.example/${'c'.repeat(64)}`;
    await api.importArtifactSBOM({ id: 'artifact-1', digest: 'sha256:abc123' }, {
      format: 'spdx',
      location: { type: 'blossom', uri, mediaType: 'application/spdx+json' }
    });
    expect(publishEncryptedRequestMock).toHaveBeenCalledTimes(1);
    const request = publishEncryptedRequestMock.mock.calls[0][0];
    expect(request.operation).toBe('sbom/import');
    expect(request.kind).toBe(25910);
    expect(request.payload.location).toEqual({ type: 'blossom', uri, mediaType: 'application/spdx+json' });
    expect(request.payload).not.toHaveProperty('payloadBase64');
    expect(request.payload.idempotencyKey).toContain(`location:blossom:${uri}`);
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

  it('publishes backup mutations and operations as handler-compatible intents', async () => {
    await api.registerBackupRepository({ name: 'archive', backend: 'kopia', repository_uri: 'kopia://archive', idempotency_key: 'repo-1' });
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({
      domain: 'backup', op: 'repository-register', coordinate: expect.stringMatching(/^backup-repository:/),
      content: expect.objectContaining({ name: 'archive', backend: 'kopia', repository_uri: 'kopia://archive' })
    }));

    await api.applyBackupPolicy({ name: 'verified', require_verification: true, verification_mode: 'kopia_snapshot_verify', idempotency_key: 'policy-1' });
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'policy-apply' }));

    await api.applyBackupRecipe({ name: 'daily', version: 'v1', backend: 'kopia', repository_id: 'repo-id', target_ref: 'fs:/srv/app', idempotency_key: 'recipe-1' });
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'recipe-apply' }));

    await api.applyBackupDefinition({ name: 'daily-app', repository_id: 'repo-id', policy_id: 'policy-id', recipe_id: 'recipe-id', idempotency_key: 'definition-1' });
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'definition-apply' }));

    await api.requestBackupRun({ id: 'recipe-id', repository_id: 'repo-id', backend: 'kopia', target_ref: 'fs:/srv/app' });
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'run',
      content: expect.objectContaining({ recipe_id: 'recipe-id', repository_id: 'repo-id', backend: 'kopia', target_ref: 'fs:/srv/app' }) }));

    await api.requestBackupVerification({ id: 'run-id', verification_mode: 'kopia_snapshot_verify' });
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'verification' }));

    await api.requestBackupRestore({ id: 'run-id' }, 'fs:/restore');
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'restore' }));

    await api.requestBackupRetention({ repository_id: 'repo-id', policy_id: 'policy-id', backend: 'kopia', dry_run: true });
    expect(publishDomainIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'retention',
      content: expect.objectContaining({ repository_id: 'repo-id', backend: 'kopia', dry_run: true }) }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
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
      tags: [['environment', 'env-1'], ['artifact', 'artifact-1']],
      payload: { artifact_id: 'artifact-1', environment_id: 'env-1' },
      kind: 25910,
      resultKinds: [25910],
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

  it('preserves structured ContextVM error metadata for revision handling', async () => {
    requestEncryptedResultMock.mockResolvedValueOnce({
      requestEventId: 'req-1',
      result: {
        status: 'error',
        error: {
          code: -32009,
          message: 'environment revision conflict',
          data: { expected_updated_at: 'old' }
        }
      }
    });

    let thrown;
    try {
      await api.updateEnvironment('env-1', {
        expected_updated_at: 'old',
        deployment_units: []
      });
    } catch (error) {
      thrown = error;
    }
    expect(thrown).toMatchObject({
      message: 'environment revision conflict',
      code: -32009,
      operation: 'environment/update'
    });
    expect(thrown.data).toEqual({ expected_updated_at: 'old' });
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
