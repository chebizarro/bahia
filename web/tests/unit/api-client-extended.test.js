import { describe, it, expect, beforeEach, vi } from 'vitest';

// Mock the window object before importing the client
global.window = global;

describe('BahiaClient - Extended API Coverage', () => {
  let api;

  beforeEach(async () => {
    // Clear localStorage and mocks
    localStorage.clear();
    vi.clearAllMocks();
    
    // Reset modules to avoid state leakage
    vi.resetModules();
    
    // Dynamically import the client to get a fresh instance
    const module = await import('../../src/lib/api/client.js');
    api = module.api;
  });

  describe('Auth Provider', () => {
    it('uses auth provider authorization without bearer token fallback', async () => {
      api.setAuthProvider({ getAuthorizationHeader: vi.fn().mockResolvedValue('Nostr signed-event') });
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: [] })
      });

      await api.listServices();

      expect(global.fetch).toHaveBeenCalledWith('/api/v1/services', expect.objectContaining({
        headers: expect.objectContaining({ Authorization: 'Nostr signed-event' })
      }));
    });

    it('passes method and API-relative URL to auth provider', async () => {
      const provider = { getAuthorizationHeader: vi.fn().mockResolvedValue('Nostr signed-event') };
      api.setAuthProvider(provider);
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: 'svc-1' } })
      });

      await api.createService({ name: 'api', artifact_repo: 'repo' });

      expect(provider.getAuthorizationHeader).toHaveBeenCalledWith({ method: 'POST', url: '/api/v1/services' });
    });
  });

  describe('Environment Methods', () => {
    it('should call createEnvironment with correct method and path', async () => {
      const payload = {
        name: 'production',
        runtime_config: { cpu: '2', memory: '4Gi' }
      };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: 'env-1', ...payload } })
      });

      const result = await api.createEnvironment(payload);

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/environments',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ id: 'env-1', ...payload });
    });

    it('should call updateEnvironment with correct method and encoded path', async () => {
      const envId = 'env-prod/staging';
      const patch = { runtime_config: { cpu: '4' } };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: envId, ...patch } })
      });

      const result = await api.updateEnvironment(envId, patch);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/environments/${encodeURIComponent(envId)}`,
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify(patch)
        })
      );
      expect(result).toEqual({ id: envId, ...patch });
    });

    it('should call deleteEnvironment with correct method and encoded path', async () => {
      const envId = 'env-test';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        status: 204,
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('No content'); }
      });

      const result = await api.deleteEnvironment(envId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/environments/${encodeURIComponent(envId)}`,
        expect.objectContaining({
          method: 'DELETE'
        })
      );
      expect(result).toBeNull();
    });

    it('should handle Bahia envelope error in updateEnvironment', async () => {
      const envId = 'env-1';
      const patch = { name: 'invalid' };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'Invalid runtime configuration' })
      });

      await expect(api.updateEnvironment(envId, patch)).rejects.toThrow('Invalid runtime configuration');
    });
  });

  describe('State Methods', () => {
    it('should call getStateByServiceEnv with encoded service/environment IDs', async () => {
      const serviceId = 'svc/alpha';
      const envId = 'env/prod';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { service_id: serviceId, environment_id: envId } })
      });

      const result = await api.getStateByServiceEnv(serviceId, envId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/${encodeURIComponent(serviceId)}/environments/${encodeURIComponent(envId)}/state`,
        expect.any(Object)
      );
      expect(result).toEqual({ service_id: serviceId, environment_id: envId });
    });

    it('should call listStatesByEnvironment with encoded environment ID and return [] for null payload', async () => {
      const envId = 'env/prod';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: null })
      });

      const result = await api.listStatesByEnvironment(envId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/environments/${encodeURIComponent(envId)}/state`,
        expect.any(Object)
      );
      expect(result).toEqual([]);
    });

    it('should call recordObservation with POST and body', async () => {
      const payload = {
        service_id: '0f3cc322-9484-4fee-b7d1-448318791e6c',
        environment_id: '654edaf0-7ca7-4cb6-acf8-13058de4ea4a',
        observed_image_digest: 'sha256:abcd',
        health_status: 'healthy',
        source: 'agent'
      };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: 'obs-1', ...payload } })
      });

      const result = await api.recordObservation(payload);

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/observations',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ id: 'obs-1', ...payload });
    });
  });

  describe('Policy Methods', () => {
    it('should call createPolicy with all supported fields', async () => {
      const payload = {
        name: 'Production Approval Required',
        environment_id: 'env-prod',
        rules: [{ type: 'require_approval', environments: ['production'] }],
        enforcement: 'enforce',
        enabled: true
      };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: 'pol-1', ...payload } })
      });

      const result = await api.createPolicy(payload);

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/policies',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ id: 'pol-1', ...payload });
    });

    it('should call updatePolicy with all supported patch fields and encoded path', async () => {
      const policyId = 'pol/v2';
      const patch = {
        name: 'Production Gate',
        environment_id: 'env-prod',
        rules: [{ type: 'deny_if_drifted' }],
        enforcement: 'audit',
        enabled: false
      };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: policyId, ...patch } })
      });

      const result = await api.updatePolicy(policyId, patch);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/policies/${encodeURIComponent(policyId)}`,
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify(patch)
        })
      );
      expect(result).toEqual({ id: policyId, ...patch });
    });

    it('should call evaluatePolicy with POST and payload', async () => {
      const payload = {
        policy_id: 'pol-1',
        context: {
          environment_id: 'env-prod',
          service_id: 'svc-1'
        }
      };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { allowed: true, reason: 'approved' } })
      });

      const result = await api.evaluatePolicy(payload);

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/policies/evaluate',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ allowed: true, reason: 'approved' });
    });

    it('should call deletePolicy with correct method and encoded path', async () => {
      const policyId = 'pol-123';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        status: 204,
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('No content'); }
      });

      const result = await api.deletePolicy(policyId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/policies/${encodeURIComponent(policyId)}`,
        expect.objectContaining({
          method: 'DELETE'
        })
      );
      expect(result).toBeNull();
    });

    it('should encode special characters in policy ID', async () => {
      const policyId = 'policy/with/slashes';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        status: 204,
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('No content'); }
      });

      await api.deletePolicy(policyId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/policies/${encodeURIComponent(policyId)}`,
        expect.any(Object)
      );
    });
  });

  describe('Secret Methods', () => {
    it('should call createSecret with correct path and body', async () => {
      const serviceId = 'svc-app';
      const payload = { key: 'API_KEY', value: 'secret123' };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: 'secret-1', key: 'API_KEY' } })
      });

      const result = await api.createSecret(serviceId, payload);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/${encodeURIComponent(serviceId)}/secrets`,
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ id: 'secret-1', key: 'API_KEY' });
    });

    it('should call updateSecret with correct path and body', async () => {
      const serviceId = 'svc-app';
      const secretId = 'secret-1';
      const payload = { value: 'rotated-secret' };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: secretId, key: 'API_KEY' } })
      });

      const result = await api.updateSecret(serviceId, secretId, payload);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/${encodeURIComponent(serviceId)}/secrets/${encodeURIComponent(secretId)}`,
        expect.objectContaining({
          method: 'PUT',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ id: secretId, key: 'API_KEY' });
    });

    it('should encode special characters in updateSecret path parameters', async () => {
      const serviceId = 'service/with/slash';
      const secretId = 'secret@test';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: secretId } })
      });

      await api.updateSecret(serviceId, secretId, { value: 'next' });

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/${encodeURIComponent(serviceId)}/secrets/${encodeURIComponent(secretId)}`,
        expect.any(Object)
      );
    });

    it('should call deleteSecret with correct path encoding', async () => {
      const serviceId = 'svc-app';
      const secretId = 'secret-1';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        status: 204,
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('No content'); }
      });

      const result = await api.deleteSecret(serviceId, secretId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/${encodeURIComponent(serviceId)}/secrets/${encodeURIComponent(secretId)}`,
        expect.objectContaining({
          method: 'DELETE'
        })
      );
      expect(result).toBeNull();
    });

    it('should encode special characters in service and secret IDs', async () => {
      const serviceId = 'service/with/slash';
      const secretId = 'secret@test';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        status: 204,
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('No content'); }
      });

      await api.deleteSecret(serviceId, secretId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/${encodeURIComponent(serviceId)}/secrets/${encodeURIComponent(secretId)}`,
        expect.any(Object)
      );
    });

    it('should handle Bahia envelope error in createSecret', async () => {
      const serviceId = 'svc-1';
      const payload = { key: 'INVALID', value: '' };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'Secret value cannot be empty' })
      });

      await expect(api.createSecret(serviceId, payload)).rejects.toThrow('Secret value cannot be empty');
    });
  });

  describe('Build Methods', () => {
    it('should call getBuild with encoded ID', async () => {
      const buildId = 'build/abc-123';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: buildId, status: 'pending' } })
      });

      const result = await api.getBuild(buildId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/builds/${encodeURIComponent(buildId)}`,
        expect.any(Object)
      );
      expect(result).toEqual({ id: buildId, status: 'pending' });
    });

    it('should call registerBuild with POST and body', async () => {
      const payload = {
        service_id: 'svc-1',
        vcs_ref: 'refs/heads/main',
        vcs_sha: 'abcdef1234567890abcdef1234567890abcdef12',
        source: 'github-actions'
      };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: 'build-1', ...payload } })
      });

      const result = await api.registerBuild(payload);

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/builds',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ id: 'build-1', ...payload });
    });

    it('should call updateBuildStatus with PATCH and encoded ID', async () => {
      const buildId = 'build@123';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        status: 204,
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('No content'); }
      });

      const result = await api.updateBuildStatus(buildId, 'success');

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/builds/${encodeURIComponent(buildId)}/status`,
        expect.objectContaining({
          method: 'PATCH',
          body: JSON.stringify({ status: 'success' })
        })
      );
      expect(result).toBeNull();
    });

    it('should return [] from listBuilds when backend data is null', async () => {
      const serviceId = 'svc-x';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: null })
      });

      const result = await api.listBuilds(serviceId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/${encodeURIComponent(serviceId)}/builds`,
        expect.any(Object)
      );
      expect(result).toEqual([]);
    });
  });

  describe('Artifact Methods', () => {
    it('should call getArtifact with encoded ID', async () => {
      const artifactId = 'artifact/v2';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: artifactId } })
      });

      const result = await api.getArtifact(artifactId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}`,
        expect.any(Object)
      );
      expect(result).toEqual({ id: artifactId });
    });

    it('should call registerArtifact with POST and body', async () => {
      const payload = {
        build_id: 'build-1',
        digest: 'sha256:1234abcd',
        repository: 'ghcr.io/example/app',
        tags: ['latest']
      };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: 'artifact-1', ...payload } })
      });

      const result = await api.registerArtifact(payload);

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/artifacts',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ id: 'artifact-1', ...payload });
    });
  });

  describe('SBOM Methods', () => {
    it('should call getSBOM with correct path encoding', async () => {
      const artifactId = 'artifact-abc-123';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({
          data: {
            artifact_id: artifactId,
            packages: [{ name: 'express', version: '4.18.0' }]
          }
        })
      });

      const result = await api.getSBOM(artifactId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}/sbom`,
        expect.any(Object)
      );
      expect(result).toEqual({
        artifact_id: artifactId,
        packages: [{ name: 'express', version: '4.18.0' }]
      });
    });

    it('should encode special characters in artifact ID for getSBOM', async () => {
      const artifactId = 'artifact/v1.0/build-123';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { artifact_id: artifactId, packages: [] } })
      });

      await api.getSBOM(artifactId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}/sbom`,
        expect.any(Object)
      );
    });

    it('should handle missing SBOM with Bahia error envelope', async () => {
      const artifactId = 'artifact-no-sbom';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'SBOM not found for this artifact' })
      });

      await expect(api.getSBOM(artifactId)).rejects.toThrow('SBOM not found for this artifact');
    });

    it('should call getSBOMPackages with artifact and query params', async () => {
      const artifactId = 'artifact-abc-123';
      const params = { ecosystem: 'npm', limit: 25 };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { packages: [{ name: 'svelte' }] } })
      });

      const result = await api.getSBOMPackages(artifactId, params);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}/sbom/packages?ecosystem=npm&limit=25`,
        expect.any(Object)
      );
      expect(result).toEqual({ packages: [{ name: 'svelte' }] });
    });

    it('should call searchSBOMPackages with query params', async () => {
      const params = { q: 'openssl', ecosystem: 'deb' };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { results: [{ name: 'openssl' }] } })
      });

      const result = await api.searchSBOMPackages(params);

      expect(global.fetch).toHaveBeenCalledWith('/api/v1/sbom/search?q=openssl&ecosystem=deb', expect.any(Object));
      expect(result).toEqual({ results: [{ name: 'openssl' }] });
    });

    it('should call ingestSBOM with POST and payload', async () => {
      const artifactId = 'artifact-xyz';
      const payload = { format: 'spdx-json', content: '{"spdxVersion":"SPDX-2.3"}' };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { ingested: true } })
      });

      const result = await api.ingestSBOM(artifactId, payload);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}/sbom`,
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ ingested: true });
    });
  });

  describe('Deployment Run Logs Methods', () => {
    it('should request stdout logs via stream query parameter', async () => {
      const runId = 'run-123';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { stdout: 'line 1\nline 2' } })
      });

      const result = await api.getRunLogs(runId, 250, 'stdout');

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/deployments/runs/${encodeURIComponent(runId)}/logs?tail=250&stream=stdout`,
        expect.any(Object)
      );
      expect(result).toEqual({ stdout: 'line 1\nline 2' });
    });

    it('should default to merged stream when stream is omitted', async () => {
      const runId = 'run-default';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { stdout: 'out', stderr: 'err' } })
      });

      await api.getRunLogs(runId, 42);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/deployments/runs/${encodeURIComponent(runId)}/logs?tail=42&stream=merged`,
        expect.any(Object)
      );
    });
  });

  describe('Notification Methods', () => {
    it('should list notification channels with query parameters', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: [{ id: 'ch-1', type: 'slack' }] })
      });

      const result = await api.listNotificationChannels({ type: 'slack', enabled: true });

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/notifications/channels?type=slack&enabled=true',
        expect.any(Object)
      );
      expect(result).toEqual([{ id: 'ch-1', type: 'slack' }]);
    });

    it('should call notification channel CRUD and test endpoints', async () => {
      const channelId = 'chan/abc';
      const createPayload = { type: 'slack', config: { webhook_url: 'https://example.test/hook' } };
      const updatePayload = { enabled: false };

      global.fetch
        .mockResolvedValueOnce({ ok: true, headers: new Map([['content-type', 'application/json']]), json: async () => ({ data: { id: channelId } }) })
        .mockResolvedValueOnce({ ok: true, headers: new Map([['content-type', 'application/json']]), json: async () => ({ data: { id: channelId, ...createPayload } }) })
        .mockResolvedValueOnce({ ok: true, headers: new Map([['content-type', 'application/json']]), json: async () => ({ data: { id: channelId, ...updatePayload } }) })
        .mockResolvedValueOnce({ ok: true, status: 204, headers: new Map([['content-type', 'text/plain']]), json: async () => { throw new Error('No content'); } })
        .mockResolvedValueOnce({ ok: true, headers: new Map([['content-type', 'application/json']]), json: async () => ({ data: { sent: true } }) })
        .mockResolvedValueOnce({ ok: true, status: 204, headers: new Map([['content-type', 'text/plain']]), json: async () => { throw new Error('No content'); } });

      await api.getNotificationChannel(channelId);
      await api.createNotificationChannel(createPayload);
      await api.updateNotificationChannel(channelId, updatePayload);
      await api.deleteNotificationChannel(channelId);
      await api.testNotificationChannel(channelId);
      await api.deleteNotificationChannel(channelId);

      expect(global.fetch).toHaveBeenNthCalledWith(1, `/api/v1/notifications/channels/${encodeURIComponent(channelId)}`, expect.any(Object));
      expect(global.fetch).toHaveBeenNthCalledWith(2, '/api/v1/notifications/channels', expect.objectContaining({ method: 'POST', body: JSON.stringify(createPayload) }));
      expect(global.fetch).toHaveBeenNthCalledWith(3, `/api/v1/notifications/channels/${encodeURIComponent(channelId)}`, expect.objectContaining({ method: 'PUT', body: JSON.stringify(updatePayload) }));
      expect(global.fetch).toHaveBeenNthCalledWith(4, `/api/v1/notifications/channels/${encodeURIComponent(channelId)}`, expect.objectContaining({ method: 'DELETE' }));
      expect(global.fetch).toHaveBeenNthCalledWith(5, `/api/v1/notifications/channels/${encodeURIComponent(channelId)}/test`, expect.objectContaining({ method: 'POST' }));
      expect(global.fetch).toHaveBeenNthCalledWith(6, `/api/v1/notifications/channels/${encodeURIComponent(channelId)}`, expect.objectContaining({ method: 'DELETE' }));
    });

    it('should list notification logs and return [] for null data', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: null })
      });

      const result = await api.listNotificationLogs({ channel_id: 'ch-1', limit: 10 });

      expect(global.fetch).toHaveBeenCalledWith('/api/v1/notifications/log?channel_id=ch-1&limit=10', expect.any(Object));
      expect(result).toEqual([]);
    });
  });

  describe('Payment and Worker Pricing Methods', () => {
    it('should call estimateCost with POST body', async () => {
      const payload = { worker_pubkey: 'worker1', image: 'ghcr.io/acme/app:latest' };
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { estimated_sats: 1234 } })
      });

      const result = await api.estimateCost(payload);
      expect(global.fetch).toHaveBeenCalledWith('/api/v1/payments/estimate', expect.objectContaining({ method: 'POST', body: JSON.stringify(payload) }));
      expect(result).toEqual({ estimated_sats: 1234 });
    });

    it('should call getRunCost with encoded run id', async () => {
      const runId = 'run/123';
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { run_id: runId, cost_sats: 99 } })
      });

      const result = await api.getRunCost(runId);
      expect(global.fetch).toHaveBeenCalledWith(`/api/v1/deployments/runs/${encodeURIComponent(runId)}/cost`, expect.any(Object));
      expect(result).toEqual({ run_id: runId, cost_sats: 99 });
    });

    it('should call getPaymentHistory with query parameters', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: [{ id: 'p-1' }] })
      });

      const result = await api.getPaymentHistory({ worker: 'pubkey123', limit: 50 });
      expect(global.fetch).toHaveBeenCalledWith('/api/v1/payments/history?worker=pubkey123&limit=50', expect.any(Object));
      expect(result).toEqual([{ id: 'p-1' }]);
    });

    it('should call getWorkerPricing with encoded pubkey', async () => {
      const pubkey = 'npub1/test+value';
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { pubkey, sats_per_hour: 1000 } })
      });

      const result = await api.getWorkerPricing(pubkey);
      expect(global.fetch).toHaveBeenCalledWith(`/api/v1/workers/${encodeURIComponent(pubkey)}/pricing`, expect.any(Object));
      expect(result).toEqual({ pubkey, sats_per_hour: 1000 });
    });
  });

  describe('Signature Methods', () => {
    it('should call listSignatures and default to empty array for null response', async () => {
      const artifactId = 'artifact-signatures';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: null })
      });

      const result = await api.listSignatures(artifactId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}/signatures`,
        expect.any(Object)
      );
      expect(result).toEqual([]);
    });

    it('should call listVerifiedSignatures and default to empty array for null response', async () => {
      const artifactId = 'artifact-verified-signatures';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: null })
      });

      const result = await api.listVerifiedSignatures(artifactId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}/signatures/verified`,
        expect.any(Object)
      );
      expect(result).toEqual([]);
    });

    it('should call hasVerifiedSignature with encoded artifact ID', async () => {
      const artifactId = 'artifact/v2#sig';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { has_verified_signature: true } })
      });

      const result = await api.hasVerifiedSignature(artifactId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}/signatures/check`,
        expect.any(Object)
      );
      expect(result).toEqual({ has_verified_signature: true });
    });

    it('should call getSignature with encoded signature ID', async () => {
      const id = 'sig/abc:123';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id, valid: true } })
      });

      const result = await api.getSignature(id);

      expect(global.fetch).toHaveBeenCalledWith(`/api/v1/signatures/${encodeURIComponent(id)}`, expect.any(Object));
      expect(result).toEqual({ id, valid: true });
    });

    it('should call verifySignatures with correct method and path', async () => {
      const artifactId = 'artifact-xyz';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({
          data: {
            verified: true,
            signatures: [{ pubkey: 'abc123', valid: true }]
          }
        })
      });

      const result = await api.verifySignatures(artifactId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}/signatures/verify`,
        expect.objectContaining({
          method: 'POST'
        })
      );
      expect(result).toEqual({
        verified: true,
        signatures: [{ pubkey: 'abc123', valid: true }]
      });
    });

    it('should encode special characters in artifact ID for verifySignatures', async () => {
      const artifactId = 'artifact@v2.0#build';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { verified: false, signatures: [] } })
      });

      await api.verifySignatures(artifactId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/artifacts/${encodeURIComponent(artifactId)}/signatures/verify`,
        expect.any(Object)
      );
    });

    it('should handle verification failure with Bahia envelope', async () => {
      const artifactId = 'artifact-unsigned';

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({
          data: {
            verified: false,
            signatures: [],
            error: 'No signatures found'
          }
        })
      });

      const result = await api.verifySignatures(artifactId);

      expect(result.verified).toBe(false);
    });
  });
});
