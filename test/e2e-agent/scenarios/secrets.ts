/**
 * Service secrets CRUD test scenarios with encryption
 */
import type { Scenario, ScenarioResult, ScenarioDrivers, TestStepResult } from '../types.js';

/**
 * Helper to create a test step result
 */
function step(name: string, status: TestStepResult['status'], duration: number, error?: string): TestStepResult {
  return { name, status, duration, error };
}

/**
 * Generic request helper for secret endpoints
 */
async function secretRequest<T>(
  apiUrl: string,
  path: string,
  options: RequestInit = {}
): Promise<{ data?: T; error?: string }> {
  const response = await fetch(`${apiUrl}${path}`, {
    ...options,
    headers: {
      'Content-Type': 'application/json',
      ...options.headers,
    },
  });
  
  if (!response.ok) {
    throw new Error(`Secret API request failed (${response.status}): ${await response.text()}`);
  }
  
  return (await response.json()) as { data?: T; error?: string };
}

/**
 * Check if secrets endpoint is available
 */
async function checkSecretsAvailable(apiUrl: string): Promise<boolean> {
  try {
    const response = await fetch(`${apiUrl}/api/v1/services/test/secrets`);
    // 404 means secrets not configured, anything else means it exists
    return response.status !== 404;
  } catch {
    return false;
  }
}

/**
 * Create a skipped result for unavailable secrets
 */
function secretsNotAvailableResult(name: string, startTime: number): ScenarioResult {
  return {
    name,
    status: 'skipped',
    duration: Date.now() - startTime,
    steps: [step('Check secrets availability', 'skipped', 0, 'Secrets endpoint not configured')],
    error: 'Secrets endpoint not configured in this deployment',
  };
}

/**
 * Test: Create secret with NIP-44 encryption
 */
export const createSecretNIP44: Scenario = {
  name: 'Create Secret (NIP-44)',
  description: 'Create a service secret with NIP-44 encryption',
  tags: ['secrets', 'api', 'encryption', 'smoke'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    const apiUrl = (drivers.api as any).baseUrl;
    
    try {
      // Step 0: Check if secrets endpoint is available
      const checkStart = Date.now();
      const checkResponse = await fetch(`${apiUrl}/api/v1/services/test/secrets`);
      if (checkResponse.status === 404) {
        // Secrets endpoint not configured, skip test
        return {
          name: this.name,
          status: 'skipped',
          duration: Date.now() - startTime,
          steps: [step('Check secrets availability', 'skipped', Date.now() - checkStart, 'Secrets endpoint not configured')],
          error: 'Secrets endpoint not configured in this deployment',
        };
      }
      steps.push(step('Check secrets availability', 'passed', Date.now() - checkStart));

      // Step 1: Create service
      const serviceStart = Date.now();
      const service = await drivers.api.createService({
        name: `secret-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      if (!service.data?.id) {
        throw new Error('Service creation did not return an ID');
      }
      steps.push(step('Create service', 'passed', Date.now() - serviceStart));
      
      const serviceId = service.data.id;
      
      // Step 2: Create secret with NIP-44 encryption
      const secretStart = Date.now();
      const secret = await secretRequest(apiUrl, `/api/v1/services/${serviceId}/secrets`, {
        method: 'POST',
        body: JSON.stringify({
          name: 'DATABASE_URL',
          value: 'postgres://user:pass@localhost:5432/db',
          encryption_method: 'nip44',
        }),
      });
      if (!secret.data) {
        throw new Error('Secret creation did not return data');
      }
      
      const secretData = secret.data as any;
      
      // Verify encryption method
      if (secretData.encryption_method !== 'nip44') {
        throw new Error(`Expected encryption_method 'nip44', got '${secretData.encryption_method}'`);
      }
      
      // Verify value is not exposed in response
      if (secretData.value || secretData.encrypted_value) {
        throw new Error('Secret value exposed in response');
      }
      steps.push(step('Create NIP-44 encrypted secret', 'passed', Date.now() - secretStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          serviceId,
          secretId: secretData.id,
          encryptionMethod: 'nip44',
        },
      };
    } catch (error) {
      steps.push(step('Error occurred', 'error', Date.now() - startTime, String(error)));
      return {
        name: this.name,
        status: 'failed',
        duration: Date.now() - startTime,
        steps,
        error: String(error),
      };
    }
  },
};

/**
 * Test: Create secret with AES-256-GCM encryption
 */
export const createSecretAES256: Scenario = {
  name: 'Create Secret (AES-256-GCM)',
  description: 'Create a service secret with AES-256-GCM encryption',
  tags: ['secrets', 'api', 'encryption'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    const apiUrl = (drivers.api as any).baseUrl;
    
    // Check if secrets are available
    if (!(await checkSecretsAvailable(apiUrl))) {
      return secretsNotAvailableResult(this.name, startTime);
    }
    
    try {
      // Step 1: Create service
      const serviceStart = Date.now();
      const service = await drivers.api.createService({
        name: `aes-secret-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      if (!service.data?.id) {
        throw new Error('Service creation did not return an ID');
      }
      steps.push(step('Create service', 'passed', Date.now() - serviceStart));
      
      // Step 2: Create secret with AES-256-GCM
      const secretStart = Date.now();
      const apiUrl = (drivers.api as any).baseUrl;
      const secret = await secretRequest(apiUrl, `/api/v1/services/${service.data!.id}/secrets`, {
        method: 'POST',
        body: JSON.stringify({
          name: 'API_KEY',
          value: 'sk-1234567890abcdef',
          encryption_method: 'aes256gcm',
        }),
      });
      const secretData = secret.data as any;
      
      if (secretData?.encryption_method !== 'aes256gcm') {
        throw new Error(`Expected encryption_method 'aes256gcm', got '${secretData?.encryption_method}'`);
      }
      steps.push(step('Create AES-256 encrypted secret', 'passed', Date.now() - secretStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          serviceId: service.data!.id,
          secretId: secretData.id,
          encryptionMethod: 'aes256gcm',
        },
      };
    } catch (error) {
      steps.push(step('Error occurred', 'error', Date.now() - startTime, String(error)));
      return {
        name: this.name,
        status: 'failed',
        duration: Date.now() - startTime,
        steps,
        error: String(error),
      };
    }
  },
};

/**
 * Test: List service secrets
 */
export const listServiceSecrets: Scenario = {
  name: 'List Service Secrets',
  description: 'List all secrets for a service',
  tags: ['secrets', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    const apiUrl = (drivers.api as any).baseUrl;
    
    if (!(await checkSecretsAvailable(apiUrl))) {
      return secretsNotAvailableResult(this.name, startTime);
    }
    
    try {
      // Step 1: Create service
      const serviceStart = Date.now();
      const service = await drivers.api.createService({
        name: `list-secrets-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      if (!service.data?.id) {
        throw new Error('Service creation did not return an ID');
      }
      steps.push(step('Create service', 'passed', Date.now() - serviceStart));
      
      const serviceId = service.data.id;
      const apiUrl = (drivers.api as any).baseUrl;
      
      // Step 2: Create multiple secrets
      const createStart = Date.now();
      await secretRequest(apiUrl, `/api/v1/services/${serviceId}/secrets`, {
        method: 'POST',
        body: JSON.stringify({
          name: 'SECRET_1',
          value: 'value1',
        }),
      });
      await secretRequest(apiUrl, `/api/v1/services/${serviceId}/secrets`, {
        method: 'POST',
        body: JSON.stringify({
          name: 'SECRET_2',
          value: 'value2',
        }),
      });
      steps.push(step('Create test secrets', 'passed', Date.now() - createStart));
      
      // Step 3: List secrets
      const listStart = Date.now();
      const result = await secretRequest<any[]>(apiUrl, `/api/v1/services/${serviceId}/secrets`);
      if (!Array.isArray(result.data)) {
        throw new Error('List secrets did not return an array');
      }
      
      if (result.data.length < 2) {
        throw new Error(`Expected at least 2 secrets, got ${result.data.length}`);
      }
      
      // Verify no values are exposed
      const exposedSecrets = result.data.filter((s: any) => s.value || s.encrypted_value);
      if (exposedSecrets.length > 0) {
        throw new Error('Secret values exposed in list response');
      }
      steps.push(step('List secrets', 'passed', Date.now() - listStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          serviceId,
          secretCount: result.data.length,
        },
      };
    } catch (error) {
      steps.push(step('Error occurred', 'error', Date.now() - startTime, String(error)));
      return {
        name: this.name,
        status: 'failed',
        duration: Date.now() - startTime,
        steps,
        error: String(error),
      };
    }
  },
};

/**
 * Test: Update secret
 */
export const updateSecret: Scenario = {
  name: 'Update Secret',
  description: 'Update a secret value and verify version increment',
  tags: ['secrets', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    const apiUrl = (drivers.api as any).baseUrl;
    
    if (!(await checkSecretsAvailable(apiUrl))) {
      return secretsNotAvailableResult(this.name, startTime);
    }
    
    try {
      // Step 1: Create service and secret
      const setupStart = Date.now();
      const service = await drivers.api.createService({
        name: `update-secret-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      if (!service.data?.id) {
        throw new Error('Service creation did not return an ID');
      }
      
      const apiUrl = (drivers.api as any).baseUrl;
      const secret = await secretRequest(apiUrl, `/api/v1/services/${service.data!.id}/secrets`, {
        method: 'POST',
        body: JSON.stringify({
          name: 'UPDATE_TEST',
          value: 'original-value',
        }),
      });
      const secretId = (secret.data as any)?.id;
      const originalVersion = (secret.data as any)?.version;
      if (!secretId || typeof originalVersion !== 'number') {
        throw new Error('Secret creation did not return an ID and version');
      }
      steps.push(step('Setup service and secret', 'passed', Date.now() - setupStart));
      
      // Step 2: Update secret
      const updateStart = Date.now();
      const updated = await secretRequest(
        apiUrl,
        `/api/v1/services/${service.data!.id}/secrets/${secretId}`,
        {
          method: 'PUT',
          body: JSON.stringify({
            value: 'updated-value',
          }),
        }
      );
      // Step 3: Verify version incremented
      const newVersion = (updated.data as any).version;
      if (newVersion <= originalVersion) {
        throw new Error(`Version not incremented: ${originalVersion} -> ${newVersion}`);
      }
      steps.push(step('Update secret', 'passed', Date.now() - updateStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          secretId,
          versionBefore: originalVersion,
          versionAfter: newVersion,
        },
      };
    } catch (error) {
      steps.push(step('Error occurred', 'error', Date.now() - startTime, String(error)));
      return {
        name: this.name,
        status: 'failed',
        duration: Date.now() - startTime,
        steps,
        error: String(error),
      };
    }
  },
};

/**
 * Test: Delete secret
 */
export const deleteSecret: Scenario = {
  name: 'Delete Secret',
  description: 'Delete a secret and verify removal',
  tags: ['secrets', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    const apiUrl = (drivers.api as any).baseUrl;
    
    if (!(await checkSecretsAvailable(apiUrl))) {
      return secretsNotAvailableResult(this.name, startTime);
    }
    
    try {
      // Step 1: Create service and secret
      const setupStart = Date.now();
      const service = await drivers.api.createService({
        name: `delete-secret-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      if (!service.data?.id) {
        throw new Error('Service creation did not return an ID');
      }
      
      const apiUrl = (drivers.api as any).baseUrl;
      const secret = await secretRequest(apiUrl, `/api/v1/services/${service.data!.id}/secrets`, {
        method: 'POST',
        body: JSON.stringify({
          name: 'DELETE_TEST',
          value: 'to-be-deleted',
        }),
      });
      const secretId = (secret.data as any)?.id;
      if (!secretId) {
        throw new Error('Secret creation did not return an ID');
      }
      steps.push(step('Setup service and secret', 'passed', Date.now() - setupStart));
      
      // Step 2: Delete secret
      const deleteStart = Date.now();
      await secretRequest(
        apiUrl,
        `/api/v1/services/${service.data!.id}/secrets/${secretId}`,
        {
          method: 'DELETE',
        }
      );
      // Step 3: Verify deletion - list should not include deleted secret
      const verifyStart = Date.now();
      const remaining = await secretRequest<any[]>(apiUrl, `/api/v1/services/${service.data!.id}/secrets`);
      
      if (Array.isArray(remaining.data)) {
        const stillExists = remaining.data.find((s: any) => s.id === secretId);
        if (stillExists) {
          throw new Error('Secret still exists after deletion');
        }
      }
      steps.push(step('Delete secret', 'passed', Date.now() - deleteStart));
      steps.push(step('Verify deletion', 'passed', Date.now() - verifyStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { secretId },
      };
    } catch (error) {
      steps.push(step('Error occurred', 'error', Date.now() - startTime, String(error)));
      return {
        name: this.name,
        status: 'failed',
        duration: Date.now() - startTime,
        steps,
        error: String(error),
      };
    }
  },
};

/**
 * Test: Environment-scoped secrets
 */
export const environmentScopedSecrets: Scenario = {
  name: 'Environment-Scoped Secrets',
  description: 'Create secrets scoped to specific environments',
  tags: ['secrets', 'api', 'environments'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    const apiUrl = (drivers.api as any).baseUrl;
    
    if (!(await checkSecretsAvailable(apiUrl))) {
      return secretsNotAvailableResult(this.name, startTime);
    }
    
    try {
      // Step 1: Create service and environments
      const setupStart = Date.now();
      const service = await drivers.api.createService({
        name: `env-secret-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      
      const devEnv = await drivers.api.createEnvironment({
        name: `dev-${Date.now()}`,
        protected: false,
      });
      
      const prodEnv = await drivers.api.createEnvironment({
        name: `prod-${Date.now()}`,
        protected: true,
      });
      if (!service.data?.id || !devEnv.data?.id || !prodEnv.data?.id) {
        throw new Error('Service and environment setup did not return all IDs');
      }
      steps.push(step('Setup service and environments', 'passed', Date.now() - setupStart));
      
      const apiUrl = (drivers.api as any).baseUrl;
      
      // Step 2: Create environment-specific secrets
      const createStart = Date.now();
      const devSecret = await secretRequest(apiUrl, `/api/v1/services/${service.data!.id}/secrets`, {
        method: 'POST',
        body: JSON.stringify({
          name: 'DB_URL',
          value: 'postgres://dev-db:5432/app',
          environment_id: devEnv.data!.id,
        }),
      });
      
      const prodSecret = await secretRequest(apiUrl, `/api/v1/services/${service.data!.id}/secrets`, {
        method: 'POST',
        body: JSON.stringify({
          name: 'DB_URL',
          value: 'postgres://prod-db:5432/app',
          environment_id: prodEnv.data!.id,
        }),
      });
      // Step 3: Verify environment scoping
      if ((devSecret.data as any).environment_id !== devEnv.data!.id) {
        throw new Error('Dev secret not scoped to dev environment');
      }
      
      if ((prodSecret.data as any).environment_id !== prodEnv.data!.id) {
        throw new Error('Prod secret not scoped to prod environment');
      }
      steps.push(step('Create env-scoped secrets', 'passed', Date.now() - createStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          serviceId: service.data!.id,
          devEnvId: devEnv.data!.id,
          prodEnvId: prodEnv.data!.id,
        },
      };
    } catch (error) {
      steps.push(step('Error occurred', 'error', Date.now() - startTime, String(error)));
      return {
        name: this.name,
        status: 'failed',
        duration: Date.now() - startTime,
        steps,
        error: String(error),
      };
    }
  },
};

/**
 * All secret scenarios
 */
export const secretScenarios: Scenario[] = [
  createSecretNIP44,
  createSecretAES256,
  listServiceSecrets,
  updateSecret,
  deleteSecret,
  environmentScopedSecrets,
];
