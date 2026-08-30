/**
 * Service CRUD test scenarios
 */
import type { Scenario, ScenarioResult, ScenarioDrivers, TestStepResult } from '../types.js';

/**
 * Helper to create a test step result
 */
function step(name: string, status: TestStepResult['status'], duration: number, error?: string): TestStepResult {
  return { name, status, duration, error };
}

/**
 * Test: Create a service via REST API
 */
export const createServiceAPI: Scenario = {
  name: 'Create Service (API)',
  description: 'Create a new service using the REST API',
  tags: ['crud', 'services', 'api', 'smoke'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create service
      const createStart = Date.now();
      const serviceName = `test-service-${Date.now()}`;
      const result = await drivers.api.createService({
        name: serviceName,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      if (!result.data?.id) {
        throw new Error('Service creation did not return an ID');
      }
      steps.push(step('Create service', 'passed', Date.now() - createStart));
      
      // Step 2: Verify service exists by fetching it
      const getStart = Date.now();
      const fetchedService = await drivers.api.getService(result.data.id);
      if (fetchedService.data?.name !== serviceName) {
        throw new Error(`Service name mismatch: expected ${serviceName}, got ${fetchedService.data?.name}`);
      }
      steps.push(step('Fetch created service', 'passed', Date.now() - getStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { serviceId: result.data.id },
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
 * Test: List services
 */
export const listServices: Scenario = {
  name: 'List Services',
  description: 'List all services and verify count',
  tags: ['crud', 'services', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create a test service first
      const createStart = Date.now();
      const created = await drivers.api.createService({
        name: `list-test-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/list',
        runtime_type: 'docker',
      });
      if (!created.data?.id) {
        throw new Error('Test service creation did not return an ID');
      }
      steps.push(step('Create test service', 'passed', Date.now() - createStart));
      
      // Step 2: List services
      const listStart = Date.now();
      const result = await drivers.api.listServices();
      if (!Array.isArray(result.data)) {
        throw new Error('List services did not return an array');
      }
      
      if (result.data.length === 0) {
        throw new Error('No services found after creating one');
      }
      steps.push(step('List services', 'passed', Date.now() - listStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { serviceCount: result.data.length },
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
 * Test: Update service
 */
export const updateService: Scenario = {
  name: 'Update Service',
  description: 'Update a service and verify changes',
  tags: ['crud', 'services', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create a service
      const createStart = Date.now();
      const result = await drivers.api.createService({
        name: `update-test-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/original',
        runtime_type: 'docker',
      });
      if (!result.data?.id) {
        throw new Error('Service creation did not return an ID');
      }
      steps.push(step('Create service', 'passed', Date.now() - createStart));
      
      const serviceId = result.data.id;
      
      // Step 2: Update the service
      const updateStart = Date.now();
      const newRepo = 'registry.example.com/test/updated';
      await drivers.api.updateService(serviceId, {
        artifact_repo: newRepo,
      });
      
      // Step 3: Verify update
      const verifyStart = Date.now();
      const updated = await drivers.api.getService(serviceId);
      if (updated.data?.artifact_repo !== newRepo) {
        throw new Error(`Update failed: expected ${newRepo}, got ${updated.data?.artifact_repo}`);
      }
      steps.push(step('Update service', 'passed', Date.now() - updateStart));
      steps.push(step('Verify update', 'passed', Date.now() - verifyStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { serviceId },
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
 * Test: Delete service
 */
export const deleteService: Scenario = {
  name: 'Delete Service',
  description: 'Delete a service and verify it is gone',
  tags: ['crud', 'services', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create a service
      const createStart = Date.now();
      const result = await drivers.api.createService({
        name: `delete-test-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/delete',
        runtime_type: 'docker',
      });
      if (!result.data?.id) {
        throw new Error('Service creation did not return an ID');
      }
      steps.push(step('Create service', 'passed', Date.now() - createStart));
      
      const serviceId = result.data.id;
      
      // Step 2: Delete the service
      const deleteStart = Date.now();
      await drivers.api.deleteService(serviceId);
      
      // Step 3: Verify deletion (should fail to fetch)
      const verifyStart = Date.now();
      try {
        await drivers.api.getService(serviceId);
        throw new Error('Service still exists after deletion');
      } catch (error) {
        // Expected to fail
        if (String(error).includes('404') || String(error).includes('not found')) {
          steps.push(step('Delete service', 'passed', Date.now() - deleteStart));
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
        metadata: { serviceId },
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
 * Test: Full CRUD lifecycle
 */
export const fullServiceCRUD: Scenario = {
  name: 'Full Service CRUD Lifecycle',
  description: 'Complete CRUD operations on a service in sequence',
  tags: ['crud', 'services', 'api', 'integration'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      const serviceName = `lifecycle-test-${Date.now()}`;
      
      // Create
      const createStart = Date.now();
      const created = await drivers.api.createService({
        name: serviceName,
        artifact_repo: 'registry.example.com/test/lifecycle',
        runtime_type: 'docker',
      });
      if (!created.data?.id) {
        throw new Error('Service creation did not return an ID');
      }
      steps.push(step('Create', 'passed', Date.now() - createStart));
      const serviceId = created.data.id;
      
      // Read
      const readStart = Date.now();
      const fetched = await drivers.api.getService(serviceId);
      if (fetched.data?.name !== serviceName) {
        throw new Error('Read verification failed');
      }
      steps.push(step('Read', 'passed', Date.now() - readStart));
      
      // Update
      const updateStart = Date.now();
      await drivers.api.updateService(serviceId, {
        artifact_repo: 'registry.example.com/test/updated',
      });
      const updated = await drivers.api.getService(serviceId);
      if (updated.data?.artifact_repo !== 'registry.example.com/test/updated') {
        throw new Error('Update verification failed');
      }
      steps.push(step('Update', 'passed', Date.now() - updateStart));
      
      // Delete
      const deleteStart = Date.now();
      await drivers.api.deleteService(serviceId);
      
      // Verify deletion
      try {
        await drivers.api.getService(serviceId);
        throw new Error('Service still exists after deletion');
      } catch (error) {
        if (!String(error).includes('404') && !String(error).includes('not found')) {
          throw error;
        }
      }
      steps.push(step('Delete', 'passed', Date.now() - deleteStart));
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
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
 * All service scenarios
 */
export const serviceScenarios: Scenario[] = [
  createServiceAPI,
  listServices,
  updateService,
  deleteService,
  fullServiceCRUD,
];
