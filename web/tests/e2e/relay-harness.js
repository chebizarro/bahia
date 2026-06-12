import { spawn } from 'node:child_process';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { TEST_PUBKEY } from './helpers.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(__dirname, '../../..');
const defaultAddr = process.env.BAHIA_TEST_RELAY_ADDR || '127.0.0.1:48629';

export async function startBahiaTestRelay({ addr = defaultAddr } = {}) {
  const healthUrl = `http://${addr}/healthz`;
  const existing = await readRelayHealth(healthUrl);
  if (existing?.ok) {
    return relayHandle(addr, null, existing);
  }

  const child = spawn('go', ['run', './cmd/bahia-test-relay', '--addr', addr], {
    cwd: repoRoot,
    env: { ...process.env, BAHIA_TEST_RELAY_ADDR: addr },
    stdio: ['ignore', 'pipe', 'pipe']
  });

  let output = '';
  child.stdout.on('data', (chunk) => { output += chunk.toString(); });
  child.stderr.on('data', (chunk) => { output += chunk.toString(); });

  const deadline = Date.now() + 20_000;
  while (Date.now() < deadline) {
    if (child.exitCode !== null) {
      throw new Error(`Bahia test relay exited early with ${child.exitCode}:\n${output}`);
    }
    const health = await readRelayHealth(healthUrl);
    if (health?.ok) return relayHandle(addr, child, health);
    await new Promise((resolve) => setTimeout(resolve, 100));
  }

  child.kill('SIGTERM');
  throw new Error(`Timed out waiting for Bahia test relay at ${healthUrl}:\n${output}`);
}

function relayHandle(addr, child, health) {
  return {
    addr,
    httpUrl: `http://${addr}`,
    wsUrl: `ws://${addr}`,
    servicePubkey: health.service_pubkey,
    eventCount: health.events,
    async stop() {
      if (!child || child.exitCode !== null) return;
      child.kill('SIGTERM');
      await new Promise((resolve) => child.once('exit', resolve));
    }
  };
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

export async function installRelayBackedBrowserContext(page, relay, { authenticated = true } = {}) {
  await page.addInitScript(({ relayUrl, servicePubkey, authenticated, pubkey }) => {
    localStorage.clear();
    sessionStorage.clear();
    window.__BAHIA_BOOTSTRAP__ = {
      schema: 'bahia.bootstrap.v1',
      relay_urls: [relayUrl],
      service_pubkeys: [servicePubkey]
    };

    if (authenticated) {
      localStorage.setItem('bahia_auth_session', JSON.stringify({
        pubkey,
        relays: { [relayUrl]: { read: true, write: true } },
        lastAuthenticatedAt: new Date().toISOString()
      }));
      window.nostr = {
        getPublicKey: async () => pubkey,
        getRelays: async () => ({ [relayUrl]: { read: true, write: true } })
      };
    } else {
      localStorage.removeItem('bahia_auth_session');
      delete window.nostr;
    }
  }, { relayUrl: relay.wsUrl, servicePubkey: relay.servicePubkey, authenticated, pubkey: TEST_PUBKEY });
}

export async function installEmptyRestFallbacks(page) {
  await page.route('**/api/v1/**', (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ data: [] })
  }));
}
