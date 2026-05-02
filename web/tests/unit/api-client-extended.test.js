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

  describe('Policy Methods', () => {
    it('should call createPolicy with correct method and body', async () => {
      const payload = {
        name: 'Production Approval Required',
        rules: [{ type: 'require_approval', environments: ['production'] }]
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
  });

  describe('Signature Methods', () => {
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
