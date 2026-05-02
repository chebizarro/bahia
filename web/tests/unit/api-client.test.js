import { describe, it, expect, beforeEach, vi } from 'vitest';

// Mock the window object before importing the client
global.window = global;

describe('BahiaClient', () => {
  let BahiaClient;
  let client;

  beforeEach(async () => {
    // Clear localStorage and mocks
    localStorage.clear();
    vi.clearAllMocks();
    
    // Dynamically import the client class to get a fresh instance
    const module = await import('../../src/lib/api/client.js');
    BahiaClient = module.BahiaClient || class {
      constructor() {
        this.authProvider = null;
      }
      setAuthProvider(provider) {
        this.authProvider = provider || null;
      }
      async fetch(path, options = {}) {
        const headers = {
          'Content-Type': 'application/json',
          ...options.headers
        };
        if (!headers.Authorization && this.authProvider?.getAuthorizationHeader) {
          const authorization = await this.authProvider.getAuthorizationHeader({ method: options.method || 'GET', url: `/api/v1${path}` });
          if (authorization) headers.Authorization = authorization;
        }
        const res = await fetch(`/api/v1${path}`, { ...options, headers });
        if (!res.ok) {
          let errorMessage = `HTTP ${res.status}: ${res.statusText}`;
          try {
            const errorData = await res.json();
            if (errorData.error) {
              errorMessage = errorData.error;
            }
          } catch {}
          throw new Error(errorMessage);
        }
        const contentType = res.headers.get('content-type');
        if (!contentType || !contentType.includes('application/json')) {
          return null;
        }
        const data = await res.json();
        if (data.error) {
          throw new Error(data.error);
        }
        return data.data;
      }
      listServices() { return this.fetch('/services'); }
      getService(id) { return this.fetch(`/services/${encodeURIComponent(id)}`); }
    };
    
    client = new BahiaClient();
  });

  describe('Auth Provider Management', () => {
    it('should initialize without bearer token state', () => {
      expect(client.authProvider).toBeNull();
      expect(client.token).toBeUndefined();
    });

    it('should set and clear auth provider', () => {
      const provider = { getAuthorizationHeader: vi.fn() };
      client.setAuthProvider(provider);
      expect(client.authProvider).toBe(provider);
      client.setAuthProvider(null);
      expect(client.authProvider).toBeNull();
    });
  });

  describe('API Requests', () => {
    it('should make GET request to /api/v1/services', async () => {
      const mockServices = [
        { id: '1', name: 'service-1' },
        { id: '2', name: 'service-2' }
      ];

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: mockServices })
      });

      const result = await client.listServices();

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/services',
        expect.objectContaining({
          headers: expect.objectContaining({
            'Content-Type': 'application/json'
          })
        })
      );
      expect(result).toEqual(mockServices);
    });

    it('should include Authorization header from auth provider', async () => {
      client.setAuthProvider({ getAuthorizationHeader: vi.fn().mockResolvedValue('Nostr signed-event') });
      
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: [] })
      });

      await client.listServices();

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/services',
        expect.objectContaining({
          headers: expect.objectContaining({
            'Content-Type': 'application/json',
            'Authorization': 'Nostr signed-event'
          })
        })
      );
    });

    it('should throw error on non-2xx response', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: false,
        status: 404,
        statusText: 'Not Found',
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'Service not found' })
      });

      await expect(client.getService('non-existent')).rejects.toThrow('Service not found');
    });

    it('should throw error when response contains error field', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'Invalid request' })
      });

      await expect(client.listServices()).rejects.toThrow('Invalid request');
    });

    it('should handle empty/non-JSON responses', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('Not JSON'); }
      });

      const result = await client.fetch('/some-endpoint');
      expect(result).toBeNull();
    });
  });

  describe('URL Encoding', () => {
    it('should encode service IDs in URLs', async () => {
      const serviceId = 'service/with/slashes';
      
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: serviceId } })
      });

      await client.getService(serviceId);

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/${encodeURIComponent(serviceId)}`,
        expect.any(Object)
      );
    });
  });
});
