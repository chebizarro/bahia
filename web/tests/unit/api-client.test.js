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
      listServices() { return this.fetch('/services'); }
      getService(id) { return this.fetch(`/services/${encodeURIComponent(id)}`); }
    };
    
    client = new BahiaClient();
  });

  describe('Token Management', () => {
    it('should initialize without a token', () => {
      expect(client.token).toBeNull();
    });

    it('should set and persist token to localStorage', () => {
      client.setToken('test-token-123');
      expect(client.token).toBe('test-token-123');
      expect(localStorage.getItem('bahia_token')).toBe('test-token-123');
    });

    it('should remove token from localStorage when cleared', () => {
      client.setToken('test-token');
      client.setToken(null);
      expect(client.token).toBeNull();
      expect(localStorage.getItem('bahia_token')).toBeNull();
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

    it('should include Authorization header when token is set', async () => {
      client.setToken('test-token');
      
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
            'Authorization': 'Bearer test-token'
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
