# E2E test scenario library

Tagged scenarios for the agent-driven harness in `test/e2e-agent`. Each
scenario implements the `Scenario` interface, receives `ScenarioDrivers`
(`web`: Playwright, `mcp`: MCP JSON-RPC client) and returns a `ScenarioResult`
with per-step status. Scenarios drive the web UI and the MCP endpoint; entity
reads and mutations in Bahia are relay subscriptions and signed intents, so
no scenario calls daemon REST routes.

## Categories

### Events (`events.ts`)

- `sidecarRelayDiscovery` — verifies that an explicit Nostr bootstrap seed is
  configured for the run (`BAHIA_BOOTSTRAP_RELAYS` or `BAHIA_NOSTR_RELAYS`,
  and `BAHIA_SERVICE_PUBKEYS` or `BAHIA_SERVICE_PUBKEY`). Tags: `events`,
  `nostr`, `sidecar`, `smoke`.

## Usage

```typescript
import { PlaywrightDriver } from '../drivers/playwright.js';
import { MCPDriver } from '../drivers/mcp.js';
import { getSmokeTests, getScenariosByTag, printSummary, getStats } from './index.js';

const drivers = { web: new PlaywrightDriver('http://localhost:3000'), mcp: new MCPDriver() };
for (const scenario of getSmokeTests()) {
  const result = await scenario.run(drivers);
  console.log(scenario.name, result.status, `${result.duration}ms`);
}
printSummary();   // categories, scenarios and tag counts
getStats();       // { totalScenarios, categories, tags, smokeTests, integrationTests, crudTests }
```

`cli.ts` selects scenarios with `--all`, `--tags a,b` (AND) or `--scenario
<name>`; `npm run scenarios` prints the summary.

## Tags

`smoke` (fast, critical path), `integration` (multi-step), `crud`, `web`
(Playwright), `mcp` (MCP tools), plus feature tags such as `events`, `nostr`,
`sidecar`.

## Structure

```typescript
interface Scenario {
  name: string;
  description: string;
  tags: string[];
  run(drivers: ScenarioDrivers): Promise<ScenarioResult>;
}

interface ScenarioResult {
  name: string;
  status: 'passed' | 'failed' | 'skipped' | 'error';
  duration: number;
  steps: TestStepResult[];
  error?: string;
  metadata?: Record<string, unknown>;
}
```

`passed`: every step and assertion succeeded; `failed`: an assertion failed
or a response was unexpected; `skipped`: a prerequisite is missing;
`error`: an unexpected exception.

## Adding a scenario

1. Create it in the matching category file (or a new file) and implement
   `Scenario`, using a `step()` helper to record each step's status and
   duration.
2. Add it to the category's exported array and, for a new file, to
   `categories` in `index.ts`.
3. Drive the web UI through `drivers.web` and the daemon through
   `drivers.mcp` (`listTools`, `callTool`); for signed intents use the MCP
   intent tools, which return `{status, intent_id, event_id}`.
4. Scenarios create their own data and should be idempotent; cleanup is not
   required because the stack is disposable.
