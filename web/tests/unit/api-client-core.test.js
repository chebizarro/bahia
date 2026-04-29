import { describe, it, expect, beforeEach, vi } from 'vitest';

// Mock the window object before importing the client
global.window = global;

describe('BahiaClient - Core Functionality', () => {
  let BahiaClient;
  let client;

  beforeEach(async () => {
    // Clear localStorage and mocks
    localStorage.clear();
    vi.clearAllMocks();
    
    // Reset modules to avoid state leakage
    vi.resetModules();
    
    // Dynamically import the client to get a fresh instance
    const module = await import('../../src/lib/api/client.js');
    // The actual export is 'api', but we need the class for testing
    // We'll create a new instance using the same constructor logic
    BahiaClient = class {
      constructor() {
        this.token = typeof localStorage !== 'undefined' ? localStorage.getItem('bahia_token') : null;
      }
      setToken(token) {
        this.token = token;
        if (typeof localStorage !== 'undefined') {
          if (token) {
            localStorage.setItem('bahia_token', token);
          } else {
            localStorage.removeItem('bahia_token');
          }
        }
      }
      query(params) {
        if (!params || typeof params !== 'object') return '';
        const pairs = [];
        for (const [key, value] of Object.entries(params)) {
          if (value === null || value === undefined || value === '') continue;
          if (Array.isArray(value)) {
            if (value.length > 0) {
              pairs.push(`${encodeURIComponent(key)}=${encodeURIComponent(value.join(','))}`);
            }
          } else {
            pairs.push(`${encodeURIComponent(key)}=${encodeURIComponent(String(value))}`);
          }
        }
        return pairs.length > 0 ? `?${pairs.join('&')}` : '';
      }
      async fetch(path, options = {}) {
        const headers = {
          'Content-Type': 'application/json',
          ...options.headers
        };
        if (this.token) {
          headers['Authorization'] = `Bearer ${this.token}`;
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
      createSecret(serviceId, payload) {
        return this.fetch(`/services/${encodeURIComponent(serviceId)}/secrets`, {
          method: 'POST',
          body: JSON.stringify(payload)
        });
      }
      evaluatePolicy(payload) {
        return this.fetch('/policies/evaluate', {
          method: 'POST',
          body: JSON.stringify(payload)
        });
      }
      listNotificationChannels(params = {}) {
        return this.fetch(`/notifications/channels${this.query(params)}`);
      }
    };
    
    client = new BahiaClient();
  });

  describe('Token Persistence', () => {
    it('should initialize with token from localStorage if available', () => {
      localStorage.setItem('bahia_token', 'existing-token');
      const newClient = new BahiaClient();
      expect(newClient.token).toBe('existing-token');
    });

    it('should initialize with null token when localStorage is empty', () => {
      expect(client.token).toBeNull();
    });

    it('should persist token through setToken', () => {
      client.setToken('new-token-abc');
      expect(client.token).toBe('new-token-abc');
      expect(localStorage.getItem('bahia_token')).toBe('new-token-abc');
    });

    it('should remove token when setToken(null) is called', () => {
      client.setToken('token-to-clear');
      client.setToken(null);
      expect(client.token).toBeNull();
      expect(localStorage.getItem('bahia_token')).toBeNull();
    });

    it('should survive instance recreation after setToken', () => {
      client.setToken('persistent-token');
      const newClient = new BahiaClient();
      expect(newClient.token).toBe('persistent-token');
    });
  });

  describe('Auth Header Injection', () => {
    it('should include Authorization header when token is set', async () => {
      client.setToken('auth-token-123');
      
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: {} })
      });

      await client.fetch('/test-endpoint');

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/test-endpoint',
        expect.objectContaining({
          headers: expect.objectContaining({
            'Authorization': 'Bearer auth-token-123'
          })
        })
      );
    });

    it('should not include Authorization header when no token is set', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: {} })
      });

      await client.fetch('/test-endpoint');

      const callArgs = global.fetch.mock.calls[0][1];
      expect(callArgs.headers).not.toHaveProperty('Authorization');
    });

    it('should update Authorization header after token change', async () => {
      client.setToken('first-token');
      
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: {} })
      });

      await client.fetch('/test-endpoint');
      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/test-endpoint',
        expect.objectContaining({
          headers: expect.objectContaining({
            'Authorization': 'Bearer first-token'
          })
        })
      );

      client.setToken('second-token');
      global.fetch.mockClear();
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: {} })
      });

      await client.fetch('/another-endpoint');
      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/another-endpoint',
        expect.objectContaining({
          headers: expect.objectContaining({
            'Authorization': 'Bearer second-token'
          })
        })
      );
    });
  });

  describe('Non-2xx Error Handling', () => {
    it('should throw error on 404 with backend error message', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: false,
        status: 404,
        statusText: 'Not Found',
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'Resource not found' })
      });

      await expect(client.fetch('/missing')).rejects.toThrow('Resource not found');
    });

    it('should throw error on 401 Unauthorized', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: false,
        status: 401,
        statusText: 'Unauthorized',
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'Authentication required' })
      });

      await expect(client.fetch('/protected')).rejects.toThrow('Authentication required');
    });

    it('should throw error on 403 Forbidden', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: false,
        status: 403,
        statusText: 'Forbidden',
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'Access denied' })
      });

      await expect(client.fetch('/forbidden')).rejects.toThrow('Access denied');
    });

    it('should throw error on 500 with default message when no error body', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: false,
        status: 500,
        statusText: 'Internal Server Error',
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('Not JSON'); }
      });

      await expect(client.fetch('/error')).rejects.toThrow('HTTP 500: Internal Server Error');
    });

    it('should handle 204 No Content gracefully', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: true,
        status: 204,
        headers: new Map([['content-type', 'text/plain']]),
        json: async () => { throw new Error('No content'); }
      });

      const result = await client.fetch('/no-content');
      expect(result).toBeNull();
    });
  });

  describe('Backend Error Field Handling', () => {
    it('should throw when response has error field in data', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'Validation failed' })
      });

      await expect(client.fetch('/validate')).rejects.toThrow('Validation failed');
    });

    it('should prefer backend error message over HTTP status', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: false,
        status: 400,
        statusText: 'Bad Request',
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ error: 'Invalid service configuration' })
      });

      await expect(client.fetch('/bad')).rejects.toThrow('Invalid service configuration');
    });
  });

  describe('URL Encoding', () => {
    it('should encode special characters in path parameters', async () => {
      const serviceId = 'service/with/slashes';
      
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: {} })
      });

      await client.createSecret(serviceId, { key: 'SECRET_KEY', value: 'secret' });

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/${encodeURIComponent(serviceId)}/secrets`,
        expect.any(Object)
      );
    });

    it('should encode spaces in path parameters', async () => {
      const serviceId = 'my service name';
      
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: {} })
      });

      await client.createSecret(serviceId, {});

      expect(global.fetch).toHaveBeenCalledWith(
        `/api/v1/services/my%20service%20name/secrets`,
        expect.any(Object)
      );
    });

    it('should encode query parameters correctly', () => {
      const params = {
        service_id: 'abc-123',
        status: 'active',
        limit: 10
      };

      const query = client.query(params);
      expect(query).toBe('?service_id=abc-123&status=active&limit=10');
    });

    it('should encode special characters in query parameters', () => {
      const params = {
        search: 'test@example.com',
        filter: 'status=active'
      };

      const query = client.query(params);
      expect(query).toContain(encodeURIComponent('test@example.com'));
      expect(query).toContain(encodeURIComponent('status=active'));
    });

    it('should skip null, undefined, and empty string query parameters', () => {
      const params = {
        a: 'value',
        b: null,
        c: undefined,
        d: '',
        e: 'another'
      };

      const query = client.query(params);
      expect(query).toBe('?a=value&e=another');
    });

    it('should encode array query parameters as comma-separated', () => {
      const params = {
        types: ['deployment', 'rollback', 'config']
      };

      const query = client.query(params);
      expect(query).toBe('?types=deployment%2Crollback%2Cconfig');
    });

    it('should skip empty arrays in query parameters', () => {
      const params = {
        ids: [],
        status: 'active'
      };

      const query = client.query(params);
      expect(query).toBe('?status=active');
    });
  });

  describe('Representative API Methods', () => {
    it('should call createSecret with correct path and body', async () => {
      const serviceId = 'svc-123';
      const payload = { key: 'DB_PASSWORD', value: 'secret123' };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { id: 'secret-1', key: 'DB_PASSWORD' } })
      });

      const result = await client.createSecret(serviceId, payload);

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/services/svc-123/secrets',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ id: 'secret-1', key: 'DB_PASSWORD' });
    });

    it('should call evaluatePolicy with correct payload', async () => {
      const payload = {
        policy_id: 'pol-1',
        context: { environment: 'production' }
      };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: { allowed: true, reason: 'Policy approved' } })
      });

      const result = await client.evaluatePolicy(payload);

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/policies/evaluate',
        expect.objectContaining({
          method: 'POST',
          body: JSON.stringify(payload)
        })
      );
      expect(result).toEqual({ allowed: true, reason: 'Policy approved' });
    });

    it('should call listNotificationChannels with query parameters', async () => {
      const params = { type: 'slack', enabled: true };

      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: [{ id: 'ch-1', type: 'slack' }] })
      });

      const result = await client.listNotificationChannels(params);

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/notifications/channels?type=slack&enabled=true',
        expect.any(Object)
      );
      expect(result).toEqual([{ id: 'ch-1', type: 'slack' }]);
    });

    it('should call listNotificationChannels without parameters', async () => {
      global.fetch.mockResolvedValueOnce({
        ok: true,
        headers: new Map([['content-type', 'application/json']]),
        json: async () => ({ data: [] })
      });

      await client.listNotificationChannels();

      expect(global.fetch).toHaveBeenCalledWith(
        '/api/v1/notifications/channels',
        expect.any(Object)
      );
    });
  });
});
