// One-command joined assistant E2E: `pnpm run test:e2e:joined [playwright args]`.
// Starts the joined backend harness, runs the joined Playwright config against
// it, and always tears the harness down. Exits with Playwright's status.
import { spawn } from 'node:child_process';
import { createRequire } from 'node:module';
import { harnessLeaks, startAssistantJoinedHarness, webDir } from './assistant-joined.js';

const require = createRequire(import.meta.url);
const playwrightCli = require.resolve('@playwright/test/cli');

let harness = null;
let playwright = null;
let stopping = null;

async function shutdown() {
  stopping ||= (async () => {
    if (playwright && playwright.exitCode === null) playwright.kill('SIGTERM');
    if (harness) await harness.stop();
  })();
  return stopping;
}

for (const [signal, code] of [['SIGINT', 130], ['SIGTERM', 143]]) {
  process.on(signal, () => {
    shutdown().finally(() => process.exit(code));
  });
}

const started = Date.now();
let status = 1;
try {
  harness = await startAssistantJoinedHarness({ skipDashboardBuild: process.env.BAHIA_ASSISTANT_E2E_SKIP_DASHBOARD_BUILD === '1' });
  const setupSeconds = ((Date.now() - started) / 1000).toFixed(1);
  console.log(`[assistant-joined] harness ready in ${setupSeconds}s`);
  playwright = spawn(process.execPath, [playwrightCli, 'test', '--config', 'playwright.joined.config.js', ...process.argv.slice(2)], {
    cwd: webDir,
    env: { ...process.env, ...harness.env },
    stdio: 'inherit'
  });
  status = await new Promise((resolve) => playwright.once('exit', (code, signal) => resolve(code ?? (signal ? 1 : 0))));
} catch (error) {
  console.error(`[assistant-joined] ${error?.stack || error}`);
  status = 1;
} finally {
  await shutdown();
  const leaks = await harnessLeaks();
  if (leaks.processGroups.length || leaks.containers.length || leaks.relayListening) {
    console.error(`[assistant-joined] teardown leaked ${JSON.stringify(leaks)}`);
    status = status || 1;
  }
  console.log(`[assistant-joined] finished in ${((Date.now() - started) / 1000).toFixed(1)}s with status ${status}`);
}
process.exit(status);
