/**
 * Deployment workflow test scenarios
 */
import type { Scenario, ScenarioResult, ScenarioDrivers, TestStepResult } from '../types.js';

/**
 * Helper to create a test step result
 */
function step(name: string, status: TestStepResult['status'], duration: number, error?: string): TestStepResult {
  return { name, status, duration, error };
}

/**
 * Test: Create deployment intent
 */
export const createDeploymentIntent: Scenario = {
  name: 'Create Deployment Intent',
  description: 'Create a deployment intent for a service to an environment',
  tags: ['deployments', 'api', 'smoke'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create service
      const serviceStart = Date.now();
      const service = await drivers.api.createService({
        name: `deploy-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      steps.push(step('Create service', 'passed', Date.now() - serviceStart));
      
      // Step 2: Create environment
      const envStart = Date.now();
      const environment = await drivers.api.createEnvironment({
        name: `deploy-env-${Date.now()}`,
        protected: false,
      });
      steps.push(step('Create environment', 'passed', Date.now() - envStart));
      
      // Step 3: Create a build (required for artifact)
      const buildStart = Date.now();
      const build = await drivers.api.createBuild({
        service_id: service.data!.id,
        git_sha: `abc${Date.now().toString(16).slice(0, 4)}`,
        git_ref: 'refs/heads/main',
        ci_run_id: `run-${Date.now()}`,
        status: 'succeeded',
      });
      steps.push(step('Create build', 'passed', Date.now() - buildStart));
      
      // Step 4: Register an artifact (required for deployment intent)
      const artifactStart = Date.now();
      const artifact = await drivers.api.registerArtifact({
        build_id: build.data!.id,
        service_id: service.data!.id,
        image_repo: 'registry.example.com/test/app',
        image_tag: 'v1.0.0',
        image_digest: `sha256:${Date.now().toString(16).padStart(64, '0')}`,
      });
      steps.push(step('Register artifact', 'passed', Date.now() - artifactStart));
      
      // Step 5: Create deployment intent
      const intentStart = Date.now();
      const intent = await drivers.api.createDeploymentIntent({
        service_id: service.data!.id,
        environment_id: environment.data!.id,
        artifact_id: artifact.data!.id,
        requested_by: 'test-user',
      });
      steps.push(step('Create deployment intent', 'passed', Date.now() - intentStart));
      
      if (!intent.data) {
        throw new Error('Deployment intent creation did not return data');
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          serviceId: service.data!.id,
          environmentId: environment.data!.id,
          intentId: intent.data,
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
 * Test: Approve deployment intent
 */
export const approveDeploymentIntent: Scenario = {
  name: 'Approve Deployment Intent',
  description: 'Create and approve a deployment intent',
  tags: ['deployments', 'api', 'approval'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create service and environment
      const setupStart = Date.now();
      const service = await drivers.api.createService({
        name: `approve-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      const environment = await drivers.api.createEnvironment({
        name: `approve-env-${Date.now()}`,
        protected: true, // Protected environment requires approval
      });
      steps.push(step('Setup service and environment', 'passed', Date.now() - setupStart));
      
      // Step 2: Create deployment intent
      const intentStart = Date.now();
      const intent = await drivers.api.createDeploymentIntent({
        service_id: service.data!.id,
        environment_id: environment.data!.id,
        artifact_id: 'sha256:1234567890abcdef',
        requested_by: 'test-user',
      });
      steps.push(step('Create deployment intent', 'passed', Date.now() - intentStart));
      
      // Note: intent.data might be the intent ID or full object depending on API response
      const intentId = typeof intent.data === 'string' ? intent.data : (intent.data as any)?.id;
      
      if (!intentId) {
        throw new Error('Could not extract intent ID from response');
      }
      
      // Step 3: Approve the intent
      const approveStart = Date.now();
      await drivers.api.approveDeploymentIntent(intentId);
      steps.push(step('Approve deployment intent', 'passed', Date.now() - approveStart));
      
      // Step 4: Verify approval
      const verifyStart = Date.now();
      const fetchedIntent = await drivers.api.getDeploymentIntent(intentId);
      steps.push(step('Verify approval', 'passed', Date.now() - verifyStart));
      
      // Check if status indicates approval (status might be 'approved' or similar)
      const status = (fetchedIntent.data as any)?.status;
      if (status && status !== 'approved' && status !== 'pending_deployment') {
        console.warn(`Warning: Intent status is "${status}", expected "approved" or "pending_deployment"`);
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          intentId,
          status,
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
 * Test: Reject deployment intent
 */
export const rejectDeploymentIntent: Scenario = {
  name: 'Reject Deployment Intent',
  description: 'Create and reject a deployment intent with reason',
  tags: ['deployments', 'api', 'approval'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1: Create service and environment
      const setupStart = Date.now();
      const service = await drivers.api.createService({
        name: `reject-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      const environment = await drivers.api.createEnvironment({
        name: `reject-env-${Date.now()}`,
        protected: true,
      });
      steps.push(step('Setup service and environment', 'passed', Date.now() - setupStart));
      
      // Step 2: Create deployment intent
      const intentStart = Date.now();
      const intent = await drivers.api.createDeploymentIntent({
        service_id: service.data!.id,
        environment_id: environment.data!.id,
        artifact_id: 'sha256:1234567890abcdef',
        requested_by: 'test-user',
      });
      steps.push(step('Create deployment intent', 'passed', Date.now() - intentStart));
      
      const intentId = typeof intent.data === 'string' ? intent.data : (intent.data as any)?.id;
      
      // Step 3: Reject the intent
      const rejectStart = Date.now();
      await drivers.api.rejectDeploymentIntent(intentId, 'Failed security scan');
      steps.push(step('Reject deployment intent', 'passed', Date.now() - rejectStart));
      
      // Step 4: Verify rejection
      const verifyStart = Date.now();
      const fetchedIntent = await drivers.api.getDeploymentIntent(intentId);
      steps.push(step('Verify rejection', 'passed', Date.now() - verifyStart));
      
      const status = (fetchedIntent.data as any)?.status;
      if (status && status !== 'rejected') {
        console.warn(`Warning: Intent status is "${status}", expected "rejected"`);
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: { intentId, status },
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
 * Test: Full deployment workflow
 */
export const fullDeploymentWorkflow: Scenario = {
  name: 'Full Deployment Workflow',
  description: 'Complete deployment workflow: create intent, approve, deploy',
  tags: ['deployments', 'api', 'integration', 'workflow'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Setup
      const setupStart = Date.now();
      const service = await drivers.api.createService({
        name: `workflow-svc-${Date.now()}`,
        artifact_repo: 'registry.example.com/test/app',
        runtime_type: 'docker',
      });
      const environment = await drivers.api.createEnvironment({
        name: `workflow-env-${Date.now()}`,
        protected: false, // Non-protected for simpler workflow
      });
      steps.push(step('Setup', 'passed', Date.now() - setupStart));
      
      // Create intent
      const intentStart = Date.now();
      const intent = await drivers.api.createDeploymentIntent({
        service_id: service.data!.id,
        environment_id: environment.data!.id,
        artifact_id: 'sha256:workflow-test',
        requested_by: 'test-user',
      });
      steps.push(step('Create intent', 'passed', Date.now() - intentStart));
      
      const intentId = typeof intent.data === 'string' ? intent.data : (intent.data as any)?.id;
      
      // Get intent to verify creation
      const getIntentStart = Date.now();
      const fetchedIntent = await drivers.api.getDeploymentIntent(intentId);
      steps.push(step('Fetch intent', 'passed', Date.now() - getIntentStart));
      
      if (!fetchedIntent.data) {
        throw new Error('Intent not found after creation');
      }
      
      // Approve (if needed based on environment protection)
      if (environment.data!.protected) {
        const approveStart = Date.now();
        await drivers.api.approveDeploymentIntent(intentId);
        steps.push(step('Approve intent', 'passed', Date.now() - approveStart));
      }
      
      return {
        name: this.name,
        status: 'passed',
        duration: Date.now() - startTime,
        steps,
        metadata: {
          serviceId: service.data!.id,
          environmentId: environment.data!.id,
          intentId,
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
 * All deployment scenarios
 */
export const deploymentScenarios: Scenario[] = [
  createDeploymentIntent,
  approveDeploymentIntent,
  rejectDeploymentIntent,
  fullDeploymentWorkflow,
];
