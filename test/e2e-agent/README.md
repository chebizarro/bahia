# E2E agent harness

`test/e2e-agent` is a TypeScript harness that launches the Bahia
docker-compose stack (postgres, `bahia` on 8080, web on 3000), drives it
through three drivers, runs a tagged scenario library, and can hand failures
to a self-healing loop that proposes fixes. It is independent of the web
Playwright suites in `web/tests/e2e`.

## Layout

| Path | Role |
|---|---|
| `harness.ts` | `TestHarness`: `docker compose up`/`down`, health-check wait, log retrieval, URL accessors. Runs `docker info` first and exits with `DockerPreflightError` and remediation text if no daemon is reachable |
| `drivers/api.ts` | `BahiaAPIDriver`: typed `fetch` calls against the daemon's HTTP routes |
| `drivers/playwright.ts` | `PlaywrightDriver`: browser launch, navigation helpers, screenshots, DOM inspection |
| `drivers/mcp.ts` | `MCPDriver`: JSON-RPC client for the daemon's MCP endpoint (`tools/list`, `tools/call`) |
| `scenarios/` | Scenario library (see `scenarios/README.md`) |
| `runner.ts`, `cli.ts`, `reporter.ts` | Scenario execution, CLI flags, JSON/HTML reports |
| `diagnostics.ts`, `fixer.ts`, `healing-loop.ts` | Failure diagnosis and the optional fix loop |
| `check-*.ts`, `smoke-test.ts`, `mcp-config.test.ts` | Standalone checks |
| `types.ts` | Shared types (`Scenario`, `ScenarioResult`, `TestStepResult`, driver config) |

## Setup

```bash
cd test/e2e-agent
npm install
npm run typecheck          # tsc --noEmit
```

## Running

```bash
npm test                   # tsx cli.ts --all
npm run test:smoke         # tsx cli.ts --tags smoke
npm run test:json          # machine-readable report
npm run smoke              # driver smoke test (stack up, API + web + MCP tools/list, stack down)
npm run scenarios          # print the scenario library summary
npm run demo
npm run test:mcp-config
npm run clean              # docker compose down -v
```

`cli.ts` options: `--all`, `--tags <a,b>`, `--scenario <name>` (repeatable),
`--json`, `--html <path>`, `--continue-on-failure`, `--headed`, `--skip-mcp`,
`--mcp-url <url>`, `--use-existing-stack`, `--heal`, `--max-iterations <n>`,
`--approve-fixes`, `--help`.

- The harness manages the stack by default. `--use-existing-stack` skips
  docker-compose and only performs HTTP health checks against the configured
  URLs.
- The MCP URL defaults to `http://localhost:8080/mcp`, derived from
  `apiBaseUrl`; override with `BAHIA_E2E_MCP_URL` or `--mcp-url`. The daemon
  mounts MCP at `POST /mcp` (platform-admin authenticated). MCP connection
  failures are fatal unless `--skip-mcp` is given.

Harness configuration (`TestHarness` constructor):

```typescript
new TestHarness({
  composeFile: '../../docker-compose.yml',
  projectName: 'bahia-e2e-test',
  healthCheckTimeout: 60000,
  healthCheckInterval: 2000,
  apiBaseUrl: 'http://localhost:8080',
  webBaseUrl: 'http://localhost:3000',
  mcpServerUrl: 'http://localhost:8080/mcp',
});
```

## What the daemon serves

The daemon's HTTP surface is health/readiness/metrics, MCP, deployment and
live logs, DB-less payment reads, config-fabric drift, the Blossom blob proxy
and a few HTTP-native routes; entity reads and mutations are relay
subscriptions and signed intents (`docs/architecture/cli-and-mcp.md`).
`drivers/api.ts` and the `api`-tagged scenarios still address
`/api/v1/services`, `/api/v1/environments`, policies, secrets and SSE events,
which return 404 from the current daemon, so those scenarios fail until the
driver is rewritten over the MCP tools or `pkg/client`. The `web` and `mcp`
drivers are the usable paths today.

## Troubleshooting

- **Health check timeout** — `await harness.getLogs('bahia')`.
- **Port conflicts** — ports 5432, 8080 and 3000 must be free
  (`docker ps`, `docker stop <id>`), or change the compose port mappings.
- **MCP connection** — the stack must be up and `BAHIA_E2E_MCP_URL` /
  `mcpServerUrl` / `apiBaseUrl` must point at it; the endpoint is
  `POST /mcp` in `internal/api/router/router.go`.
