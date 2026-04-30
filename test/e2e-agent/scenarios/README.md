# E2E Test Scenario Library

Comprehensive test scenarios covering all Bahia functionality for agent-driven end-to-end testing.

## Overview

This directory contains test scenarios organized by feature area. Each scenario is a self-contained test that:
- Uses the driver infrastructure from `../drivers/`
- Returns a `ScenarioResult` with pass/fail status and detailed steps
- Can be executed individually or as part of a test suite

## Scenario Categories

### 1. Services (`services.ts`)
Service CRUD operations and lifecycle management.

**Scenarios:**
- `createServiceAPI` - Create a service via REST API
- `listServices` - List all services
- `updateService` - Update service properties
- `deleteService` - Delete a service
- `fullServiceCRUD` - Complete CRUD lifecycle

### 2. Environments (`environments.ts`)
Environment management with protected flag and deploy strategies.

**Scenarios:**
- `createEnvironment` - Create a non-protected environment
- `createProtectedEnvironment` - Create a protected environment requiring approvals
- `listEnvironments` - List all environments
- `updateEnvironment` - Update environment properties
- `deleteEnvironment` - Delete an environment

### 3. Deployments (`deployments.ts`)
Deployment workflows: intents, approvals, and execution.

**Scenarios:**
- `createDeploymentIntent` - Create a deployment intent
- `approveDeploymentIntent` - Approve a deployment to protected environment
- `rejectDeploymentIntent` - Reject a deployment with reason
- `fullDeploymentWorkflow` - End-to-end deployment workflow

### 4. Policies (`policies.ts`)
Policy creation, management, and enforcement rules.

**Scenarios:**
- `createPolicy` - Create a deployment policy with rules
- `listPolicies` - List all policies
- `updatePolicy` - Update policy enforcement settings
- `deletePolicy` - Delete a policy
- `policyWithMultipleRules` - Create policy with multiple enforcement rules

### 5. Workers (`workers.ts`)
Worker catalog queries and status checks (read-only).

**Scenarios:**
- `listWorkers` - List all registered workers
- `listWorkersByStatus` - Filter workers by status
- `getWorkerByPubkey` - Fetch specific worker details
- `getWorkerPricing` - Get worker pricing information

### 6. Secrets (`secrets.ts`)
Service secrets CRUD with encryption (NIP-44, AES-256-GCM).

**Scenarios:**
- `createSecretNIP44` - Create secret with NIP-44 encryption
- `createSecretAES256` - Create secret with AES-256-GCM encryption
- `listServiceSecrets` - List secrets for a service (values never exposed)
- `updateSecret` - Update secret value and verify versioning
- `deleteSecret` - Delete a secret
- `environmentScopedSecrets` - Create secrets scoped to specific environments

### 7. Events (`events.ts`)
SSE event stream connection, filtering, and verification.

**Scenarios:**
- `connectSSEStream` - Establish SSE connection
- `receiveDeploymentEvents` - Verify deployment events are broadcast
- `filterEventsByType` - Test event type filtering
- `sseHeartbeat` - Verify heartbeat keeps connection alive
- `multipleConcurrentConnections` - Test multiple simultaneous SSE clients

## Usage

### Import and Run Individual Scenarios

```typescript
import { BahiaAPIDriver } from '../drivers/api.js';
import { PlaywrightDriver } from '../drivers/playwright.js';
import { MCPDriver } from '../drivers/mcp.js';
import { createServiceAPI } from './services.js';

const drivers = {
  api: new BahiaAPIDriver('http://localhost:8080'),
  web: new PlaywrightDriver('http://localhost:3000'),
  mcp: new MCPDriver(),
};

const result = await createServiceAPI.run(drivers);
console.log(result.status); // 'passed' | 'failed' | 'skipped' | 'error'
```

### Run All Scenarios in a Category

```typescript
import { serviceScenarios } from './services.js';

for (const scenario of serviceScenarios) {
  console.log(`Running: ${scenario.name}`);
  const result = await scenario.run(drivers);
  console.log(`  Status: ${result.status} (${result.duration}ms)`);
}
```

### Filter by Tags

```typescript
import { getScenariosByTag, getSmokeTests } from './index.js';

// Run all smoke tests
const smokeTests = getSmokeTests();
for (const test of smokeTests) {
  await test.run(drivers);
}

// Run all API tests
const apiTests = getScenariosByTag('api');
for (const test of apiTests) {
  await test.run(drivers);
}
```

### Print Library Summary

```typescript
import { printSummary } from './index.js';

printSummary();
```

## Scenario Tags

Scenarios are tagged for easy filtering:

- **`smoke`** - Quick sanity checks (fast, critical path)
- **`integration`** - Multi-step workflows
- **`crud`** - Create/Read/Update/Delete operations
- **`api`** - REST API tests
- **`web`** - Web UI tests (Playwright)
- **`mcp`** - MCP tool tests

Feature-specific tags:
- `services`, `environments`, `deployments`, `policies`, `workers`, `secrets`, `events`
- `encryption`, `protected`, `approval`, `filtering`, etc.

## Scenario Structure

Each scenario implements the `Scenario` interface:

```typescript
interface Scenario {
  name: string;              // Human-readable name
  description: string;       // What the scenario tests
  tags: string[];           // Tags for filtering/categorization
  run(drivers: ScenarioDrivers): Promise<ScenarioResult>;
}
```

Results include detailed step-by-step execution:

```typescript
interface ScenarioResult {
  name: string;
  status: 'passed' | 'failed' | 'skipped' | 'error';
  duration: number;         // Total duration in ms
  steps: TestStepResult[];  // Individual step results
  error?: string;           // Error message if failed
  metadata?: Record<string, unknown>; // Additional data
}
```

## Pass/Fail Criteria

Each scenario has clear pass/fail criteria:

- **PASSED**: All steps complete successfully, assertions pass
- **FAILED**: Test assertions fail, unexpected response
- **SKIPPED**: Test cannot run (e.g., no workers available for worker tests)
- **ERROR**: Unexpected error during execution

## Adding New Scenarios

1. Create scenario in appropriate category file
2. Implement `Scenario` interface
3. Add to category's exported array
4. Use the `step()` helper to track individual steps
5. Return detailed `ScenarioResult`

Example template:

```typescript
export const myNewScenario: Scenario = {
  name: 'My New Test',
  description: 'What this test verifies',
  tags: ['category', 'smoke'],
  
  async run(drivers: ScenarioDrivers): Promise<ScenarioResult> {
    const steps: TestStepResult[] = [];
    const startTime = Date.now();
    
    try {
      // Step 1
      const step1Start = Date.now();
      // ... test code ...
      steps.push(step('Step 1 name', 'passed', Date.now() - step1Start));
      
      // Step 2
      const step2Start = Date.now();
      // ... test code ...
      steps.push(step('Step 2 name', 'passed', Date.now() - step2Start));
      
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
```

## Statistics

Run `getStats()` from `index.ts` to see scenario library metrics:

```typescript
import { getStats } from './index.js';

const stats = getStats();
console.log(stats);
// {
//   totalScenarios: 30,
//   categories: 7,
//   tags: [...],
//   smokeTests: 6,
//   integrationTests: 4,
//   crudTests: 15
// }
```

## Next Steps

These scenarios will be used by:
- **Item 4: Agent Test Runner** - Autonomous execution and reporting
- **Item 5: Self-Healing Loop** - Failure analysis and fix generation

## Notes

- Scenarios are designed to be idempotent where possible
- Each scenario creates its own test data (services, environments, etc.)
- Cleanup is generally not performed (tests run in isolated docker environment)
- Some scenarios may be skipped if prerequisites aren't met (e.g., no workers registered)
