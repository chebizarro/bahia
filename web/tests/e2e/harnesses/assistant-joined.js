// Joined backend harness for tests/e2e/assistant-unified-execution.spec.js.
//
// Starts, on loopback only:
//   - PostgreSQL (docker, TLS on, tmpfs data) so the backend runs at full tier;
//     without it the controlplane reactor and assistant recovery are gated off;
//   - bahia-test-relay (through relay-harness.js, same leak-safe lifecycle);
//   - cmd/bahia-assistant-e2e-provider, the deterministic OpenAI-compatible model;
//   - cmd/server built from this checkout with the assistant enabled, fresh
//     random service and fleet-operator keys, and no dev_mode relaxations;
//   - the production static dashboard build, served the way web/nginx.conf and
//     docker-entrypoint.d/40-bahia-bootstrap-env.sh serve it (bootstrap
//     placeholders substituted, SPA fallback, /api proxied to the backend);
//   - a loopback control endpoint whose POST /restart-backend is the spec's
//     restart command.
//
// stop() tears everything down; an exit hook SIGKILLs every process group and
// removes the container if the runner dies first.
import { execFile, execFileSync, spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { createWriteStream, existsSync, mkdirSync, readFileSync, rmSync, statSync, writeFileSync } from 'node:fs';
import { createServer, request as httpRequest } from 'node:http';
import net from 'node:net';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { getPublicKey } from 'nostr-tools';
import { startBahiaTestRelay } from '../relay-harness.js';

const execFileAsync = promisify(execFile);
const __dirname = path.dirname(fileURLToPath(import.meta.url));
export const webDir = path.resolve(__dirname, '../../..');
export const repoRoot = path.resolve(webDir, '..');
export const runDir = path.join(webDir, 'test-results', 'assistant-joined-harness');

const PG_IMAGE = process.env.BAHIA_ASSISTANT_E2E_PG_IMAGE || 'postgres:16';
const STARTUP_TIMEOUT_MS = 60_000;
const STOP_GRACE_MS = 20_000;

// ---------------------------------------------------------------------------
// Process and container bookkeeping (synchronous exit-hook safety net)

const liveGroups = new Set();
const liveContainers = new Set();
let exitHookInstalled = false;

function installExitHook() {
  if (exitHookInstalled) return;
  exitHookInstalled = true;
  process.once('exit', () => {
    for (const pid of liveGroups) {
      try { process.kill(-pid, 'SIGKILL'); } catch { /* already gone */ }
    }
    for (const name of liveContainers) {
      try { execFileSync('docker', ['rm', '-f', name], { stdio: 'ignore' }); } catch { /* already gone */ }
    }
  });
}

// Process groups and containers this harness started that are still alive.
// Empty after a clean stop(); the runner fails the run otherwise.
let relayAddr = '';

function tcpListening(addr) {
  if (!addr) return Promise.resolve(false);
  const [host, port] = [addr.slice(0, addr.lastIndexOf(':')), Number(addr.slice(addr.lastIndexOf(':') + 1))];
  return new Promise((resolve) => {
    const socket = net.connect({ host, port });
    socket.once('connect', () => { socket.destroy(); resolve(true); });
    socket.once('error', () => resolve(false));
  });
}

export async function harnessLeaks() {
  return {
    processGroups: [...liveGroups].filter(groupAlive),
    containers: [...liveContainers],
    relayListening: await tcpListening(relayAddr) ? relayAddr : ''
  };
}

function log(message) {
  process.stdout.write(`[assistant-joined] ${message}\n`);
}

function groupAlive(pid) {
  try {
    process.kill(-pid, 0);
    return true;
  } catch {
    return false;
  }
}

// Starts a command in its own process group, tees output to a log file and
// resolves once `ready` matches the accumulated output.
function startProcess(name, command, args, { env = process.env, cwd = repoRoot, ready, logFile }) {
  installExitHook();
  const child = spawn(command, args, { cwd, env, detached: true, stdio: ['ignore', 'pipe', 'pipe'] });
  liveGroups.add(child.pid);
  const out = createWriteStream(logFile, { flags: 'a' });
  let buffer = '';
  let settle;
  const readyPromise = new Promise((resolve, reject) => { settle = { resolve, reject }; });
  const onData = (chunk) => {
    out.write(chunk);
    if (!settle) return;
    buffer += chunk.toString();
    const match = buffer.match(ready);
    if (match) {
      settle.resolve(match);
      settle = null;
      buffer = '';
    }
  };
  child.stdout.on('data', onData);
  child.stderr.on('data', onData);
  const exited = new Promise((resolve) => child.once('exit', (code, signal) => {
    liveGroups.delete(child.pid);
    out.end();
    if (settle) settle.reject(new Error(`${name} exited before it was ready (code=${code} signal=${signal}); see ${logFile}`));
    settle = null;
    resolve({ code, signal });
  }));
  let timer;
  const readyWithTimeout = Promise.race([
    readyPromise,
    new Promise((_, reject) => { timer = setTimeout(() => reject(new Error(`timed out starting ${name}; see ${logFile}`)), STARTUP_TIMEOUT_MS); })
  ]).finally(() => clearTimeout(timer));

  return {
    name,
    pid: child.pid,
    ready: readyWithTimeout,
    exited,
    async stop() {
      if (child.exitCode === null && child.signalCode === null) {
        try { process.kill(-child.pid, 'SIGTERM'); } catch { /* gone */ }
        let graceTimer;
        const graceful = await Promise.race([
          exited.then(() => true),
          new Promise((resolve) => { graceTimer = setTimeout(() => resolve(false), STOP_GRACE_MS); })
        ]);
        clearTimeout(graceTimer);
        if (!graceful) {
          log(`${name} ignored SIGTERM for ${STOP_GRACE_MS}ms; killing`);
          try { process.kill(-child.pid, 'SIGKILL'); } catch { /* gone */ }
          await exited;
        }
      }
      // Reap anything the process left behind in its group.
      if (groupAlive(child.pid)) {
        try { process.kill(-child.pid, 'SIGKILL'); } catch { /* gone */ }
      }
      liveGroups.delete(child.pid);
    }
  };
}

async function freePort() {
  const server = net.createServer();
  server.listen(0, '127.0.0.1');
  await new Promise((resolve, reject) => { server.once('listening', resolve); server.once('error', reject); });
  const { port } = server.address();
  await new Promise((resolve) => server.close(resolve));
  return port;
}

function randomSecretHex() {
  return randomBytes(32).toString('hex');
}

async function run(command, args, options = {}) {
  try {
    return await execFileAsync(command, args, { cwd: repoRoot, maxBuffer: 64 * 1024 * 1024, ...options });
  } catch (error) {
    throw new Error(`${command} ${args.join(' ')} failed:\n${error.stdout || ''}${error.stderr || error.message}`);
  }
}

// ---------------------------------------------------------------------------
// PostgreSQL

async function startPostgres() {
  installExitHook();
  const name = `bahia-assistant-joined-${process.pid}-${randomBytes(3).toString('hex')}`;
  const password = randomBytes(12).toString('hex');
  // The Debian image ships a snakeoil certificate, so TLS is on and the backend
  // keeps its production sslmode=require validation.
  await run('docker', ['run', '-d', '--rm', '--name', name, '--label', 'io.bahia.e2e=assistant-joined',
    '-e', 'POSTGRES_USER=bahia', '-e', `POSTGRES_PASSWORD=${password}`, '-e', 'POSTGRES_DB=bahia',
    '-p', '127.0.0.1::5432', '--tmpfs', '/var/lib/postgresql/data',
    PG_IMAGE,
    '-c', 'ssl=on', '-c', 'ssl_cert_file=/etc/ssl/certs/ssl-cert-snakeoil.pem', '-c', 'ssl_key_file=/etc/ssl/private/ssl-cert-snakeoil.key',
    '-c', 'fsync=off', '-c', 'synchronous_commit=off', '-c', 'full_page_writes=off']);
  liveContainers.add(name);
  const { stdout } = await run('docker', ['port', name, '5432/tcp']);
  const port = Number(stdout.trim().split('\n')[0].split(':').pop());
  // Health check: TCP readiness inside the container. The image's init phase
  // runs a socket-only temporary server, so a TCP probe only succeeds once the
  // real server is up.
  const deadline = Date.now() + STARTUP_TIMEOUT_MS;
  for (;;) {
    try {
      await execFileAsync('docker', ['exec', name, 'pg_isready', '-h', '127.0.0.1', '-p', '5432', '-U', 'bahia', '-d', 'bahia']);
      break;
    } catch (error) {
      if (Date.now() > deadline) throw new Error(`PostgreSQL did not become ready: ${error.stderr || error.message}`);
      await new Promise((resolve) => setTimeout(resolve, 250));
    }
  }
  return {
    port,
    password,
    async stop() {
      try { await execFileAsync('docker', ['rm', '-f', name]); } catch { /* already gone */ }
      liveContainers.delete(name);
    }
  };
}

// ---------------------------------------------------------------------------
// Backend

export function backendConfig({ backendPort, pg, relayUrl, providerUrl, serviceSecretHex, operatorPubkey }) {
  const q = JSON.stringify; // JSON scalars are valid YAML scalars.
  const relays = `[${q(relayUrl)}]`;
  return `# Generated by web/tests/e2e/harnesses/assistant-joined.js; do not edit.
server:
  host: "127.0.0.1"
  port: ${backendPort}
  shutdown_timeout: 10s
db:
  host: "127.0.0.1"
  port: ${pg.port}
  user: bahia
  password: ${q(pg.password)}
  name: bahia
  sslmode: require
log:
  level: info
  format: json
reconcile:
  enabled: false
nostr:
  private_key: ${q(serviceSecretHex)}
  relays: ${relays}
  service_relays: ${relays}
  browser_relays: ${relays}
  contextvm_relays: ${relays}
  publish_enabled: true
  authorized_pubkeys: [${q(operatorPubkey)}]
loom:
  relays: ${relays}
# The DNS read tools are the backend's read-only assistant tools; with no
# zones and no projection sources they succeed with an empty read model.
dns:
  enabled: true
  projection:
    services: false
    llm_routes: false
    ml_endpoints: false
    workers: false
    mesh_endpoints: false
    capability_aliases: false
assistant:
  enabled: true
  llm_base_url: ${q(providerUrl)}
  llm_model: "joined-e2e-planner"
  default_workflow: "batch"
  agentic:
    provider: "openai_compatible"
    base_url: ${q(providerUrl)}
    model: "joined-e2e-agent"
    request_timeout: 15m
`;
}

async function readJSON(url) {
  const response = await fetch(url);
  if (!response.ok) throw new Error(`${url} -> HTTP ${response.status}`);
  return response.json();
}

function startBackend({ binary, configPath, backendPort, logFile }) {
  // The controlplane reactor logs this once its ContextVM subscription has
  // caught up; before that a browser request (ephemeral 25910) would be lost.
  const proc = startProcess('bahia backend', binary, ['-config', configPath], {
    ready: /control-plane EOSE received/,
    logFile
  });
  const ready = proc.ready.then(async () => {
    const deadline = Date.now() + STARTUP_TIMEOUT_MS;
    for (;;) {
      try {
        const body = await readJSON(`http://127.0.0.1:${backendPort}/ready`);
        if (body.ready === true && body.active_tier === 3) return body;
      } catch { /* not listening yet */ }
      if (Date.now() > deadline) throw new Error(`backend never reported ready at full tier; see ${logFile}`);
      await new Promise((resolve) => setTimeout(resolve, 200));
    }
  });
  return { ...proc, ready };
}

// ---------------------------------------------------------------------------
// Dashboard: static production build, bootstrap substituted at serve time.

const CONTENT_TYPES = {
  '.html': 'text/html; charset=utf-8', '.js': 'text/javascript; charset=utf-8', '.css': 'text/css; charset=utf-8',
  '.json': 'application/json', '.webmanifest': 'application/manifest+json', '.png': 'image/png', '.svg': 'image/svg+xml',
  '.ico': 'image/x-icon', '.woff2': 'font/woff2', '.woff': 'font/woff', '.txt': 'text/plain; charset=utf-8', '.map': 'application/json'
};

export function startDashboardServer({ buildDir, port, relayUrl, servicePubkey, backendPort }) {
  const indexHtml = readFileSync(path.join(buildDir, 'index.html'), 'utf8')
    .replaceAll('__PUBLIC_BAHIA_BOOTSTRAP_RELAYS__', relayUrl)
    .replaceAll('__PUBLIC_BAHIA_SERVICE_PUBKEYS__', servicePubkey);
  if (indexHtml.includes('__PUBLIC_BAHIA_')) throw new Error('dashboard build still contains bootstrap placeholders');
  const root = path.resolve(buildDir);
  const server = createServer((req, res) => {
    const url = new URL(req.url, 'http://127.0.0.1');
    if (url.pathname.startsWith('/api/')) {
      const upstream = httpRequest({ host: '127.0.0.1', port: backendPort, method: req.method, path: req.url, headers: req.headers }, (up) => {
        res.writeHead(up.statusCode || 502, up.headers);
        up.pipe(res);
      });
      upstream.on('error', () => { if (!res.headersSent) res.writeHead(502); res.end(); });
      req.pipe(upstream);
      return;
    }
    const filePath = path.resolve(root, `.${decodeURIComponent(url.pathname)}`);
    const insideRoot = filePath.startsWith(root + path.sep);
    if (insideRoot && existsSync(filePath) && statSync(filePath).isFile()) {
      res.writeHead(200, { 'content-type': CONTENT_TYPES[path.extname(filePath)] || 'application/octet-stream' });
      res.end(readFileSync(filePath));
      return;
    }
    if (url.pathname.startsWith('/_app/immutable/')) {
      res.writeHead(404);
      res.end();
      return;
    }
    res.writeHead(200, { 'content-type': CONTENT_TYPES['.html'], 'cache-control': 'no-store' });
    res.end(indexHtml);
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => resolve({
      url: `http://127.0.0.1:${port}/`,
      stop: () => new Promise((done) => { server.closeAllConnections?.(); server.close(() => done()); })
    }));
  });
}

// ---------------------------------------------------------------------------
// Control endpoint: the spec's restart command.

function startControlServer({ port, restartBackend }) {
  let restarting = null;
  const server = createServer(async (req, res) => {
    if (req.method !== 'POST' || req.url !== '/restart-backend') {
      res.writeHead(404);
      res.end();
      return;
    }
    try {
      restarting ||= restartBackend().finally(() => { restarting = null; });
      await restarting;
      res.writeHead(200, { 'content-type': 'text/plain' });
      res.end('restarted\n');
    } catch (error) {
      res.writeHead(500, { 'content-type': 'text/plain' });
      res.end(`${error?.stack || error}\n`);
    }
  });
  return new Promise((resolve, reject) => {
    server.once('error', reject);
    server.listen(port, '127.0.0.1', () => resolve({
      restartCommand: `node -e 'fetch(process.argv[1], { method: "POST" }).then(async (r) => { const t = await r.text(); if (!r.ok) { console.error(t); process.exit(1); } })' http://127.0.0.1:${port}/restart-backend`,
      stop: () => new Promise((done) => { server.closeAllConnections?.(); server.close(() => done()); })
    }));
  });
}

// ---------------------------------------------------------------------------
// Harness

export async function startAssistantJoinedHarness({ skipDashboardBuild = false } = {}) {
  rmSync(runDir, { recursive: true, force: true });
  const binDir = path.join(runDir, 'bin');
  mkdirSync(binDir, { recursive: true });
  const stops = [];
  const stopAll = async () => {
    for (const stop of stops.reverse()) {
      try { await stop(); } catch (error) { log(`teardown step failed: ${error?.message || error}`); }
    }
    stops.length = 0;
  };

  try {
    log('building backend, provider and dashboard');
    const serverBinary = path.join(binDir, 'bahia-server');
    const providerBinary = path.join(binDir, 'bahia-assistant-e2e-provider');
    await Promise.all([
      run('go', ['build', '-o', serverBinary, './cmd/server']),
      run('go', ['build', '-o', providerBinary, './cmd/bahia-assistant-e2e-provider']),
      // Warms the build cache for relay-harness.js, which starts the relay with `go run`.
      run('go', ['build', '-o', path.join(binDir, 'bahia-test-relay'), './cmd/bahia-test-relay']),
      skipDashboardBuild ? Promise.resolve() : run('pnpm', ['build'], { cwd: webDir })
    ]);

    log('starting PostgreSQL, relay and model provider');
    // allSettled so a partial start still tears down whatever did start.
    const started = await Promise.allSettled([
      startPostgres(),
      startBahiaTestRelay(),
      (async () => {
        const proc = startProcess('model provider', providerBinary, [], { ready: /listening on (\S+)/, logFile: path.join(runDir, 'provider.log') });
        try {
          const [, addr] = await proc.ready;
          return { ...proc, url: `http://${addr}` };
        } catch (error) {
          await proc.stop();
          throw error;
        }
      })()
    ]);
    for (const result of started) {
      if (result.status === 'fulfilled') stops.push(() => result.value.stop());
    }
    const failed = started.find((result) => result.status === 'rejected');
    if (failed) throw failed.reason;
    const [pg, relay, provider] = started.map((result) => result.value);
    relayAddr = relay.addr;
    const fixtures = await readJSON(`${provider.url}/fixtures`);

    const serviceSecretHex = randomSecretHex();
    const operatorSecretHex = randomSecretHex();
    const servicePubkey = getPublicKey(Buffer.from(serviceSecretHex, 'hex'));
    const operatorPubkey = getPublicKey(Buffer.from(operatorSecretHex, 'hex'));
    const [backendPort, dashboardPort, controlPort] = await Promise.all([freePort(), freePort(), freePort()]);
    const configPath = path.join(runDir, 'backend-config.yaml');
    writeFileSync(configPath, backendConfig({ backendPort, pg, relayUrl: relay.wsUrl, providerUrl: provider.url, serviceSecretHex, operatorPubkey }), { mode: 0o600 });

    let backendGeneration = 0;
    let backend = null;
    const launchBackend = async () => {
      backendGeneration += 1;
      const proc = startBackend({ binary: serverBinary, configPath, backendPort, logFile: path.join(runDir, `backend-${backendGeneration}.log`) });
      backend = proc;
      await proc.ready;
      return proc;
    };
    log('starting backend');
    stops.push(() => backend?.stop());
    await launchBackend();

    const restartBackend = async () => {
      log('restarting backend');
      await backend.stop();
      await launchBackend();
      log(`backend restarted (generation ${backendGeneration})`);
    };

    const dashboard = await startDashboardServer({ buildDir: path.join(webDir, 'build'), port: dashboardPort, relayUrl: relay.wsUrl, servicePubkey, backendPort });
    stops.push(() => dashboard.stop());
    const control = await startControlServer({ port: controlPort, restartBackend });
    stops.push(() => control.stop());

    const env = {
      BAHIA_ASSISTANT_E2E_JOINED_URL: dashboard.url,
      BAHIA_ASSISTANT_E2E_SECRET_HEX: operatorSecretHex,
      BAHIA_ASSISTANT_E2E_BATCH_PROMPT: fixtures.batch_prompt,
      BAHIA_ASSISTANT_E2E_EDITED_ARGS_JSON: fixtures.edited_first_step_args_json,
      BAHIA_ASSISTANT_E2E_ITERATIVE_PROMPT: fixtures.iterative_prompt,
      BAHIA_ASSISTANT_E2E_RESTART_COMMAND: control.restartCommand
    };
    // The reconciliation case stays skipped: its evidence is a kind-25910
    // request event, which is in NIP-01's ephemeral range. bahia-test-relay and
    // the production relay sidecar (both khatru) broadcast ephemeral events but
    // never store them, so the backend's evidence resolver (a REQ by id) cannot
    // find a real downstream request on any Bahia relay. Seeding fabricated
    // checkpoint state would not exercise the product; see bahia-0hq7t notes.
    log('reconciliation case not seeded: kind-25910 request evidence is not retained by khatru relays (bahia-0hq7t)');
    log(`ready: dashboard=${dashboard.url} relay=${relay.wsUrl} backend=http://127.0.0.1:${backendPort} logs=${runDir}`);
    return {
      env,
      servicePubkey,
      operatorPubkey,
      relayUrl: relay.wsUrl,
      providerUrl: provider.url,
      restartBackend,
      stop: stopAll
    };
  } catch (error) {
    await stopAll();
    throw error;
  }
}
