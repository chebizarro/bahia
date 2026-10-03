import { spawn } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { RELAY_OPERATOR_PUBKEY } from './e2e-keyring.js';
import { installTestSigner } from './test-signer.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '../../..');
const defaultAddr = process.env.BAHIA_TEST_RELAY_ADDR || '127.0.0.1:0';
export { RELAY_OPERATOR_PUBKEY };

const activeRelayProcesses = new Map();
let cleanupHooksInstalled = false;

export async function startBahiaTestRelay({
  addr = defaultAddr,
  waitForReady = waitForRelayReady,
  spawnImpl = spawn,
  killImpl = process.kill.bind(process)
} = {}) {
  const child = spawnImpl('go', ['run', './cmd/bahia-test-relay', '--addr', addr], {
    cwd: repoRoot,
    detached: process.platform !== 'win32',
    env: { ...process.env, BAHIA_TEST_RELAY_ADDR: addr },
    stdio: ['ignore', 'pipe', 'pipe']
  });
  activeRelayProcesses.set(child, killImpl);
  installCleanupHooks();

  let output = '';
  let resolveReady;
  let rejectReady;
  const ready = new Promise((resolve, reject) => {
    resolveReady = resolve;
    rejectReady = reject;
  });
  const captureOutput = (chunk) => {
    output += chunk.toString();
    const match = output.match(/bahia test relay listening on (\S+)/);
    if (match) resolveReady(match[1]);
  };
  child.stdout.on('data', captureOutput);
  child.stderr.on('data', captureOutput);
  child.once('exit', (code) => {
    rejectReady(new Error(`Bahia test relay exited early with ${code}:\n${output}`));
  });

  let timeout;
  try {
    const started = await Promise.race([
      waitForReady({ child, ready, readHealth: readRelayHealth }),
      new Promise((_, reject) => {
        timeout = setTimeout(() => reject(new Error(`Timed out waiting for Bahia test relay requested at ${addr}:\n${output}`)), 20_000);
      })
    ]);
    if (!started.health?.ok) {
      throw new Error(`Bahia test relay signaled readiness before health was available at ${started.healthUrl}:\n${output}`);
    }
    return relayHandle(started.addr, child, started.health, killImpl);
  } catch (error) {
    await stopRelayProcess(child, killImpl);
    throw error;
  } finally {
    clearTimeout(timeout);
  }
}

async function waitForRelayReady({ ready, readHealth }) {
  const addr = await ready;
  const healthUrl = `http://${addr}/healthz`;
  return { addr, healthUrl, health: await readHealth(healthUrl) };
}

function relayHandle(addr, child, health, killImpl) {
  return {
    addr,
    httpUrl: `http://${addr}`,
    wsUrl: `ws://${addr}`,
    servicePubkey: health.service_pubkey,
    eventCount: health.events,
    async stop() {
      await stopRelayProcess(child, killImpl);
    }
  };
}

async function stopRelayProcess(child, killImpl) {
  if (!child) return;

  if (child.exitCode === null) {
    const exited = new Promise((resolve) => child.once('exit', resolve));
    let forceKillTimer;
    signalRelayProcess(child, 'SIGTERM', killImpl);
    const exitedGracefully = await Promise.race([
      exited.then(() => true),
      new Promise((resolve) => {
        forceKillTimer = setTimeout(() => resolve(false), 5_000);
      })
    ]);
    clearTimeout(forceKillTimer);
    if (!exitedGracefully) {
      signalRelayProcess(child, 'SIGKILL', killImpl);
      await exited;
    }
  } else {
    signalRelayProcess(child, 'SIGTERM', killImpl);
  }

  if (relayProcessGroupIsAlive(child, killImpl)) {
    signalRelayProcess(child, 'SIGKILL', killImpl);
  }
  activeRelayProcesses.delete(child);
}

function signalRelayProcess(child, signal, killImpl) {
  if (process.platform !== 'win32' && child.pid) {
    try {
      killImpl(-child.pid, signal);
      return;
    } catch (error) {
      if (error?.code === 'ESRCH') return;
    }
  }
  if (child.exitCode === null) child.kill(signal);
}

function relayProcessGroupIsAlive(child, killImpl) {
  if (process.platform === 'win32' || !child.pid) return false;
  try {
    killImpl(-child.pid, 0);
    return true;
  } catch {
    return false;
  }
}

function installCleanupHooks() {
  if (cleanupHooksInstalled) return;
  cleanupHooksInstalled = true;
  process.once('exit', () => stopAllRelayProcesses('SIGKILL'));
  process.once('SIGINT', () => terminateAfterCleanup(130));
  process.once('SIGTERM', () => terminateAfterCleanup(143));
}

function stopAllRelayProcesses(signal) {
  for (const [child, killImpl] of activeRelayProcesses) {
    signalRelayProcess(child, signal, killImpl);
  }
}

function terminateAfterCleanup(exitCode) {
  stopAllRelayProcesses('SIGTERM');
  process.exit(exitCode);
}

async function readRelayHealth(url) {
  try {
    const response = await fetch(url);
    if (!response.ok) return null;
    return await response.json();
  } catch {
    return null;
  }
}

export async function installRelayBackedBrowserContext(page, relay, { authenticated = true, roleOverride = true } = {}) {
  if (authenticated) await installTestSigner(page, { pubkey: RELAY_OPERATOR_PUBKEY, relays: [relay.wsUrl] });

  await page.addInitScript(({ relayUrl, servicePubkey, authenticated, pubkey, roleOverride }) => {
    localStorage.clear();
    sessionStorage.clear();
    window.__BAHIA_BOOTSTRAP__ = {
      schema: 'bahia.bootstrap.v1',
      relay_urls: [relayUrl],
      service_pubkeys: [servicePubkey]
    };
    localStorage.setItem('bahia_nostr_relays', JSON.stringify([relayUrl]));

    if (authenticated) {
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        pubkey,
        relays: { [relayUrl]: { read: true, write: true } },
        lastAuthenticatedAt: new Date().toISOString()
      }));
      if (roleOverride) window.__BAHIA_E2E_USER_ROLES = ['owner'];
      else delete window.__BAHIA_E2E_USER_ROLES;
    } else {
      localStorage.removeItem('bahia_auth_session');
      delete window.nostr;
      delete window.__BAHIA_E2E_USER_ROLES;
    }
  }, { relayUrl: relay.wsUrl, servicePubkey: relay.servicePubkey, authenticated, pubkey: RELAY_OPERATOR_PUBKEY, roleOverride });
}

export async function installEmptyRestFallbacks(page) {
  await page.route('**/api/v1/**', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ data: new URL(route.request().url()).pathname === '/api/v1/orgs'
      ? [{ id: 'org-e2e', name: 'E2E organization', role: 'owner' }]
      : [] })
  }));
}
