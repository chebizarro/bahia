/**
 * REST API driver for Bahia
 */
import type { APIResponse, Service, Environment } from '../types.js';

/**
 * BahiaAPIDriver provides typed methods for interacting with the Bahia REST API
 */
export class BahiaAPIDriver {
  private baseUrl: string;

  constructor(baseUrl: string) {
    this.baseUrl = baseUrl.replace(/\/$/, ''); // Remove trailing slash
  }

  /**
   * Generic request method
   */
  private async request<T>(
    path: string,
    options: RequestInit = {}
  ): Promise<APIResponse<T>> {
    const url = `${this.baseUrl}${path}`;
    
    const response = await fetch(url, {
      ...options,
      headers: {
        'Content-Type': 'application/json',
        ...options.headers,
      },
    });

    if (!response.ok) {
      const errorText = await response.text();
      throw new Error(`API request failed (${response.status}): ${errorText}`);
    }

    return response.json() as Promise<APIResponse<T>>;
  }

  // ==================== Health ====================

  /**
   * Check API health
   */
  async health(): Promise<{ status: string }> {
    const response = await fetch(`${this.baseUrl}/health`);
    if (!response.ok) {
      throw new Error(`Health check failed: ${response.status}`);
    }
    return response.json() as Promise<{ status: string }>;
  }

  /**
   * Check API readiness
   */
  async ready(): Promise<{ status: string }> {
    const response = await fetch(`${this.baseUrl}/ready`);
    if (!response.ok) {
      throw new Error(`Ready check failed: ${response.status}`);
    }
    return response.json() as Promise<{ status: string }>;
  }

  // ==================== Services ====================

  /**
   * Create a new service
   */
  async createService(data: {
    name: string;
    artifact_repo: string;
    repo_url?: string;
    runtime_type?: 'docker' | 'compose' | 'kubernetes' | 'podman' | 'vm-firecracker' | 'vm-qemu';
  }): Promise<APIResponse<Service>> {
    return this.request<Service>('/api/v1/services', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  /**
   * List all services
   */
  async listServices(): Promise<APIResponse<Service[]>> {
    return this.request<Service[]>('/api/v1/services');
  }

  /**
   * Get a service by ID
   */
  async getService(id: string): Promise<APIResponse<Service>> {
    return this.request<Service>(`/api/v1/services/${id}`);
  }

  /**
   * Update a service
   */
  async updateService(
    id: string,
    data: Partial<Service>
  ): Promise<APIResponse<Service>> {
    return this.request<Service>(`/api/v1/services/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    });
  }

  /**
   * Delete a service
   */
  async deleteService(id: string): Promise<APIResponse<void>> {
    return this.request<void>(`/api/v1/services/${id}`, {
      method: 'DELETE',
    });
  }

  // ==================== Environments ====================

  /**
   * Create a new environment
   */
  async createEnvironment(data: {
    name: string;
    protected?: boolean;
    deploy_strategy?: 'replace' | 'blue_green' | 'canary';
  }): Promise<APIResponse<Environment>> {
    return this.request<Environment>('/api/v1/environments', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  /**
   * List all environments
   */
  async listEnvironments(): Promise<APIResponse<Environment[]>> {
    return this.request<Environment[]>('/api/v1/environments');
  }

  /**
   * Get an environment by ID
   */
  async getEnvironment(id: string): Promise<APIResponse<Environment>> {
    return this.request<Environment>(`/api/v1/environments/${id}`);
  }

  /**
   * Update an environment
   */
  async updateEnvironment(
    id: string,
    data: Partial<Environment>
  ): Promise<APIResponse<Environment>> {
    return this.request<Environment>(`/api/v1/environments/${id}`, {
      method: 'PUT',
      body: JSON.stringify(data),
    });
  }

  /**
   * Delete an environment
   */
  async deleteEnvironment(id: string): Promise<APIResponse<void>> {
    return this.request<void>(`/api/v1/environments/${id}`, {
      method: 'DELETE',
    });
  }

  // ==================== Builds ====================

  /**
   * Create a build
   */
  async createBuild(data: {
    service_id: string;
    git_sha: string;
    git_ref: string;
    ci_run_id: string;
    status?: string;
    metadata?: Record<string, unknown>;
  }): Promise<APIResponse<{ id: string }>> {
    return this.request<{ id: string }>('/api/v1/builds', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  /**
   * Get a build by ID
   */
  async getBuild(id: string): Promise<APIResponse<unknown>> {
    return this.request(`/api/v1/builds/${id}`);
  }

  // ==================== Artifacts ====================

  /**
   * Register an artifact
   */
  async registerArtifact(data: {
    build_id: string;
    service_id: string;
    image_repo: string;
    image_tag: string;
    image_digest: string;
    metadata?: Record<string, unknown>;
  }): Promise<APIResponse<{ id: string }>> {
    return this.request<{ id: string }>('/api/v1/artifacts', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  /**
   * Get an artifact by ID
   */
  async getArtifact(id: string): Promise<APIResponse<unknown>> {
    return this.request(`/api/v1/artifacts/${id}`);
  }

  // ==================== Deployment Intents ====================

  /**
   * Create a deployment intent
   */
  async createDeploymentIntent(data: {
    service_id: string;
    environment_id: string;
    artifact_id: string;
    requested_by: string;
  }): Promise<APIResponse<unknown>> {
    return this.request('/api/v1/deployments/intents', {
      method: 'POST',
      body: JSON.stringify(data),
    });
  }

  /**
   * Get a deployment intent
   */
  async getDeploymentIntent(id: string): Promise<APIResponse<unknown>> {
    return this.request(`/api/v1/deployments/intents/${id}`);
  }

  /**
   * Approve a deployment intent
   */
  async approveDeploymentIntent(id: string): Promise<APIResponse<unknown>> {
    return this.request(`/api/v1/deployments/intents/${id}/approve`, {
      method: 'POST',
    });
  }

  /**
   * Reject a deployment intent
   */
  async rejectDeploymentIntent(id: string, reason?: string): Promise<APIResponse<unknown>> {
    return this.request(`/api/v1/deployments/intents/${id}/reject`, {
      method: 'POST',
      body: JSON.stringify({ reason }),
    });
  }
}
