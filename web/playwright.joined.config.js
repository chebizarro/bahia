import { defineConfig, devices } from '@playwright/test';

// Joined assistant E2E: runs only against the backend harness started by
// `pnpm run test:e2e:joined` (tests/e2e/harnesses/run-assistant-joined.js).
// No webServer: the harness serves the production dashboard build itself.
// The spec shares one backend and restarts it, so it runs serially.
export default defineConfig({
  testDir: './tests/e2e',
  testMatch: /assistant-unified-execution\.spec\.js$/,
  fullyParallel: false,
  workers: 1,
  retries: 0,
  forbidOnly: !!process.env.CI,
  timeout: 180_000,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never', outputFolder: 'playwright-report/assistant-joined' }]] : [['list']],
  outputDir: 'test-results/assistant-joined',
  use: {
    trace: 'retain-on-failure'
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] }
    }
  ]
});
