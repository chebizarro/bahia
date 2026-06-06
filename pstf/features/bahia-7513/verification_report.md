# Verification Report: bahia-7513

## Observed behavior

- `test/e2e-agent/harness.ts` carried `mcpServerUrl: 'stdio://bahia-mcp'`, which did not match Bahia's implemented MCP transport.
- `test/e2e-agent/drivers/mcp.ts` used stdio MCP SDK transport even though Bahia exposes native MCP JSON-RPC over HTTP.
- `test/e2e-agent/smoke-test.ts` skipped MCP coverage and documented that additional configuration was pending.

## Intended behavior

- The harness derives the MCP endpoint from the Bahia API base URL as `<apiBaseUrl>/mcp`, with explicit override via `mcpServerUrl` or `BAHIA_E2E_MCP_URL`.
- The MCP driver connects to Bahia's HTTP JSON-RPC MCP endpoint and verifies `tools/list` during connection.
- Invalid non-HTTP placeholder configuration fails closed with an explicit error.
- Smoke coverage exercises MCP tool discovery when stack execution reaches the MCP phase.
- Docker-unavailable environments continue to fail at Docker preflight before stack execution.

## Verification evidence

- `pnpm run typecheck` passed in `test/e2e-agent`.
- `pnpm run test:mcp-config` passed in `test/e2e-agent`; it verified default MCP URL derivation, explicit MCP URL override, rejection of `stdio://bahia-mcp`, and JSON-RPC `tools/list` request behavior.
- `pnpm test:smoke` from the repository root failed because this repository has no root `package.json`; the e2e-agent package owns the script.
- `pnpm test:smoke` passed the expected negative verification in `test/e2e-agent` by exiting 1 at `DockerPreflightError` before stack execution because Docker is unavailable.

## Known boundaries

- Docker is unavailable in this environment, so full stack MCP smoke execution is expected to stop at Docker preflight. The MCP connection behavior is covered deterministically by `mcp-config.test.ts` without starting the stack.
