/**
 * Environment CRUD test scenarios
 */
import type { Scenario, ScenarioResult, ScenarioDrivers, TestStepResult } from '../types.js';

/**
 * Helper to create a test step result
 */
function step(name: string, status: TestStepResult['status'], duration: number, error?: string): TestStepResult {
  return { name, status, duration, error };
}

/**
 * Test: Create environment
 */
export const createEnvironment: Scenario = {
  name: 'Create Environment (API)',
  description: 'Create a new environment using the REST API',
  tags: ['crud', 'environments', 'api', 'smoke'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create environment
      const createStart = Date.now();
      const envName = `test-env-${Date.now()}`;
      const result = await drivers.api.createEnvironment({
        name: envName,
        protected: false,
        deploy_strategy: 'replace',
      });
      steps.push(step('Create environment', 'passed', Date.now() - createStart));
      
      if (!result.data?.id) {
        throw new Error('Environment creation did not return an ID');
      }
      
      // Step 2: Verify environment exists
      const getStart = Date.now();
      const fetched = await drivers.api.getEnvironment(result.data.id);
      steps.push(step('Fetch created environment', 'passed', Date.now() - getStart));
      
      if (fetched.data?.name !== envName) {
        throw new Error(`Environment name mismatch: expected ${envName}, got ${fetched.data?.name}`);
      }
      
      if (fetched.data?.protected !== false) {
        throw new Error('Environment protected flag not set correctly');
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { environmentId: result.data.id },
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
 * Test: Create protected environment
 */
export const createProtectedEnvironment: Scenario = {
  name: 'Create Protected Environment',
  description: 'Create a protected environment and verify the protected flag',
  tags: ['crud', 'environments', 'api', 'protected'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create protected environment
      const createStart = Date.now();
      const envName = `prod-env-${Date.now()}`;
      const result = await drivers.api.createEnvironment({
        name: envName,
        protected: true,
        deploy_strategy: 'blue_green',
      });
      steps.push(step('Create protected environment', 'passed', Date.now() - createStart));
      
      // Step 2: Verify protected flag
      const verifyStart = Date.now();
      const fetched = await drivers.api.getEnvironment(result.data!.id);
      steps.push(step('Verify protected flag', 'passed', Date.now() - verifyStart));
      
      if (fetched.data?.protected !== true) {
        throw new Error('Protected flag not set correctly');
      }
      
      if (fetched.data?.deploy_strategy !== 'blue_green') {
        throw new Error('Deploy strategy not set correctly');
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { environmentId: result.data!.id },
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
 * Test: List environments
 */
export const listEnvironments: Scenario = {
  name: 'List Environments',
  description: 'List all environments and verify count',
  tags: ['crud', 'environments', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create test environments
      const createStart = Date.now();
      await drivers.api.createEnvironment({
        name: `list-test-1-${Date.now()}`,
        protected: false,
      });
      await drivers.api.createEnvironment({
        name: `list-test-2-${Date.now()}`,
        protected: true,
      });
      steps.push(step('Create test environments', 'passed', Date.now() - createStart));
      
      // Step 2: List environments
      const listStart = Date.now();
      const result = await drivers.api.listEnvironments();
      steps.push(step('List environments', 'passed', Date.now() - listStart));
      
      if (!Array.isArray(result.data)) {
        throw new Error('List environments did not return an array');
      }
      
      if (result.data.length < 2) {
        throw new Error('Expected at least 2 environments');
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { environmentCount: result.data.length },
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
 * Test: Update environment
 */
export const updateEnvironment: Scenario = {
  name: 'Update Environment',
  description: 'Update an environment and verify changes',
  tags: ['crud', 'environments', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create environment
      const createStart = Date.now();
      const result = await drivers.api.createEnvironment({
        name: `update-test-${Date.now()}`,
        protected: false,
        deploy_strategy: 'replace',
      });
      steps.push(step('Create environment', 'passed', Date.now() - createStart));
      
      const envId = result.data!.id;
      
      // Step 2: Update to protected
      const updateStart = Date.now();
      await drivers.api.updateEnvironment(envId, {
        protected: true,
        deploy_strategy: 'canary',
      });
      steps.push(step('Update environment', 'passed', Date.now() - updateStart));
      
      // Step 3: Verify update
      const verifyStart = Date.now();
      const updated = await drivers.api.getEnvironment(envId);
      steps.push(step('Verify update', 'passed', Date.now() - verifyStart));
      
      if (updated.data?.protected !== true) {
        throw new Error('Protected flag not updated');
      }
      
      if (updated.data?.deploy_strategy !== 'canary') {
        throw new Error('Deploy strategy not updated');
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { environmentId: envId },
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
 * Test: Delete environment
 */
export const deleteEnvironment: Scenario = {
  name: 'Delete Environment',
  description: 'Delete an environment and verify it is gone',
  tags: ['crud', 'environments', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create environment
      const createStart = Date.now();
      const result = await drivers.api.createEnvironment({
        name: `delete-test-${Date.now()}`,
        protected: false,
      });
      steps.push(step('Create environment', 'passed', Date.now() - createStart));
      
      const envId = result.data!.id;
      
      // Step 2: Delete environment
      const deleteStart = Date.now();
      await drivers.api.deleteEnvironment(envId);
      steps.push(step('Delete environment', 'passed', Date.now() - deleteStart));
      
      // Step 3: Verify deletion
      const verifyStart = Date.now();
      try {
        await drivers.api.getEnvironment(envId);
        throw new Error('Environment still exists after deletion');
      } catch (error) {
        if (String(error).includes('404') || String(error).includes('not found')) {
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
        metadata: { environmentId: envId },
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
 * All environment scenarios
 */
export const environmentScenarios: Scenario[] = [
  createEnvironment,
  createProtectedEnvironment,
  listEnvironments,
  updateEnvironment,
  deleteEnvironment,
];
