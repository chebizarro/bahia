const BASE_URL = '/api/v1';

export class BahiaClient {
  constructor() {
    this.authProvider = null;
  }

  setAuthProvider(provider) {
    this.authProvider = provider || null;
  }

  query(params) {
    if (!params || typeof params !== 'object') return '';

    const pairs = [];
    for (const [key, value] of Object.entries(params)) {
      if (value === null || value === undefined || value === '') continue;
      if (Array.isArray(value)) {
        if (value.length > 0) pairs.push(`${encodeURIComponent(key)}=${encodeURIComponent(value.join(','))}`);
      } else {
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
      if (authorization) headers.Authorization = authorization;
    }

    const retries = Number.isInteger(retryOverride) ? Math.max(0, retryOverride) : (method === 'GET' ? 1 : 0);
    const retryDelayMs = Number.isFinite(retryDelayOverride) ? Math.max(0, retryDelayOverride) : 200;
    const retryStatuses = Array.isArray(retryStatusesOverride) ? new Set(retryStatusesOverride) : null;
    const isRetriableStatus = (status) => retryStatuses ? retryStatuses.has(status) : status >= 500 && status <= 599;
    const waitForRetry = (attempt) => new Promise((resolve) => setTimeout(resolve, retryDelayMs * (2 ** attempt)));

    let res;
    for (let attempt = 0; ; attempt++) {
      try {
        res = await fetch(url, { ...fetchOptions, method, headers });
      } catch (error) {
        if (attempt >= retries) throw error;
        await waitForRetry(attempt);
        continue;
      }

      if (!res.ok && isRetriableStatus(res.status) && attempt < retries) {
        await waitForRetry(attempt);
        continue;
      }
      break;
    }

    if (!res.ok) {
      let errorMessage = `HTTP ${res.status}: ${res.statusText}`;
      try {
        const errorData = await res.json();
        if (errorData.error) errorMessage = errorData.error;
      } catch {
        // Keep the HTTP status fallback when the response is not JSON.
      }
      throw new Error(errorMessage);
    }

    const contentType = res.headers.get('content-type');
    if (!contentType || !contentType.includes('application/json')) return null;

    const data = await res.json();
    if (data.error) throw new Error(data.error);
    return data.data;
  }

  listConfigFabricDrift() {
    return this.fetch('/config-fabric/drift').then((result) => result ?? []);
  }

  publishConfigFabricEvent(payload) {
    return this.fetch('/config-fabric/events', {
      method: 'POST',
      body: JSON.stringify(payload)
    });
  }

  rollbackConfigFabricEvent(eventId) {
    return this.fetch('/config-fabric/rollback', {
      method: 'POST',
      body: JSON.stringify({ event_id: eventId })
    });
  }

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

  getSBOMAttestation(artifactId) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/sbom/attestation`);
  }

  getSBOMNTIACompliance(artifactId) {
    return this.fetch(`/artifacts/${encodeURIComponent(artifactId)}/sbom/ntia`);
  }

  async listBlossomBlobs(pubkey = null) {
    const body = pubkey ? { pubkey } : {};
    return this.fetch('/blossom/list', {
      method: 'POST',
      body: JSON.stringify(body)
    }).then((r) => r ?? []);
  }

  async getBlossomServers() {
    return this.fetch('/blossom/servers').then((r) => r ?? []);
  }

  async checkBlossomHealth() {
    return this.fetch('/blossom/health').then((r) => r ?? {});
  }

  async getBlossomStats() {
    return this.fetch('/blossom/stats').then((r) => r ?? {});
  }

  /**
   * Download a Blossom blob by SHA-256 hash, proxied through the backend API.
   * Returns the raw Response so callers can read text/json/blob as needed.
   */
  async fetchBlossomBlob(sha256Hash) {
    const url = `${BASE_URL}/blossom/blob/${encodeURIComponent(sha256Hash)}`;
    const resp = await fetch(url);
    if (!resp.ok) throw new Error(`${resp.status} ${resp.statusText}`);
    return resp;
  }
}

export const api = typeof window !== 'undefined' ? new BahiaClient() : null;
export default api;
