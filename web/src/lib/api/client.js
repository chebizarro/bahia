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

  // Secrets
  listSecrets(serviceId) { return this.fetch(`/services/${encodeURIComponent(serviceId)}/secrets`); }

  // Organizations
  listOrgs() { return this.fetch('/orgs'); }
  getOrg(id) { return this.fetch(`/orgs/${encodeURIComponent(id)}`); }
  listOrgMembers(orgId) { return this.fetch(`/orgs/${encodeURIComponent(orgId)}/members`); }

  // Artifacts
  listArtifacts(serviceId) { return this.fetch(`/services/${encodeURIComponent(serviceId)}/artifacts`); }

  // Builds
  listBuilds(serviceId) { return this.fetch(`/services/${encodeURIComponent(serviceId)}/builds`); }

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
}

// Only instantiate in browser context
export const api = typeof window !== 'undefined' ? new BahiaClient() : null;
export default api;
