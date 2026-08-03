import { describe, expect, it } from 'vitest';

import {
  buildManagedRuntimeConfig,
  createManagedRuntimeForm,
  desiredStateChanges,
  immutableArtifactDigest,
  isRegisteredImmutableArtifact
} from '../../src/lib/deployment-desired-state.js';

describe('managed deployment desired state', () => {
  it('assembles an Arcana-ready generic Compose configuration without secret values', () => {
    const form = createManagedRuntimeForm({ name: 'Arcana Web' });
    expect(form.health_enabled).toBe(false);
    expect(form.health_path).toBe('');

    Object.assign(form, {
      service_name: 'arcana-web',
      ports: '8080:8080',
      environment: 'LOG_LEVEL=info\nPUBLIC_URL=https://arcana.example',
      secret_refs: [{
        enabled: true,
        env_var: 'API_TOKEN',
        secret_id: '11111111-1111-1111-1111-111111111111',
        value: 'must-never-be-sent'
      }],
      health_enabled: true,
      health_path: '/healthz',
      health_port: 8080,
      restart_policy: 'unless-stopped'
    });

    const config = buildManagedRuntimeConfig(form);
    expect(config).toMatchObject({
      schema_version: '1',
      service_name: 'arcana-web',
      ports: ['8080:8080'],
      environment: {
        LOG_LEVEL: 'info',
        PUBLIC_URL: 'https://arcana.example'
      },
      secret_refs: [{
        env_var: 'API_TOKEN',
        secret_id: '11111111-1111-1111-1111-111111111111'
      }],
      healthcheck: {
        protocol: 'http',
        method: 'GET',
        path: '/healthz',
        port: 8080
      },
      restart_policy: 'unless-stopped',
      pull_policy: 'always'
    });
    expect(JSON.stringify(config)).not.toContain('must-never-be-sent');
  });

  it('accepts only registered artifacts with immutable SHA-256 digests', () => {
    const digest = `sha256:${'a'.repeat(64)}`;
    expect(isRegisteredImmutableArtifact({ id: 'artifact-1', image_digest: digest })).toBe(true);
    expect(immutableArtifactDigest({ image_digest: digest.toUpperCase() })).toBe(digest);
    expect(isRegisteredImmutableArtifact({ id: 'artifact-1', image_digest: 'sha256:abc123' })).toBe(false);
    expect(isRegisteredImmutableArtifact({ image_digest: digest })).toBe(false);
  });

  it('rejects literal and secret-backed values sharing an environment name', () => {
    expect(() => buildManagedRuntimeConfig({
      service_name: 'web',
      environment: 'TOKEN=literal',
      secret_refs: [{ env_var: 'TOKEN', secret_id: 'secret-1' }],
      restart_policy: 'unless-stopped'
    })).toThrow(/both literal and secret-backed/);
  });

  it('produces a deterministic leaf-level non-secret diff', () => {
    expect(desiredStateChanges(
      { image_ref: 'repo@old', env: { MODE: 'dev' } },
      { image_ref: 'repo@new', env: { MODE: 'prod' } }
    )).toEqual([
      { path: 'env.MODE', before: 'dev', after: 'prod' },
      { path: 'image_ref', before: 'repo@old', after: 'repo@new' }
    ]);
  });
});
