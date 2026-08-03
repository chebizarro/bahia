import { describe, expect, it, vi } from 'vitest';

import {
  ARCANA_PUBLIC_BUILD_ARGS,
  arcanaBuildPayload,
  artifactCandidateForBuild,
  buildEvidence,
  publicArcanaBuildArgs
} from '$lib/stores/arcana-build.js';

describe('Arcana build contract', () => {
  const service = {
    id: 'service-1',
    repository: { repo_coordinate: 'chebizarro/living-library-forge' },
    artifact_repo: 'registry.example/arcana'
  };

  it('sends an opaque credential ref and only allowlisted public build args', () => {
    vi.stubGlobal('crypto', { randomUUID: () => 'progress-1' });
    const payload = arcanaBuildPayload({
      service,
      gitRef: '0123456789abcdef0123456789abcdef01234567',
      credentialRef: 'secret-id',
      buildArgs: {
        VITE_ARCANA_SIGNER_MODE: 'nip07',
        VITE_BLOSSOM_URL: 'https://blossom.example'
      }
    });
    expect(payload.repository_credential_ref).toBe('secret-id');
    expect(payload).not.toHaveProperty('credential');
    expect(payload).not.toHaveProperty('token');
    expect(payload.build_args).toEqual({
      VITE_ARCANA_SIGNER_MODE: 'nip07',
      VITE_BLOSSOM_URL: 'https://blossom.example'
    });
    expect(payload._meta.progressToken).toBe('progress-1');
    vi.unstubAllGlobals();
  });

  it('matches the Dockerfile public argument allowlist and rejects secrets', () => {
    expect(ARCANA_PUBLIC_BUILD_ARGS).toHaveLength(9);
    expect(() => publicArcanaBuildArgs({ GITHUB_TOKEN: 'secret' })).toThrow(/not an approved public/);
  });

  it('returns only digest-pinned successful OCI candidates', () => {
    const build = { id: 'build-1', status: 'succeeded' };
    const digest = `sha256:${'a'.repeat(64)}`;
    expect(artifactCandidateForBuild(build, [{
      id: 'artifact-1', build_id: 'build-1', image_repo: 'registry.example/arcana', image_digest: digest
    }])?.immutable_ref).toBe(`registry.example/arcana@${digest}`);
    expect(artifactCandidateForBuild(build, [{
      id: 'artifact-2', build_id: 'build-1', image_repo: 'registry.example/arcana', image_digest: 'latest'
    }])).toBeNull();
  });

  it('normalizes projected logs and signed evidence', () => {
    expect(buildEvidence({
      source_event_id: 'request-id',
      metadata: { log_url: 'https://ci.example/log', evidence: { result_event_id: 'result-id' } }
    })).toMatchObject({
      log_url: 'https://ci.example/log',
      request_event_id: 'request-id',
      result_event_id: 'result-id'
    });
  });
});
