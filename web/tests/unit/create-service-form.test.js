import { describe, it, expect } from 'vitest';
import {
  buildArtifactRepo,
  validateCreateServiceForm,
  buildCreateServicePayload
} from '../../src/routes/services/create-service-form.js';

describe('create service form helpers', () => {
  it('builds artifact repo from selected registry + path', () => {
    expect(buildArtifactRepo({
      selectedRegistry: 'default',
      repoPath: 'team/api',
      availableRegistries: [{ id: 'default', base_url: 'ghcr.io' }]
    })).toBe('ghcr.io/team/api');
  });

  it('uses raw path for custom registry', () => {
    expect(buildArtifactRepo({
      selectedRegistry: 'custom',
      repoPath: 'ghcr.io/team/api',
      availableRegistries: [{ id: 'default', base_url: 'ghcr.io' }]
    })).toBe('ghcr.io/team/api');
  });

  it('validates required create-service fields', () => {
    expect(validateCreateServiceForm({ name: '', artifactRepo: 'a/b', runtimeType: 'docker' })).toBe('Name is required');
    expect(validateCreateServiceForm({ name: 'api', artifactRepo: '', runtimeType: 'docker' })).toBe('Artifact repository is required');
    expect(validateCreateServiceForm({ name: 'api', artifactRepo: 'a/b', runtimeType: '' })).toBe('Runtime type is required');
    expect(validateCreateServiceForm({ name: 'api', artifactRepo: 'a/b', runtimeType: 'docker' })).toBeNull();
  });

  it('builds create-service payload including repo_url from repository selection', () => {
    expect(buildCreateServicePayload({
      name: ' api ',
      repositorySelection: { repoUrl: 'https://github.com/acme/api' },
      artifact_repo: ' ghcr.io/acme/api ',
      runtime_type: 'docker',
      default_branch: ''
    })).toEqual({
      name: 'api',
      repo_url: 'https://github.com/acme/api',
      artifact_repo: 'ghcr.io/acme/api',
      runtime_type: 'docker',
      default_branch: 'main'
    });
  });
});
