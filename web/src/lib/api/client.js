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
    const data = await res.json();
    
    if (data.error) {
      throw new Error(data.error);
    }
    return data.data;
  }

  // Services
  listServices() { return this.fetch('/services'); }
  getService(id) { return this.fetch(`/services/${id}`); }

  // Environments
  listEnvironments() { return this.fetch('/environments'); }
  getEnvironment(id) { return this.fetch(`/environments/${id}`); }

  // State
  listStates() { return this.fetch('/state'); }
  listDriftedStates() { return this.fetch('/state/drifted'); }

  // Deployments
  listIntents(serviceId, envId) {
    return this.fetch(`/deployments/intents?service_id=${serviceId}&environment_id=${envId}`);
  }
  getIntent(id) { return this.fetch(`/deployments/intents/${id}`); }
  createIntent(serviceId, envId, artifactId) {
    return this.fetch('/deployments/intents', {
      method: 'POST',
      body: JSON.stringify({ service_id: serviceId, environment_id: envId, artifact_id: artifactId })
    });
  }
  approveIntent(id) { return this.fetch(`/deployments/intents/${id}/approve`, { method: 'POST' }); }
  
  // Runs
  getRun(id) { return this.fetch(`/deployments/runs/${id}`); }
  getRunLogs(id, tail = 100) { return this.fetch(`/deployments/runs/${id}/logs?tail=${tail}`); }

  // Workers
  listWorkers() { return this.fetch('/workers'); }
  getWorker(pubkey) { return this.fetch(`/workers/${pubkey}`); }

  // Policies
  listPolicies() { return this.fetch('/policies'); }
  getPolicy(id) { return this.fetch(`/policies/${id}`); }

  // Secrets
  listSecrets(serviceId) { return this.fetch(`/services/${serviceId}/secrets`); }

  // Organizations
  listOrgs() { return this.fetch('/orgs'); }
  getOrg(id) { return this.fetch(`/orgs/${id}`); }
  listOrgMembers(orgId) { return this.fetch(`/orgs/${orgId}/members`); }

  // Artifacts
  listArtifacts(serviceId) { return this.fetch(`/services/${serviceId}/artifacts`); }

  // Builds
  listBuilds(serviceId) { return this.fetch(`/services/${serviceId}/builds`); }

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

export const api = new BahiaClient();
export default api;
