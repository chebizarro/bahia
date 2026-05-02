/**
 * E2E Test Scenario Library Index
 * 
 * This module exports all test scenarios with metadata for discovery and execution.
 */
import type { Scenario } from '../types.js';

// Import all scenario modules
import { serviceScenarios } from './services.js';
import { environmentScenarios } from './environments.js';
import { deploymentScenarios } from './deployments.js';
import { policyScenarios } from './policies.js';
import { workerScenarios } from './workers.js';
import { secretScenarios } from './secrets.js';
import { eventScenarios } from './events.js';

/**
 * Scenario category metadata
 */
export interface ScenarioCategory {
  name: string;
  description: string;
  scenarios: Scenario[];
}

/**
 * All scenario categories
 */
export const categories: ScenarioCategory[] = [
  {
    name: 'Services',
    description: 'Service CRUD operations and lifecycle management',
    scenarios: serviceScenarios,
  },
  {
    name: 'Environments',
    description: 'Environment CRUD with protected flag and deploy strategies',
    scenarios: environmentScenarios,
  },
  {
    name: 'Deployments',
    description: 'Deployment workflows: intents, approvals, and execution',
    scenarios: deploymentScenarios,
  },
  {
    name: 'Policies',
    description: 'Policy creation, management, and enforcement',
    scenarios: policyScenarios,
  },
  {
    name: 'Workers',
    description: 'Worker catalog queries and status checks',
    scenarios: workerScenarios,
  },
  {
    name: 'Secrets',
    description: 'Service secrets CRUD with encryption (NIP-44, AES-256-GCM)',
    scenarios: secretScenarios,
  },
  {
    name: 'Events',
    description: 'Nostr sidecar relay discovery and control-plane feature checks',
    scenarios: eventScenarios,
  },
];

/**
 * All scenarios flattened
 */
export const allScenarios: Scenario[] = categories.flatMap(c => c.scenarios);

/**
 * Get scenarios by tag
 */
export function getScenariosByTag(tag: string): Scenario[] {
  return allScenarios.filter(s => s.tags.includes(tag));
}

/**
 * Get scenarios by multiple tags (AND logic)
 */
export function getScenariosByTags(tags: string[]): Scenario[] {
  return allScenarios.filter(s => tags.every(tag => s.tags.includes(tag)));
}

/**
 * Get scenario by name
 */
export function getScenarioByName(name: string): Scenario | undefined {
  return allScenarios.find(s => s.name === name);
}

/**
 * Get smoke test scenarios (quick sanity checks)
 */
export function getSmokeTests(): Scenario[] {
  return getScenariosByTag('smoke');
}

/**
 * Get integration test scenarios (multi-step workflows)
 */
export function getIntegrationTests(): Scenario[] {
  return getScenariosByTag('integration');
}

/**
 * Get all CRUD scenarios
 */
export function getCRUDTests(): Scenario[] {
  return getScenariosByTag('crud');
}

/**
 * Scenario library statistics
 */
export function getStats() {
  const tagCounts = new Map<string, number>();
  
  for (const scenario of allScenarios) {
    for (const tag of scenario.tags) {
      tagCounts.set(tag, (tagCounts.get(tag) || 0) + 1);
    }
  }
  
  return {
    totalScenarios: allScenarios.length,
    categories: categories.length,
    tags: Array.from(tagCounts.entries()).map(([tag, count]) => ({ tag, count })),
    smokeTests: getSmokeTests().length,
    integrationTests: getIntegrationTests().length,
    crudTests: getCRUDTests().length,
  };
}

/**
 * Print scenario library summary
 */
export function printSummary(): void {
  console.log('📚 E2E Test Scenario Library\n');
  
  for (const category of categories) {
    console.log(`📁 ${category.name} (${category.scenarios.length} scenarios)`);
    console.log(`   ${category.description}`);
    
    for (const scenario of category.scenarios) {
      const tags = scenario.tags.join(', ');
      console.log(`   • ${scenario.name} [${tags}]`);
    }
    console.log();
  }
  
  const stats = getStats();
  console.log('📊 Statistics:');
  console.log(`   Total Scenarios: ${stats.totalScenarios}`);
  console.log(`   Categories: ${stats.categories}`);
  console.log(`   Smoke Tests: ${stats.smokeTests}`);
  console.log(`   Integration Tests: ${stats.integrationTests}`);
  console.log(`   CRUD Tests: ${stats.crudTests}`);
  console.log();
  
  console.log('🏷️  Tags:');
  stats.tags.sort((a, b) => b.count - a.count);
  for (const { tag, count } of stats.tags) {
    console.log(`   ${tag}: ${count}`);
  }
}

// Export individual scenario arrays for direct imports
export {
  serviceScenarios,
  environmentScenarios,
  deploymentScenarios,
  policyScenarios,
  workerScenarios,
  secretScenarios,
  eventScenarios,
};
