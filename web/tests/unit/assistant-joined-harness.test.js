// @vitest-environment node
import { mkdtempSync, mkdirSync, rmSync, writeFileSync } from 'node:fs';
import { createServer, get } from 'node:http';
import os from 'node:os';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { backendConfig, startDashboardServer } from '../e2e/harnesses/assistant-joined.js';

const RELAY = 'ws://127.0.0.1:45001';
const SERVICE = 'b'.repeat(64);

// The unit setup stubs global fetch; talk HTTP directly.
function httpGet(url) {
  return new Promise((resolve, reject) => {
    get(url, (res) => {
      let body = '';
      res.setEncoding('utf8');
      res.on('data', (chunk) => { body += chunk; });
      res.on('end', () => resolve({ status: res.statusCode, contentType: res.headers['content-type'] || '', body }));
    }).on('error', reject);
  });
}

function listen(server) {
  return new Promise((resolve) => server.listen(0, '127.0.0.1', () => resolve(server.address().port)));
}

describe('assistant joined harness dashboard server', () => {
  let buildDir;
  let dashboard;
  let backend;

  beforeEach(async () => {
    buildDir = mkdtempSync(path.join(os.tmpdir(), 'joined-build-'));
    mkdirSync(path.join(buildDir, '_app', 'immutable'), { recursive: true });
    writeFileSync(path.join(buildDir, 'index.html'), "<script>relays='__PUBLIC_BAHIA_BOOTSTRAP_RELAYS__';keys='__PUBLIC_BAHIA_SERVICE_PUBKEYS__'</script>");
    writeFileSync(path.join(buildDir, '_app', 'immutable', 'entry.js'), 'export {};');
    backend = createServer((req, res) => res.end(JSON.stringify({ proxied: req.url })));
    const backendPort = await listen(backend);
    const probe = createServer();
    const port = await listen(probe);
    await new Promise((resolve) => probe.close(resolve));
    dashboard = await startDashboardServer({ buildDir, port, relayUrl: RELAY, servicePubkey: SERVICE, backendPort });
  });

  afterEach(async () => {
    await dashboard?.stop();
    await new Promise((resolve) => backend.close(resolve));
    rmSync(buildDir, { recursive: true, force: true });
  });

  it('substitutes the bootstrap placeholders like the nginx entrypoint and falls back to the app shell', async () => {
    for (const route of ['', 'services/abc']) {
      const { body } = await httpGet(`${dashboard.url}${route}`);
      expect(body).toContain(`relays='${RELAY}'`);
      expect(body).toContain(`keys='${SERVICE}'`);
      expect(body).not.toContain('__PUBLIC_BAHIA_');
    }
  });

  it('serves build assets, never falls back for immutable assets, and refuses traversal', async () => {
    const asset = await httpGet(`${dashboard.url}_app/immutable/entry.js`);
    expect(asset.contentType).toContain('text/javascript');
    expect(asset.body).toBe('export {};');
    expect((await httpGet(`${dashboard.url}_app/immutable/missing.js`)).status).toBe(404);
    const traversal = await httpGet(`${dashboard.url}..%2f..%2fetc%2fpasswd`);
    expect(traversal.body).toContain(`relays='${RELAY}'`);
  });

  it('proxies /api to the backend', async () => {
    expect(JSON.parse((await httpGet(`${dashboard.url}api/v1/orgs?x=1`)).body)).toEqual({ proxied: '/api/v1/orgs?x=1' });
  });
});

describe('assistant joined harness backend config', () => {
  it('pins the backend to the harness relay, provider and operator without dev_mode', () => {
    const yaml = backendConfig({
      backendPort: 41000,
      pg: { port: 42000, password: 'pw"with-quote' },
      relayUrl: RELAY,
      providerUrl: 'http://127.0.0.1:43000',
      serviceSecretHex: 'c'.repeat(64),
      operatorPubkey: 'd'.repeat(64)
    });
    expect(yaml).not.toMatch(/dev_mode/);
    expect(yaml).toContain('sslmode: require');
    expect(yaml).toContain('password: "pw\\"with-quote"');
    expect(yaml).toContain(`authorized_pubkeys: ["${'d'.repeat(64)}"]`);
    expect(yaml).toContain(`contextvm_relays: ["${RELAY}"]`);
    expect(yaml).toContain('llm_base_url: "http://127.0.0.1:43000"');
    expect(yaml).toContain('default_workflow: "batch"');
  });
});
