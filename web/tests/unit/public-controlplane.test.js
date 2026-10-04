import { describe, it, expect, beforeEach, vi } from 'vitest';

const requestEncryptedResultMock = vi.hoisted(() => vi.fn());
const publishEncryptedRequestMock = vi.hoisted(() => vi.fn());
const bootstrapMock = vi.hoisted(() => vi.fn());
const gotoMock = vi.hoisted(() => vi.fn());
const publishIntentMock = vi.hoisted(() => vi.fn());
const publishIntentForStatusMock = vi.hoisted(() => vi.fn());
const canonicalIntentRecordMock = vi.hoisted(() => vi.fn());
const UUID_V7 = /^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;
const ORG_ID = '3b45458b-2724-4dda-9fc6-66f12249660d';

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

vi.mock('$lib/nostr/intent-client.svelte.js', () => ({
  publishIntent: publishIntentMock,
  publishIntentForStatus: publishIntentForStatusMock,
  canonicalIntentRecord: canonicalIntentRecordMock,
  resolveIntentOrgId: (domain, explicit) => explicit || (domain === 'backup' || domain === 'package' ? 'f1e7f1e7-f1e7-51e7-a11e-f1e7f1e7f1e7' : '3b45458b-2724-4dda-9fc6-66f12249660d')
}));

describe('public controlplane command helpers', () => {
  let api;

  beforeEach(async () => {
    vi.resetModules();
    vi.resetAllMocks();
    publishIntentMock.mockResolvedValue({ pending: true });
    canonicalIntentRecordMock.mockReturnValue(null);
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
    publishIntentMock.mockImplementation(async request => ({ pending: true, desiredState: request.content,
      intentId: 'intent-1', coordinate: request.coordinate }));
    publishIntentForStatusMock.mockResolvedValue({ data: { desired_state_hash: `sha256:${'a'.repeat(64)}` },
      evaluation: { allowed: true, warnings: 0, blockers: 0 } });
    api = await import('../../src/lib/stores/public-controlplane.svelte.js');
  });

  it('creates services through signed intents, not ContextVM requests', async () => {
    const payload = {
      org_id: ORG_ID,
      name: 'api',
      repo_url: '',
      artifact_repo: 'ghcr.io/example/api',
      runtime_type: 'docker',
      default_branch: 'main'
    };

    await api.createService(payload);

    expect(bootstrapMock).not.toHaveBeenCalled();
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
    expect(publishIntentMock).toHaveBeenCalledWith(expect.objectContaining({
      domain: 'service', op: 'create', orgId: ORG_ID, coordinate: expect.stringMatching(UUID_V7),
      content: { ...payload, id: expect.stringMatching(UUID_V7) }
    }));
  });

  it('mints a client entity id for service and environment creates when absent (bahia-irsry.35)', async () => {
    await api.createService({ org_id: ORG_ID, name: 'api', artifact_repo: 'ghcr.io/example/api' });
    await api.createEnvironment({ org_id: ORG_ID, name: 'staging' });

    const [serviceCall, environmentCall] = publishIntentMock.mock.calls.map(([request]) => request);
    expect(serviceCall.domain).toBe('service');
    expect(serviceCall.content.id).toMatch(UUID_V7);
    expect(environmentCall.domain).toBe('environment');
    expect(environmentCall.content.id).toMatch(UUID_V7);
    expect(environmentCall.content.id).not.toBe(serviceCall.content.id);
  });

  it('mints a client entity id for policy and LLM route creates and keeps a supplied one (bahia-irsry.42)', async () => {
    const id = '01920d4e-7b3a-7c3d-9f2e-0123456789ab';
    await api.createPolicy({ org_id: ORG_ID, name: 'sig', rules: [{ type: 'require_signature' }], enforcement: 'block', enabled: true });
    await api.createPolicy({ id, org_id: ORG_ID, name: 'sig', rules: [{ type: 'require_signature' }], enforcement: 'block', enabled: true });
    await api.createLLMRoute({ name: 'chat' });
    await api.createLLMRoute({ id, name: 'chat' });

    const policyCalls = publishIntentMock.mock.calls.map(([request]) => request);
    expect(policyCalls.map(call => call.domain)).toEqual(['policy', 'policy', 'llm', 'llm']);
    expect(policyCalls[0].content.id).toMatch(UUID_V7);
    expect(policyCalls[1].content.id).toBe(id);
    expect(publishIntentMock.mock.calls[2][0].content.id).toMatch(UUID_V7);
    expect(publishIntentMock.mock.calls[3][0].content.id).toBe(id);
    await expect(api.createPolicy({ id: 'Not-A-UUID', name: 'x', rules: [] })).rejects.toThrow(/Invalid entity id/);
  });

  it('sends a caller-minted entity id unchanged so retries stay idempotent', async () => {
    const id = '01920d4e-7b3a-7c3d-9f2e-0123456789ab';
    await api.createService({ id, org_id: ORG_ID, name: 'api', artifact_repo: 'ghcr.io/example/api' });
    await api.createService({ id, org_id: ORG_ID, name: 'api', artifact_repo: 'ghcr.io/example/api' });

    expect(publishIntentMock.mock.calls.map(([request]) => request.content.id)).toEqual([id, id]);
    await expect(api.createEnvironment({ id: 'Not-A-UUID', name: 'prod' })).rejects.toThrow(/Invalid entity id/);
  });

  it('creates deployment intents as pending signed events, not ContextVM', async () => {
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1');
    expect(publishIntentMock).toHaveBeenCalledWith(expect.objectContaining({
      domain: 'deployment', op: 'create', coordinate: 'svc-1:env-1',
      content: expect.objectContaining({ service_id: 'svc-1', environment_id: 'env-1', artifact_id: 'artifact-1', intent_id: expect.any(String) })
    }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('preserves an explicit deployment unit in the signed intent', async () => {
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1', 'unit-max');
    expect(publishIntentMock).toHaveBeenCalledWith(expect.objectContaining({
      content: expect.objectContaining({ deployment_unit_id: 'unit-max' })
    }));
  });

  it('previews proposed managed desired state from accepted 30315 data', async () => {
    const managed = { schema_version: '1', service_name: 'web', restart_policy: 'unless-stopped', pull_policy: 'always' };

    const result = await api.previewServiceDeployment({
      service_id: 'svc-1',
      environment_id: 'env-1',
      deployment_unit_id: 'unit-1',
      artifact_id: 'artifact-1',
      managed_runtime_config: managed
    });

    expect(result.desired_state_hash).toBe(`sha256:${'a'.repeat(64)}`);
    expect(publishIntentForStatusMock).toHaveBeenCalledWith(expect.objectContaining({
      domain: 'deployment', op: 'preview', coordinate: 'deployment-preview:svc-1:env-1',
      content: expect.objectContaining({ artifact_id: 'artifact-1', deployment_unit_id: 'unit-1',
        managed_runtime_config: managed, compact: true, intent_id: expect.any(String) })
    }));
    expect(publishIntentMock).not.toHaveBeenCalled();
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('preserves the reviewed desired-state hash in the signed intent', async () => {
    const hash = `sha256:${'b'.repeat(64)}`;
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1', 'unit-1', hash);
    expect(publishIntentMock).toHaveBeenCalledWith(expect.objectContaining({
      content: expect.objectContaining({ expected_desired_state_hash: hash, idempotency_key: hash, deployment_unit_id: 'unit-1' })
    }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('preserves the reviewed public route in the signed intent', async () => {
    const publicRoute = { hostname: 'arcana.example.com', upstream_scheme: 'http', upstream_port: 8080,
      health_path: '/healthz', tls: 'managed' };
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1', 'unit-1', 'sha256:hash', publicRoute);
    expect(publishIntentMock).toHaveBeenCalledWith(expect.objectContaining({
      content: expect.objectContaining({ public_route: publicRoute })
    }));
  });

  it('publishes full desired state with the current canonical revision', async () => {
    const hash = `sha256:${'c'.repeat(64)}`;
    canonicalIntentRecordMock.mockReturnValue({ content: {
      id: 'svc-1', org_id: ORG_ID, name: 'api', updated_at: '2026-08-29T12:00:00Z'
    } });
    await api.updateService('svc-1', {
      expected_updated_at: '2026-08-29T12:00:00Z',
      managed_runtime_config: { schema_version: '1', service_name: 'web' },
      idempotency_key: hash
    });

    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({
      domain: 'service', op: 'update', coordinate: 'svc-1',
      currentRecord: expect.objectContaining({ updated_at: '2026-08-29T12:00:00Z' }),
      content: expect.objectContaining({ id: 'svc-1', name: 'api', idempotency_key: hash })
    }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('approves deployment intents with the canonical revision', async () => {
    const { deploymentIntents } = await import('../../src/lib/stores/collections/deployments.svelte.js');
    deploymentIntents.push({ id: 'intent-1', updated_at: '2026-10-03T00:00:00Z', org_id: ORG_ID });
    await api.approveDeploymentIntent('intent-1');
    expect(publishIntentMock).toHaveBeenCalledWith(expect.objectContaining({
      domain: 'deployment', op: 'approve', coordinate: 'intent-1',
      content: expect.objectContaining({ deployment_intent_id: 'intent-1', expected_updated_at: '2026-10-03T00:00:00Z' })
    }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
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
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({
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
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({
      domain: 'llm', op: 'release-register', coordinate: expect.stringMatching(/^llm-release:/),
      content: { ...releasePayload, id: expect.stringMatching(UUID_V7) }
    }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('publishes LLM deploys and rollbacks as pending intents', async () => {
    await api.requestLLMDeploy({ route_id: 'llm-route-1', environment_id: 'env-prod',
      release_id: 'llm-release-1', requested_by: 'f'.repeat(64) });
    await api.requestLLMRollback({ route_id: 'llm-route-1', environment_id: 'env-prod' });
    expect(publishIntentMock).toHaveBeenNthCalledWith(1, expect.objectContaining({
      domain: 'llm', op: 'deploy', coordinate: 'llm-route-1:env-prod',
      content: expect.objectContaining({ release_id: 'llm-release-1' })
    }));
    expect(publishIntentMock).toHaveBeenNthCalledWith(2, expect.objectContaining({
      domain: 'llm', op: 'rollback', coordinate: 'llm-route-1:env-prod' }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
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
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({
      domain: 'backup', op: 'repository-register', coordinate: expect.stringMatching(/^backup-repository:/),
      content: expect.objectContaining({ name: 'archive', backend: 'kopia', repository_uri: 'kopia://archive' })
    }));

    await api.applyBackupPolicy({ name: 'verified', require_verification: true, verification_mode: 'kopia_snapshot_verify', idempotency_key: 'policy-1' });
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'policy-apply' }));

    await api.applyBackupRecipe({ name: 'daily', version: 'v1', backend: 'kopia', repository_id: 'repo-id', target_ref: 'fs:/srv/app', idempotency_key: 'recipe-1' });
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'recipe-apply' }));

    await api.applyBackupDefinition({ name: 'daily-app', repository_id: 'repo-id', policy_id: 'policy-id', recipe_id: 'recipe-id', idempotency_key: 'definition-1' });
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'definition-apply' }));

    await api.requestBackupRun({ id: 'recipe-id', repository_id: 'repo-id', backend: 'kopia', target_ref: 'fs:/srv/app' });
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'run',
      content: expect.objectContaining({ recipe_id: 'recipe-id', repository_id: 'repo-id', backend: 'kopia', target_ref: 'fs:/srv/app' }) }));

    await api.requestBackupVerification({ id: 'run-id', verification_mode: 'kopia_snapshot_verify' });
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'verification' }));

    await api.requestBackupRestore({ id: 'run-id' }, 'fs:/restore');
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'restore' }));

    await api.requestBackupRetention({ repository_id: 'repo-id', policy_id: 'policy-id', backend: 'kopia', dry_run: true });
    expect(publishIntentMock).toHaveBeenLastCalledWith(expect.objectContaining({ op: 'retention',
      content: expect.objectContaining({ repository_id: 'repo-id', backend: 'kopia', dry_run: true }) }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('evaluates deployment policy from accepted 30315 evaluation', async () => {
    publishIntentForStatusMock.mockResolvedValueOnce({ evaluation: { allowed: true, warnings: 0, blockers: 0,
      results: [{ policy_id: 'sig-required', passed: true }] } });

    const result = await api.evaluatePolicy({ artifact_id: 'artifact-1', environment_id: 'env-1' });

    expect(publishIntentForStatusMock).toHaveBeenCalledWith(expect.objectContaining({
      domain: 'policy', op: 'evaluate', coordinate: 'evaluation:artifact-1:env-1',
      content: { artifact_id: 'artifact-1', environment_id: 'env-1', intent_id: expect.any(String) }
    }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
    expect(result).toMatchObject({
      allowed: true,
      warnings: 0,
      blockers: 0,
      results: [{ policy_id: 'sig-required', passed: true }]
    });
  });

  it('requires a canonical revision before signing an environment update', async () => {
    await expect(api.updateEnvironment('env-1', { deployment_units: [] })).rejects.toThrow(/re-read and resubmit/);
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('signs package promote and yank intents without ContextVM CRUD requests', async () => {
    const artifact = { org_id: ORG_ID, repository_id: 'repo-a', target_repository_id: 'repo-b',
      namespace: 'team', package_name: 'api', version: '1.0', filename: 'api.tgz' };
    await api.promotePackage({ ...artifact, source_repository_id: 'repo-a' });
    await api.yankPackage(artifact);
    expect(publishIntentMock).toHaveBeenNthCalledWith(1, expect.objectContaining({
      domain: 'package', op: 'promote', orgId: ORG_ID,
      coordinate: 'package:repo-b:team:api:1.0:api.tgz'
    }));
    expect(publishIntentMock).toHaveBeenNthCalledWith(2, expect.objectContaining({
      domain: 'package', op: 'yank', orgId: ORG_ID,
      coordinate: 'package:repo-a:team:api:1.0:api.tgz'
    }));
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

  it('surfaces rejected preview status without a ContextVM request', async () => {
    publishIntentForStatusMock.mockRejectedValueOnce(new Error('policy blocked'));
    await expect(api.previewServiceDeployment({ service_id: 'svc-1', environment_id: 'env-1', artifact_id: 'artifact-1' }))
      .rejects.toThrow('policy blocked');
    await api.createDeploymentIntent('svc-1', 'env-1', 'artifact-1');
    expect(publishIntentMock).toHaveBeenCalled();
    expect(requestEncryptedResultMock).not.toHaveBeenCalled();
  });

});
