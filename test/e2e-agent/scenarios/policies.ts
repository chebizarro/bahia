/**
 * Policy creation and enforcement test scenarios
 * 
 * Note: Full policy enforcement testing requires artifact and SBOM data.
 * These scenarios focus on policy CRUD operations.
 */
import type { Scenario, ScenarioResult, ScenarioDrivers, TestStepResult } from '../types.js';

/**
 * Helper to create a test step result
 */
function step(name: string, status: TestStepResult['status'], duration: number, error?: string): TestStepResult {
  return { name, status, duration, error };
}

/**
 * Generic request helper for policy endpoints
 */
async function policyRequest<T>(
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
    throw new Error(`Policy API request failed (${response.status}): ${await response.text()}`);
  }
  
  return (await response.json()) as { data?: T; error?: string };
}

/**
 * Test: Create policy
 */
export const createPolicy: Scenario = {
  name: 'Create Policy',
  description: 'Create a deployment policy with rules',
  tags: ['policies', 'api', 'smoke'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create environment for policy
      const envStart = Date.now();
      const environment = await drivers.api.createEnvironment({
        name: `policy-env-${Date.now()}`,
        protected: true,
      });
      steps.push(step('Create environment', 'passed', Date.now() - envStart));
      
      // Step 2: Create policy
      const policyStart = Date.now();
      const apiUrl = (drivers.api as any).baseUrl;
      const policy = await policyRequest(apiUrl, '/api/v1/policies', {
        method: 'POST',
        body: JSON.stringify({
          name: `test-policy-${Date.now()}`,
          environment_id: environment.data!.id,
          rules: [
            {
              type: 'require_signature',
              params: { required: true },
            },
          ],
          enforcement: 'warn',
          enabled: true,
        }),
      });
      if (!policy.data) {
        throw new Error('Policy creation did not return data');
      }
      steps.push(step('Create policy', 'passed', Date.now() - policyStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          policyId: (policy.data as any).id,
          environmentId: environment.data!.id,
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
 * Test: List policies
 */
export const listPolicies: Scenario = {
  name: 'List Policies',
  description: 'List all policies',
  tags: ['policies', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create a test policy
      const createStart = Date.now();
      const apiUrl = (drivers.api as any).baseUrl;
      await policyRequest(apiUrl, '/api/v1/policies', {
        method: 'POST',
        body: JSON.stringify({
          name: `list-test-policy-${Date.now()}`,
          rules: [{ type: 'require_signature', params: {} }],
          enforcement: 'warn',
          enabled: true,
        }),
      });
      steps.push(step('Create test policy', 'passed', Date.now() - createStart));
      
      // Step 2: List policies
      const listStart = Date.now();
      const result = await policyRequest<any[]>(apiUrl, '/api/v1/policies');
      if (!Array.isArray(result.data)) {
        throw new Error('List policies did not return an array');
      }
      
      if (result.data.length === 0) {
        throw new Error('No policies found after creating one');
      }
      steps.push(step('List policies', 'passed', Date.now() - listStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { policyCount: result.data.length },
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
 * Test: Update policy
 */
export const updatePolicy: Scenario = {
  name: 'Update Policy',
  description: 'Update a policy and verify changes',
  tags: ['policies', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create policy
      const createStart = Date.now();
      const apiUrl = (drivers.api as any).baseUrl;
      const created = await policyRequest(apiUrl, '/api/v1/policies', {
        method: 'POST',
        body: JSON.stringify({
          name: `update-test-${Date.now()}`,
          rules: [{ type: 'require_signature', params: {} }],
          enforcement: 'warn',
          enabled: true,
        }),
      });
      const policyId = (created.data as any)?.id;
      if (!policyId) {
        throw new Error('Policy creation did not return an ID');
      }
      steps.push(step('Create policy', 'passed', Date.now() - createStart));
      
      // Step 2: Update policy
      const updateStart = Date.now();
      await policyRequest(apiUrl, `/api/v1/policies/${policyId}`, {
        method: 'PUT',
        body: JSON.stringify({
          enforcement: 'block',
          enabled: false,
        }),
      });
      
      // Step 3: Verify update
      const verifyStart = Date.now();
      const updated = await policyRequest(apiUrl, `/api/v1/policies/${policyId}`);
      
      if ((updated.data as any).enforcement !== 'block') {
        throw new Error('Enforcement not updated');
      }
      
      if ((updated.data as any).enabled !== false) {
        throw new Error('Enabled flag not updated');
      }
      steps.push(step('Update policy', 'passed', Date.now() - updateStart));
      steps.push(step('Verify update', 'passed', Date.now() - verifyStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { policyId },
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
 * Test: Delete policy
 */
export const deletePolicy: Scenario = {
  name: 'Delete Policy',
  description: 'Delete a policy and verify removal',
  tags: ['policies', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create policy
      const createStart = Date.now();
      const apiUrl = (drivers.api as any).baseUrl;
      const created = await policyRequest(apiUrl, '/api/v1/policies', {
        method: 'POST',
        body: JSON.stringify({
          name: `delete-test-${Date.now()}`,
          rules: [{ type: 'require_signature', params: {} }],
          enforcement: 'warn',
          enabled: true,
        }),
      });
      const policyId = (created.data as any)?.id;
      if (!policyId) {
        throw new Error('Policy creation did not return an ID');
      }
      steps.push(step('Create policy', 'passed', Date.now() - createStart));
      
      // Step 2: Delete policy
      const deleteStart = Date.now();
      await policyRequest(apiUrl, `/api/v1/policies/${policyId}`, {
        method: 'DELETE',
      });
      
      // Step 3: Verify deletion
      const verifyStart = Date.now();
      try {
        await policyRequest(apiUrl, `/api/v1/policies/${policyId}`);
        throw new Error('Policy still exists after deletion');
      } catch (error) {
        if (String(error).includes('404') || String(error).includes('not found')) {
          steps.push(step('Delete policy', 'passed', Date.now() - deleteStart));
          steps.push(step('Verify deletion', 'passed', Date.now() - verifyStart));
        } else {
          throw error;
        }
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { policyId },
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
 * Test: Create policy with multiple rules
 */
export const policyWithMultipleRules: Scenario = {
  name: 'Policy with Multiple Rules',
  description: 'Create a policy with multiple enforcement rules',
  tags: ['policies', 'api', 'rules'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create policy with multiple rules
      const createStart = Date.now();
      const apiUrl = (drivers.api as any).baseUrl;
      const policy = await policyRequest(apiUrl, '/api/v1/policies', {
        method: 'POST',
        body: JSON.stringify({
          name: `multi-rule-policy-${Date.now()}`,
          rules: [
            {
              type: 'require_signature',
              params: { required: true },
            },
            {
              type: 'sbom_required',
              params: { format: 'cyclonedx' },
            },
            {
              type: 'no_critical_vulnerabilities',
              params: { max_critical: 0 },
            },
          ],
          enforcement: 'block',
          enabled: true,
        }),
      });
      const policyId = (policy.data as any)?.id;
      if (!policyId) {
        throw new Error('Policy creation did not return an ID');
      }
      steps.push(step('Create multi-rule policy', 'passed', Date.now() - createStart));
      
      // Step 2: Verify rules
      const verifyStart = Date.now();
      const fetched = await policyRequest(apiUrl, `/api/v1/policies/${policyId}`);
      const rules = (fetched.data as any).rules;
      if (!Array.isArray(rules) || rules.length !== 3) {
        throw new Error(`Expected 3 rules, got ${rules?.length || 0}`);
      }
      steps.push(step('Verify policy rules', 'passed', Date.now() - verifyStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          policyId,
          ruleCount: rules.length,
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
 * All policy scenarios
 */
export const policyScenarios: Scenario[] = [
  createPolicy,
  listPolicies,
  updatePolicy,
  deletePolicy,
  policyWithMultipleRules,
];
