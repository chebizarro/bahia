# E2E Agent Test Harness

Agent-driven end-to-end testing infrastructure for Bahia. This harness provides programmatic control over the full Bahia stack (postgres, bahia server, web UI) and drivers for testing all three interfaces: REST API, Web UI (Playwright), and MCP.

## Components

### Core Infrastructure

- **`harness.ts`** - Main orchestration layer
  - Launches/stops docker-compose stack programmatically
  - Waits for service health checks
  - Provides service logs and status

### Drivers

- **`drivers/api.ts`** - REST API driver
  - Typed methods for all Bahia API endpoints
  - Services, environments, deployments, artifacts, etc.
  - Built on native `fetch` for minimal dependencies

- **`drivers/playwright.ts`** - Web UI driver
  - Playwright-based browser automation
  - Navigation helpers for Bahia pages
  - Screenshot and DOM inspection capabilities

- **`drivers/mcp.ts`** - MCP client driver
  - Connects to Bahia MCP server via stdio
  - Invokes MCP tools for deployment operations
  - Typed helpers for common Bahia MCP operations

### Types

- **`types.ts`** - Shared TypeScript types
  - Test results and status
  - API entities (Service, Environment, etc.)
  - Driver capabilities

## Installation

```bash
cd test/e2e-agent
npm install
```

## Usage

### Smoke Test

Run the smoke test to verify all drivers work:

```bash
npm run smoke
```

This will:
1. Launch the docker-compose stack
2. Wait for all services to be healthy
3. Test REST API operations (create service, environment)
4. Test Web UI navigation with Playwright
5. Clean up the stack

### Manual Testing

```typescript
import { TestHarness } from './harness.js';
import { BahiaAPIDriver } from './drivers/api.js';
import { PlaywrightDriver } from './drivers/playwright.js';

const harness = new TestHarness();

// Start stack
await harness.start();

// Use API driver
const api = new BahiaAPIDriver(harness.getApiUrl());
const service = await api.createService({
  name: 'my-app',
  artifact_repo: 'registry.example.com/my-app',
  runtime_type: 'docker',
});

// Use Playwright driver
const web = new PlaywrightDriver(harness.getWebUrl());
await web.launch();
await web.goToServices();
await web.screenshot('/tmp/services.png');
await web.close();

// Cleanup
await harness.cleanup();
```

### MCP Driver

The MCP driver requires the Bahia server to expose an MCP interface. Example usage:

```typescript
import { MCPDriver } from './drivers/mcp.js';

const mcp = new MCPDriver();

// Connect to Bahia MCP server
await mcp.connect({
  command: 'bahia-mcp-server',  // Replace with actual command
  args: [],
  env: { BAHIA_DB_HOST: 'localhost' },
});

// List available tools
const tools = await mcp.listTools();
console.log('Available tools:', tools);

// Call a tool
const result = await mcp.bahiaListServices();
console.log('Services:', result);

await mcp.disconnect();
```

## Configuration

The harness uses sensible defaults but can be configured:

```typescript
const harness = new TestHarness({
  composeFile: '../../docker-compose.yml',
  projectName: 'bahia-e2e-test',
  healthCheckTimeout: 60000,  // 60 seconds
  healthCheckInterval: 2000,  // 2 seconds
  apiBaseUrl: 'http://localhost:8080',
  webBaseUrl: 'http://localhost:3000',
});
```

## Architecture

```
┌─────────────────────────────────────────┐
│          Test Harness                   │
│  (Docker Compose Orchestration)         │
└──────────┬──────────────────────────────┘
           │
           ├─────► Docker Compose Stack
           │       ├─ postgres:5432
           │       ├─ bahia:8080 (API + MCP)
           │       └─ web:3000
           │
           ├─────► API Driver ──────► REST API (:8080/api/v1)
           │
           ├─────► Playwright Driver ──► Web UI (:3000)
           │
           └─────► MCP Driver ──────► MCP Server (stdio)
```

## Docker Compose Ports

- **Postgres**: 5432
- **Bahia API**: 8080
- **Web UI**: 3000

## Next Steps

This harness is **Item 2** of the E2E testing plan. The remaining items are:

- **Item 3**: Test Scenario Library - Define comprehensive test scenarios
- **Item 4**: Agent Test Runner - AI agent loop to execute scenarios
- **Item 5**: Self-Healing Loop - Diagnose failures and submit fixes

## Troubleshooting

### Docker daemon preflight

When the harness manages the stack, it runs `docker info` before `docker compose up`. If Docker is not reachable, `pnpm test:smoke` fails before scenario execution with an actionable preflight error. Start Docker Desktop or a compatible Docker daemon, verify `docker info` succeeds, and rerun the smoke command.

### Health check timeout

If services don't become healthy within 60 seconds, check the logs:

```typescript
const logs = await harness.getLogs('bahia');
console.log(logs);
```

### Port conflicts

If ports 5432, 8080, or 3000 are already in use, stop conflicting services:

```bash
docker ps
docker stop <container-id>
```

Or modify the docker-compose.yml port mappings.

### MCP connection issues

The MCP driver requires the Bahia server to expose an MCP stdio interface. Verify:

1. Bahia MCP server is implemented in `internal/mcp/server.go`
2. A CLI entrypoint exists to run the MCP server
3. The command path is correct in `MCPDriver.connect()`
