// Bahia API Client
const BASE_URL = '/api/v1';

class BahiaClient {
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

  // Query parameter helper
  query(params) {
    if (!params || typeof params !== 'object') return '';
    
    const pairs = [];
    for (const [key, value] of Object.entries(params)) {
      // Omit null, undefined, and empty string values
      if (value === null || value === undefined || value === '') continue;
      
      // Handle arrays by comma-joining
      if (Array.isArray(value)) {
        if (value.length > 0) {
          pairs.push(`${encodeURIComponent(key)}=${encodeURIComponent(value.join(','))}`);
        }
      } else {
        // Serialize booleans and numbers as strings
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

    const res = await fetch(`${BASE_URL}${path}`, { ...options, headers });
    
    // Handle non-2xx responses
    if (!res.ok) {
      let errorMessage = `HTTP ${res.status}: ${res.statusText}`;
      try {
        const errorData = await res.json();
        if (errorData.error) {
          errorMessage = errorData.error;
        }
      } catch {
        // Response body is not JSON or empty, use status text
      }
      throw new Error(errorMessage);
    }

    // Handle empty responses (e.g., 204 No Content)
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

  // Services
  listServices() { return this.fetch('/services'); }
  getService(id) { return this.fetch(`/services/${encodeURIComponent(id)}`); }
  createService(payload) {
    return this.fetch('/services', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }
  updateService(id, patch) {
    return this.fetch(`/services/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(patch)
    });
  }
  deleteService(id, force = false) {
    const query = force ? '?force=true' : '';
    return this.fetch(`/services/${encodeURIComponent(id)}${query}`, {
      method: 'DELETE'
    });
  }

  // Environments
  listEnvironments() { return this.fetch('/environments'); }
  getEnvironment(id) { return this.fetch(`/environments/${encodeURIComponent(id)}`); }
  createEnvironment(payload) {
    return this.fetch('/environments', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }
  updateEnvironment(id, patch) {
    return this.fetch(`/environments/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(patch)
    });
  }
  deleteEnvironment(id) {
    return this.fetch(`/environments/${encodeURIComponent(id)}`, {
      method: 'DELETE'
    });
  }

  // State
  listStates() { return this.fetch('/state'); }
  listDriftedStates() { return this.fetch('/state/drifted'); }

  // Deployments
  listIntents(serviceId, envId) {
    return this.fetch(`/services/${encodeURIComponent(serviceId)}/environments/${encodeURIComponent(envId)}/intents`);
  }
  getIntent(id) { return this.fetch(`/deployments/intents/${encodeURIComponent(id)}`); }
  createIntent(serviceId, envId, artifactId) {
    return this.fetch('/deployments/intents', {
      method: 'POST',
      body: JSON.stringify({ service_id: serviceId, environment_id: envId, artifact_id: artifactId })
    });
  }
  approveIntent(id) {
    return this.fetch(`/deployments/intents/${encodeURIComponent(id)}/approve`, { method: 'POST' });
  }
  rejectIntent(id) {
    return this.fetch(`/deployments/intents/${encodeURIComponent(id)}/reject`, { method: 'POST' });
  }
  
  // Runs
  getRun(id) { return this.fetch(`/deployments/runs/${encodeURIComponent(id)}`); }
  getRunLogs(id, tail = 100) {
    return this.fetch(`/deployments/runs/${encodeURIComponent(id)}/logs?tail=${encodeURIComponent(tail)}`);
  }
  listRuns(intentId) {
    return this.fetch(`/deployments/intents/${encodeURIComponent(intentId)}/runs`);
  }
  createRun(payload) {
    return this.fetch('/deployments/runs', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }
  completeRun(id, payload) {
    return this.fetch(`/deployments/runs/${encodeURIComponent(id)}/complete`, {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }
  rollback(payload) {
    return this.fetch('/rollback', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }

  // Workers
  listWorkers() { return this.fetch('/workers'); }
  getWorker(pubkey) { return this.fetch(`/workers/${encodeURIComponent(pubkey)}`); }

  // Policies
  listPolicies() { return this.fetch('/policies'); }
  getPolicy(id) { return this.fetch(`/policies/${encodeURIComponent(id)}`); }
  createPolicy(payload) {
    return this.fetch('/policies', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }
  updatePolicy(id, patch) {
    return this.fetch(`/policies/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(patch)
    });
  }
  deletePolicy(id) {
    return this.fetch(`/policies/${encodeURIComponent(id)}`, {
      method: 'DELETE'
    });
  }
  evaluatePolicy(payload) {
    return this.fetch('/policies/evaluate', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }

  // Secrets
  listSecrets(serviceId) { return this.fetch(`/services/${encodeURIComponent(serviceId)}/secrets`); }
  createSecret(serviceId, payload) {
    return this.fetch(`/services/${encodeURIComponent(serviceId)}/secrets`, {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }
  updateSecret(serviceId, secretId, payload) {
    return this.fetch(`/services/${encodeURIComponent(serviceId)}/secrets/${encodeURIComponent(secretId)}`, {
      method: 'PUT',
      body: JSON.stringify(payload)
    });
  }
  deleteSecret(serviceId, secretId) {
    return this.fetch(`/services/${encodeURIComponent(serviceId)}/secrets/${encodeURIComponent(secretId)}`, {
      method: 'DELETE'
    });
  }

  // Organizations
  listOrgs() { return this.fetch('/orgs'); }
  getOrg(id) { return this.fetch(`/orgs/${encodeURIComponent(id)}`); }
  listOrgMembers(orgId) { return this.fetch(`/orgs/${encodeURIComponent(orgId)}/members`); }

  // Artifacts
  listArtifacts(serviceId) { return this.fetch(`/services/${encodeURIComponent(serviceId)}/artifacts`); }

  // Builds
  listBuilds(serviceId) { return this.fetch(`/services/${encodeURIComponent(serviceId)}/builds`); }

  // Notifications
  listNotificationChannels(params = {}) {
    return this.fetch(`/notifications/channels${this.query(params)}`);
  }
  getNotificationChannel(id) {
    return this.fetch(`/notifications/channels/${encodeURIComponent(id)}`);
  }
  createNotificationChannel(payload) {
    return this.fetch('/notifications/channels', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }
  updateNotificationChannel(id, patch) {
    return this.fetch(`/notifications/channels/${encodeURIComponent(id)}`, {
      method: 'PUT',
      body: JSON.stringify(patch)
    });
  }
  deleteNotificationChannel(id) {
    return this.fetch(`/notifications/channels/${encodeURIComponent(id)}`, {
      method: 'DELETE'
    });
  }
  testNotificationChannel(id) {
    return this.fetch(`/notifications/channels/${encodeURIComponent(id)}/test`, {
      method: 'POST'
    });
  }
  listNotificationLogs(params = {}) {
    return this.fetch(`/notifications/log${this.query(params)}`);
  }

  // Payments
  estimateCost(payload) {
    return this.fetch('/payments/estimate', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }
  getRunCost(runId) {
    return this.fetch(`/deployments/runs/${encodeURIComponent(runId)}/cost`);
  }
  getPaymentHistory(params = {}) {
    return this.fetch(`/payments/history${this.query(params)}`);
  }

  // SBOM
  getSBOM(artifactId) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/sbom`);
  }
  getSBOMPackages(artifactId, params = {}) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/sbom/packages${this.query(params)}`);
  }
  searchSBOMPackages(params = {}) {
    return this.fetch(`/sbom/search${this.query(params)}`);
  }
  ingestSBOM(artifactId, payload) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/sbom`, {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }

  // Signatures
  listSignatures(artifactId) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/signatures`);
  }
  listVerifiedSignatures(artifactId) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/signatures/verified`);
  }
  hasVerifiedSignature(artifactId) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/signatures/check`);
  }
  getSignature(id) {
    return this.fetch(`/signatures/${encodeURIComponent(id)}`);
  }
  verifySignatures(artifactId) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/signatures/verify`, {
      method: 'POST'
    });
  }

  // SSE Event Stream
  streamEvents(types = [], onEvent, onError) {
    const url = types.length > 0 
      ? `${BASE_URL}/events/stream?types=${types.join(',')}`
      : `${BASE_URL}/events/stream`;
    
    const eventSource = new EventSource(url);
    
    eventSource.onmessage = (e) => {
      try {
        const data = JSON.parse(e.data);
        onEvent(data);
      } catch (err) {
        console.error('Failed to parse SSE event:', err);
      }
    };

    eventSource.onerror = (err) => {
      if (onError) onError(err);
    };

    return () => eventSource.close();
  }

  // Live Logs SSE
  streamLogs(serviceId, envId, tail = 100, onLog, onError) {
    const url = `${BASE_URL}/services/${serviceId}/environments/${envId}/logs?follow=true&tail=${tail}`;
    const eventSource = new EventSource(url);

    eventSource.addEventListener('log', (e) => {
      try {
        const data = JSON.parse(e.data);
        onLog(data);
      } catch (err) {
        console.error('Failed to parse log event:', err);
      }
    });

    eventSource.onerror = (err) => {
      if (onError) onError(err);
    };

    return () => eventSource.close();
  }

  // Auth Exchange
  // Exchange NIP-98 signed event for a JWT token
  async exchangeNostrAuth(event) {
    // This endpoint is unauthenticated, so we temporarily clear the token
    const savedToken = this.token;
    this.token = null;
    
    try {
      const res = await fetch(`${BASE_URL}/auth/nostr`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json'
        },
        body: JSON.stringify({ event })
      });

      if (!res.ok) {
        let errorMessage = `HTTP ${res.status}: ${res.statusText}`;
        try {
          const errorData = await res.json();
          if (errorData.error) {
            errorMessage = errorData.error;
          }
        } catch {
          // Response body is not JSON or empty
        }
        throw new Error(errorMessage);
      }

      const data = await res.json();
      
      if (data.error) {
        throw new Error(data.error);
      }
      
      return data.data;
    } finally {
      // Restore the saved token
      this.token = savedToken;
    }
  }
}

// Only instantiate in browser context
export const api = typeof window !== 'undefined' ? new BahiaClient() : null;
export default api;
