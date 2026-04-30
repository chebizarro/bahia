/**
 * Worker registration and status test scenarios
 * 
 * Note: Workers are read-only from the API perspective. They register themselves
 * via Nostr events. These scenarios test the catalog/listing functionality.
 */
import type { Scenario, ScenarioResult, ScenarioDrivers, TestStepResult } from '../types.js';

/**
 * Helper to create a test step result
 */
function step(name: string, status: TestStepResult['status'], duration: number, error?: string): TestStepResult {
  return { name, status, duration, error };
}

/**
 * Generic request helper for worker endpoints
 */
async function workerRequest<T>(
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
    throw new Error(`Worker API request failed (${response.status}): ${await response.text()}`);
  }
  
  return (await response.json()) as { data?: T; error?: string };
}

/**
 * Test: List workers
 */
export const listWorkers: Scenario = {
  name: 'List Workers',
  description: 'List all registered workers in the catalog',
  tags: ['workers', 'api', 'smoke'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // List all workers
      const listStart = Date.now();
      const apiUrl = (drivers.api as any).baseUrl;
      const result = await workerRequest<any[]>(apiUrl, '/api/v1/workers');
      steps.push(step('List workers', 'passed', Date.now() - listStart));
      
      // Workers list might be empty/null if no workers have registered
      const workers = result.data ?? [];
      if (!Array.isArray(workers)) {
        throw new Error('List workers did not return an array');
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
          metadata: { workerCount: workers.length },
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
 * Test: List workers by status
 */
export const listWorkersByStatus: Scenario = {
  name: 'List Workers by Status',
  description: 'Filter workers by status (online, offline, busy)',
  tags: ['workers', 'api', 'filtering'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      const apiUrl = (drivers.api as any).baseUrl;
      
      // Test different status filters
      const statuses = ['online', 'offline', 'busy'];
      
      for (const status of statuses) {
        const filterStart = Date.now();
        const result = await workerRequest<any[]>(apiUrl, `/api/v1/workers?status=${status}`);
        steps.push(step(`Filter by status: ${status}`, 'passed', Date.now() - filterStart));
        
        // Handle null as empty array (no workers with that status)
        const workers = result.data ?? [];
        if (!Array.isArray(workers)) {
          throw new Error(`Status filter ${status} did not return an array`);
        }
        
        // Verify all returned workers have the requested status (if any returned)
        if (workers.length > 0) {
          const invalidWorkers = workers.filter((w: any) => w.status !== status);
          if (invalidWorkers.length > 0) {
            throw new Error(`Found ${invalidWorkers.length} workers with wrong status`);
          }
        }
      }
      
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
 * Test: Get worker by pubkey
 */
export const getWorkerByPubkey: Scenario = {
  name: 'Get Worker by Pubkey',
  description: 'Fetch a specific worker by their Nostr public key',
  tags: ['workers', 'api'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      const apiUrl = (drivers.api as any).baseUrl;
      
      // Step 1: List workers to get a pubkey
      const listStart = Date.now();
      const workers = await workerRequest<any[]>(apiUrl, '/api/v1/workers');
      steps.push(step('List workers', 'passed', Date.now() - listStart));
      
      if (!Array.isArray(workers.data) || workers.data.length === 0) {
        // No workers registered, skip test
        return {
          name: this.name,
          status: 'skipped',
          duration: Date.now() - startTime,
          steps,
          error: 'No workers available to test',
        };
      }
      
      const testPubkey = workers.data[0].pubkey;
      
      // Step 2: Get specific worker
      const getStart = Date.now();
      const worker = await workerRequest(apiUrl, `/api/v1/workers/${testPubkey}`);
      steps.push(step('Get worker by pubkey', 'passed', Date.now() - getStart));
      
      if (!worker.data) {
        throw new Error('Worker not found');
      }
      
      if ((worker.data as any).pubkey !== testPubkey) {
        throw new Error('Returned worker has wrong pubkey');
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { pubkey: testPubkey },
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
 * Test: Get worker pricing
 */
export const getWorkerPricing: Scenario = {
  name: 'Get Worker Pricing',
  description: 'Fetch pricing information for a worker',
  tags: ['workers', 'api', 'pricing'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      const apiUrl = (drivers.api as any).baseUrl;
      
      // Step 1: List workers to get a pubkey
      const listStart = Date.now();
      const workers = await workerRequest<any[]>(apiUrl, '/api/v1/workers');
      steps.push(step('List workers', 'passed', Date.now() - listStart));
      
      if (!Array.isArray(workers.data) || workers.data.length === 0) {
        return {
          name: this.name,
          status: 'skipped',
          duration: Date.now() - startTime,
          steps,
          error: 'No workers available to test',
        };
      }
      
      const testPubkey = workers.data[0].pubkey;
      
      // Step 2: Get worker pricing
      const pricingStart = Date.now();
      const pricing = await workerRequest(apiUrl, `/api/v1/workers/${testPubkey}/pricing`);
      steps.push(step('Get worker pricing', 'passed', Date.now() - pricingStart));
      
      if (!pricing.data) {
        throw new Error('No pricing data returned');
      }
      
      // Pricing should have expected fields (structure may vary)
      const pricingData = pricing.data as any;
      if (typeof pricingData !== 'object') {
        throw new Error('Pricing data is not an object');
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          pubkey: testPubkey,
          pricing: pricingData,
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
 * All worker scenarios
 */
export const workerScenarios: Scenario[] = [
  listWorkers,
  listWorkersByStatus,
  getWorkerByPubkey,
  getWorkerPricing,
];
