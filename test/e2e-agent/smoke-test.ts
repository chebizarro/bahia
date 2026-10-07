/***/
 * Smoke test for E2E agent test harness
 *
 * This script verifies that the harness, the Playwright driver and the MCP
 * driver can connect and perform basic operations against the Bahia stack.
 */
import { TestHarness } from './harness.js';
import { PlaywrightDriver } from './drivers/playwright.js';
import { MCPDriver } from './drivers/mcp.js';

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

    // ==================== Daemon readiness ====================
    console.log('🌐 Test 2: Daemon health and readiness');
    for (const route of ['/health', '/ready']) {
      const response = await fetch(`${harness.getApiUrl()}${route}`);
      if (!response.ok) throw new Error(`GET ${route} returned ${response.status}`);
      console.log(`  ✅ ${route}:`, await response.json());
    }
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

    // ==================== MCP Driver ====================
    console.log('🔌 Test 4: MCP Driver');
    const mcpDriver = new MCPDriver();
    await mcpDriver.connect({ serverUrl: harness.getMcpUrl() });
    const tools = await mcpDriver.listTools();
    console.log('  ✅ Listed MCP tools:', tools.length);
    await mcpDriver.disconnect();
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
