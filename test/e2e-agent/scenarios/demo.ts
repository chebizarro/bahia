/**
 * Demo script showing how to use the scenario library
 * 
 * Usage:
 *   npm run demo:scenarios
 */
import { BahiaAPIDriver } from '../drivers/api.js';
import { PlaywrightDriver } from '../drivers/playwright.js';
import { MCPDriver } from '../drivers/mcp.js';
import { printSummary, getSmokeTests, getStats, allScenarios } from './index.js';
import type { ScenarioDrivers, ScenarioResult } from '../types.js';

const API_URL = process.env.BAHIA_API_URL || 'http://localhost:8080';
const WEB_URL = process.env.BAHIA_WEB_URL || 'http://localhost:3000';

/**
 * Format result for console output
 */
function formatResult(result: ScenarioResult): string {
  const statusIcon = {
    passed: '✅',
    failed: '❌',
    skipped: '⏭️ ',
    error: '💥',
  }[result.status];
  
  return `${statusIcon} ${result.name} (${result.duration}ms)`;
}

/**
 * Print detailed result
 */
function printDetailedResult(result: ScenarioResult): void {
  console.log(`\n${formatResult(result)}`);
  
  if (result.steps.length > 0) {
    console.log('  Steps:');
    for (const step of result.steps) {
      const stepIcon = step.status === 'passed' ? '  ✓' : '  ✗';
      console.log(`    ${stepIcon} ${step.name} (${step.duration}ms)`);
      if (step.error) {
        console.log(`       Error: ${step.error}`);
      }
    }
  }
  
  if (result.error) {
    console.log(`  Error: ${result.error}`);
  }
  
  if (result.metadata) {
    console.log(`  Metadata:`, result.metadata);
  }
}

/**
 * Run scenarios demo
 */
async function main() {
  console.log('🧪 E2E Test Scenario Library Demo\n');
  
  // Print library summary
  console.log('='.repeat(60));
  printSummary();
  console.log('='.repeat(60));
  
  // Initialize drivers
  console.log('\n📦 Initializing test drivers...');
  const drivers: ScenarioDrivers = {
    api: new BahiaAPIDriver(API_URL),
    web: new PlaywrightDriver(WEB_URL),
    mcp: new MCPDriver(),
  };
  console.log(`  API: ${API_URL}`);
  console.log(`  Web: ${WEB_URL}`);
  console.log('  MCP: (not connected for demo)');
  
  // Health check
  console.log('\n🏥 API Health Check...');
  try {
    const health = await drivers.api.health();
    console.log(`  ✅ API is healthy:`, health);
  } catch (error) {
    console.error(`  ❌ API health check failed:`, error);
    console.log('\n⚠️  Make sure the Bahia stack is running: docker compose up');
    process.exit(1);
  }
  
  // Demo 1: Run smoke tests
  console.log('\n🔥 Running Smoke Tests...');
  console.log('='.repeat(60));
  
  const smokeTests = getSmokeTests();
  console.log(`Found ${smokeTests.length} smoke tests\n`);
  
  const results: ScenarioResult[] = [];
  
  for (const scenario of smokeTests) {
    console.log(`▶️  Running: ${scenario.name}`);
    try {
      const result = await scenario.run(drivers);
      results.push(result);
      console.log(`   ${formatResult(result)}`);
    } catch (error) {
      console.error(`   ❌ Unexpected error:`, error);
      results.push({
        name: scenario.name,
        status: 'error',
        duration: 0,
        steps: [],
        error: String(error),
      });
    }
  }
  
  // Summary
  console.log('\n' + '='.repeat(60));
  console.log('📊 Test Results Summary\n');
  
  const passed = results.filter(r => r.status === 'passed').length;
  const failed = results.filter(r => r.status === 'failed').length;
  const skipped = results.filter(r => r.status === 'skipped').length;
  const errors = results.filter(r => r.status === 'error').length;
  
  console.log(`  ✅ Passed:  ${passed}`);
  console.log(`  ❌ Failed:  ${failed}`);
  console.log(`  ⏭️  Skipped: ${skipped}`);
  console.log(`  💥 Errors:  ${errors}`);
  console.log(`  📝 Total:   ${results.length}`);
  
  const totalDuration = results.reduce((sum, r) => sum + r.duration, 0);
  console.log(`  ⏱️  Duration: ${totalDuration}ms`);
  
  // Show failures in detail
  const failedResults = results.filter(r => r.status === 'failed' || r.status === 'error');
  if (failedResults.length > 0) {
    console.log('\n❌ Failed Tests:');
    for (const result of failedResults) {
      printDetailedResult(result);
    }
  }
  
  // Exit with error code if any tests failed
  const exitCode = failed + errors > 0 ? 1 : 0;
  console.log('\n' + '='.repeat(60));
  console.log(exitCode === 0 ? '✅ All tests passed!' : '❌ Some tests failed');
  
  process.exit(exitCode);
}

// Handle errors
main().catch(error => {
  console.error('💥 Demo script failed:', error);
  process.exit(1);
});
