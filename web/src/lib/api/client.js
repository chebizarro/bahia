// Bahia API Client
const BASE_URL = '/api/v1';

export class BahiaClient {
  constructor() {
    this.authProvider = null;
  }

  setAuthProvider(provider) {
    this.authProvider = provider || null;
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
    const {
      retries: retryOverride,
      retryDelayMs: retryDelayOverride,
      retryStatuses: retryStatusesOverride,
      ...fetchOptions
    } = options;
    const method = fetchOptions.method || 'GET';
    const url = `${BASE_URL}${path}`;
    const headers = {
      'Content-Type': 'application/json',
      ...fetchOptions.headers
    };

    if (!headers.Authorization && this.authProvider?.getAuthorizationHeader) {
      const authorization = await this.authProvider.getAuthorizationHeader({ method, url });
      if (authorization) {
        headers.Authorization = authorization;
      }
    }

    const retries = Number.isInteger(retryOverride) ? Math.max(0, retryOverride) : (method === 'GET' ? 1 : 0);
    const retryDelayMs = Number.isFinite(retryDelayOverride) ? Math.max(0, retryDelayOverride) : 200;
    const retryStatuses = Array.isArray(retryStatusesOverride) ? new Set(retryStatusesOverride) : null;
    const isRetriableStatus = (status) => {
      if (retryStatuses) {
        return retryStatuses.has(status);
      }
      return status >= 500 && status <= 599;
    };

    const waitForRetry = (attempt) => new Promise((resolve) => {
      setTimeout(resolve, retryDelayMs * (2 ** attempt));
    });

    let res;
    for (let attempt = 0; ; attempt++) {
      try {
        res = await fetch(url, { ...fetchOptions, method, headers });
      } catch (error) {
        if (attempt >= retries) {
          throw error;
        }
        await waitForRetry(attempt);
        continue;
      }

      if (!res.ok && isRetriableStatus(res.status) && attempt < retries) {
        await waitForRetry(attempt);
        continue;
      }

      break;
    }
    
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
  listServices() { return this.fetch('/services').then(r => r ?? []); }
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
  listEnvironments() { return this.fetch('/environments').then(r => r ?? []); }
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
  listStates() { return this.fetch('/state').then(r => r ?? []); }
  listDriftedStates() { return this.fetch('/state/drifted').then(r => r ?? []); }
  getStateByServiceEnv(serviceId, envId) {
    return this.fetch(`/services/${encodeURIComponent(serviceId)}/environments/${encodeURIComponent(envId)}/state`);
  }
  listStatesByEnvironment(envId) {
    return this.fetch(`/environments/${encodeURIComponent(envId)}/state`).then(r => r ?? []);
  }
  recordObservation(payload) {
    return this.fetch('/observations', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }

  // Deployments
  listIntents(serviceId, envId) {
    return this.fetch(`/services/${encodeURIComponent(serviceId)}/environments/${encodeURIComponent(envId)}/intents`).then(r => r ?? []);
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
  getRunLogs(id, tail = 100, stream = 'merged') {
    const params = new URLSearchParams({ tail: String(tail) });
    if (stream) params.set('stream', stream);
    return this.fetch(`/deployments/runs/${encodeURIComponent(id)}/logs?${params.toString()}`);
  }
  listRuns(intentId) {
    return this.fetch(`/deployments/intents/${encodeURIComponent(intentId)}/runs`).then(r => r ?? []);
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

  // Adoption
  scanAdoption(payload) {
    return this.fetch('/adoption/scan', {
      method: 'POST',
      body: JSON.stringify(payload)
    }).then(r => r ?? []);
  }
  importAdoption(payload) {
    return this.fetch('/adoption/import', {
      method: 'POST',
      body: JSON.stringify(payload)
    }).then(r => r ?? []);
  }

  // Direct runtime actions
  deployService(serviceId, envId, artifactId = null) {
    const body = artifactId ? { artifact_id: artifactId } : undefined;
    return this.fetch(`/services/${encodeURIComponent(serviceId)}/environments/${encodeURIComponent(envId)}/deploy`, {
      method: 'POST',
      ...(body ? { body: JSON.stringify(body) } : {})
    });
  }
  restartService(serviceId, envId) {
    return this.fetch(`/services/${encodeURIComponent(serviceId)}/environments/${encodeURIComponent(envId)}/restart`, {
      method: 'POST'
    });
  }
  stopService(serviceId, envId) {
    return this.fetch(`/services/${encodeURIComponent(serviceId)}/environments/${encodeURIComponent(envId)}/stop`, {
      method: 'POST'
    });
  }

  // Workers
  listWorkers() { return this.fetch('/workers').then(r => r ?? []); }
  getWorker(pubkey) { return this.fetch(`/workers/${encodeURIComponent(pubkey)}`); }
  getWorkerPricing(pubkey) {
    return this.fetch(`/workers/${encodeURIComponent(pubkey)}/pricing`);
  }

  // Policies
  listPolicies() { return this.fetch('/policies').then(r => r ?? []); }
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
  listSecrets(serviceId) { return this.fetch(`/services/${encodeURIComponent(serviceId)}/secrets`).then(r => r ?? []); }
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
  listOrgs() { return this.fetch('/orgs').then(r => r ?? []); }
  getOrg(id) { return this.fetch(`/orgs/${encodeURIComponent(id)}`); }
  listOrgMembers(orgId) { return this.fetch(`/orgs/${encodeURIComponent(orgId)}/members`).then(r => r ?? []); }

  // Artifacts
  listArtifacts(serviceId) { return this.fetch(`/services/${encodeURIComponent(serviceId)}/artifacts`).then(r => r ?? []); }
  getArtifact(id) { return this.fetch(`/artifacts/${encodeURIComponent(id)}`); }
  registerArtifact(payload) {
    return this.fetch('/artifacts', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }

  // Builds
  listBuilds(serviceId) { return this.fetch(`/services/${encodeURIComponent(serviceId)}/builds`).then(r => r ?? []); }
  getBuild(id) { return this.fetch(`/builds/${encodeURIComponent(id)}`); }
  registerBuild(payload) {
    return this.fetch('/builds', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }
  updateBuildStatus(id, status) {
    return this.fetch(`/builds/${encodeURIComponent(id)}/status`, {
      method: 'PATCH',
      body: JSON.stringify({ status })
    });
  }

  // Notifications
  listNotificationChannels(params = {}) {
    return this.fetch(`/notifications/channels${this.query(params)}`).then(r => r ?? []);
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
    return this.fetch(`/notifications/log${this.query(params)}`).then(r => r ?? []);
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
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/signatures`).then(r => r ?? []);
  }
  listVerifiedSignatures(artifactId) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/signatures/verified`).then(r => r ?? []);
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

  // CI Lookup
  async lookupRepositoryCI(repoCoordinates, { includeDisabledPolicies = false } = {}) {
    const payload = await this.fetch('/repositories/ci/lookup', {
      method: 'POST',
      body: JSON.stringify({
        repo_coordinates: repoCoordinates,
        include_disabled_policies: includeDisabledPolicies
      })
    });
    return payload?.results || [];
  }

  // System Info
  async getSystemInfo() {
    return this.fetch('/system/info');
  }

  // Blossom Artifacts
  /**
   * List blobs from configured Blossom servers.
   * @param {string} [pubkey] - Optional pubkey to filter blobs by owner
   * @returns {Promise<Array<{url: string, sha256: string, size: number, type: string, uploaded: string}>>}
   */
  async listBlossomBlobs(pubkey = null) {
    const body = pubkey ? { pubkey } : {};
    return this.fetch('/blossom/list', {
      method: 'POST',
      body: JSON.stringify(body)
    }).then(r => r ?? []);
  }

  /**
   * Get list of configured Blossom server URLs.
   * @returns {Promise<string[]>}
   */
  async getBlossomServers() {
    return this.fetch('/blossom/servers').then(r => r ?? []);
  }

  /**
   * Check health of all Blossom servers.
   * @returns {Promise<Record<string, string>>} Map of server URL to status ("ok" or error message)
   */
  async checkBlossomHealth() {
    return this.fetch('/blossom/health').then(r => r ?? {});
  }

  /**
   * Get upload/download statistics for Blossom servers.
   * @returns {Promise<Record<string, {uploads: number, downloads: number, failures: number}>>}
   */
  async getBlossomStats() {
    return this.fetch('/blossom/stats').then(r => r ?? {});
  }

  // ============ Organizations ============

  // List organizations the current user belongs to
  async listOrgs() {
    return this.fetch('/orgs').then(r => r ?? []);
  }

  // Get a single organization by ID
  async getOrg(id) {
    return this.fetch(`/orgs/${id}`);
  }

  // Create a new organization
  async createOrg({ name, displayName }) {
    return this.fetch('/orgs', {
      method: 'POST',
      body: JSON.stringify({ name, display_name: displayName })
    });
  }

  // Update an organization
  async updateOrg(id, { displayName }) {
    return this.fetch(`/orgs/${id}`, {
      method: 'PUT',
      body: JSON.stringify({ display_name: displayName })
    });
  }

  // Delete an organization
  async deleteOrg(id) {
    return this.fetch(`/orgs/${id}`, { method: 'DELETE' });
  }

  // ============ Organization Members ============

  // List members of an organization
  async listOrgMembers(orgId) {
    return this.fetch(`/orgs/${orgId}/members`).then(r => r ?? []);
  }

  // Add a member to an organization
  async addOrgMember(orgId, { pubkey, role }) {
    return this.fetch(`/orgs/${orgId}/members`, {
      method: 'POST',
      body: JSON.stringify({ pubkey, role })
    });
  }

  // Update a member's role
  async updateOrgMemberRole(orgId, pubkey, { role }) {
    return this.fetch(`/orgs/${orgId}/members/${pubkey}`, {
      method: 'PUT',
      body: JSON.stringify({ role })
    });
  }

  // Remove a member from an organization
  async removeOrgMember(orgId, pubkey) {
    return this.fetch(`/orgs/${orgId}/members/${pubkey}`, { method: 'DELETE' });
  }

  // ============ Organization Invites ============

  // List invites for an organization
  async listOrgInvites(orgId) {
    return this.fetch(`/orgs/${orgId}/invites`).then(r => r ?? []);
  }

  // Create an invite
  async createOrgInvite(orgId, { pubkey, role, expiresIn }) {
    return this.fetch(`/orgs/${orgId}/invites`, {
      method: 'POST',
      body: JSON.stringify({ pubkey, role, expires_in: expiresIn })
    });
  }

  // Revoke an invite
  async revokeOrgInvite(orgId, inviteId) {
    return this.fetch(`/orgs/${orgId}/invites/${inviteId}`, { method: 'DELETE' });
  }

  // Get my pending invites
  async getMyInvites() {
    return this.fetch('/me/invites').then(r => r ?? []);
  }

  // Accept an invite
  async acceptInvite(inviteId) {
    return this.fetch(`/invites/${inviteId}/accept`, { method: 'POST' });
  }
}

// Only instantiate in browser context
export const api = typeof window !== 'undefined' ? new BahiaClient() : null;
export default api;
