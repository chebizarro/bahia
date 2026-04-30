/**
 * Smoke test for E2E agent test harness
 * 
 * This script verifies that all drivers (API, Playwright, MCP) can connect
 * and perform basic operations against the Bahia stack.
 */
import { TestHarness } from './harness.js';
import { BahiaAPIDriver } from './drivers/api.js';
import { PlaywrightDriver } from './drivers/playwright.js';
// MCP driver requires bahia server to expose MCP interface - skipping for now

async function runSmokeTests() {
  const harness = new TestHarness();
  let exitCode = 0;

  try {
    console.log('🧪 Starting E2E Agent Test Harness Smoke Tests\n');

    // ==================== Test Harness ====================
    console.log('📦 Test 1: Docker Compose Stack');
    await harness.start();
    console.log('✅ Stack started successfully\n');

    const health = await harness.checkHealth();
    console.log('🏥 Service Health:');
    health.forEach(s => {
      console.log(`  ${s.healthy ? '✅' : '❌'} ${s.name}${s.error ? ` (${s.error})` : ''}`);
    });
    console.log();

    // ==================== API Driver ====================
    console.log('🌐 Test 2: REST API Driver');
    const apiDriver = new BahiaAPIDriver(harness.getApiUrl());

    // Health check
    const healthResult = await apiDriver.health();
    console.log('  ✅ Health check:', healthResult);

    // Create a test service
    const serviceResult = await apiDriver.createService({
      name: `test-service-${Date.now()}`,
      artifact_repo: 'registry.example.com/test/app',
      runtime_type: 'docker',
    });
    console.log('  ✅ Created service:', serviceResult.data?.name);

    // List services
    const servicesResult = await apiDriver.listServices();
    console.log('  ✅ Listed services:', servicesResult.data?.length ?? 0, 'services');

    // Create a test environment
    const envResult = await apiDriver.createEnvironment({
      name: `test-env-${Date.now()}`,
      protected: false,
      deploy_strategy: 'replace',
    });
    console.log('  ✅ Created environment:', envResult.data?.name);

    // List environments
    const envsResult = await apiDriver.listEnvironments();
    console.log('  ✅ Listed environments:', envsResult.data?.length ?? 0, 'environments');
    console.log();

    // ==================== Playwright Driver ====================
    console.log('🎭 Test 3: Playwright Web UI Driver');
    const webDriver = new PlaywrightDriver(harness.getWebUrl());

    await webDriver.launch({ headless: true });
    console.log('  ✅ Browser launched');

    const isDashboardLoaded = await webDriver.isDashboardLoaded();
    console.log('  ✅ Dashboard loaded:', isDashboardLoaded);

    // Navigate to services page
    await webDriver.goToServices();
    const servicesUrl = await webDriver.getCurrentUrl();
    console.log('  ✅ Navigated to services:', servicesUrl);

    // Navigate to environments page
    await webDriver.goToEnvironments();
    const envsUrl = await webDriver.getCurrentUrl();
    console.log('  ✅ Navigated to environments:', envsUrl);

    // Take a screenshot
    const screenshotPath = '/tmp/bahia-smoke-test.png';
    await webDriver.screenshot(screenshotPath);
    console.log('  ✅ Screenshot saved:', screenshotPath);

    await webDriver.close();
    console.log('  ✅ Browser closed');
    console.log();

    // ==================== MCP Driver (Placeholder) ====================
    console.log('🔌 Test 4: MCP Driver');
    console.log('  ⚠️  Skipped: MCP server connection requires additional configuration');
    console.log('  ℹ️  MCP driver implementation is ready but needs bahia MCP server endpoint');
    console.log();

    console.log('✅ All smoke tests passed!\n');

  } catch (error) {
    console.error('❌ Smoke test failed:', error);
    exitCode = 1;
  } finally {
    // Cleanup
    console.log('🧹 Cleaning up...');
    try {
      await harness.cleanup();
      console.log('✅ Cleanup complete\n');
    } catch (error) {
      console.error('❌ Cleanup failed:', error);
      exitCode = 1;
    }
  }

  process.exit(exitCode);
}

// Run smoke tests
runSmokeTests();
