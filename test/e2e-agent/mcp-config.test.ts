import assert from 'node:assert/strict';
import { TestHarness } from './harness.js';
import { MCPDriver } from './drivers/mcp.js';

async function testHarnessMcpUrlDefaultsToBahiaHttpEndpoint(): Promise<void> {
  const harness = new TestHarness({ apiBaseUrl: 'http://bahia.test:8080' });
  assert.equal(harness.getMcpUrl(), 'http://bahia.test:8080/mcp');
}

async function testHarnessMcpUrlAllowsExplicitExternalConfig(): Promise<void> {
  const harness = new TestHarness({
    apiBaseUrl: 'http://bahia.test:8080',
    mcpServerUrl: 'http://mcp.test/api/v1/mcp',
  });
  assert.equal(harness.getMcpUrl(), 'http://mcp.test/api/v1/mcp');
}

async function testDriverFailsClosedForNonHttpPlaceholder(): Promise<void> {
  const driver = new MCPDriver();
  await assert.rejects(
    () => driver.connect({ serverUrl: 'stdio://bahia-mcp' }),
    /must be an HTTP\(S\) URL/,
  );
  assert.equal(driver.isConnected(), false);
}

async function testDriverConnectsWithToolsListJsonRpc(): Promise<void> {
  const requests: Array<{ url: string; body: unknown }> = [];
  const fetchImpl: typeof fetch = async (input, init) => {
    const body = JSON.parse(String(init?.body));
    requests.push({ url: String(input), body });

    assert.equal(init?.method, 'POST');
    assert.equal((init?.headers as Record<string, string>)['Content-Type'], 'application/json');
    assert.equal(body.jsonrpc, '2.0');

    if (body.method === 'tools/list') {
      return new Response(JSON.stringify({
        jsonrpc: '2.0',
        id: body.id,
        result: { tools: [{ name: 'bahia_list_services', description: 'List services' }] },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }

    return new Response(JSON.stringify({
      jsonrpc: '2.0',
      id: body.id,
      error: { code: -32601, message: `unknown method ${body.method}` },
    }), { status: 200, headers: { 'Content-Type': 'application/json' } });
  };

  const driver = new MCPDriver();
  await driver.connect({ serverUrl: 'http://bahia.test:8080/mcp', fetchImpl });
  assert.equal(driver.isConnected(), true);

  const tools = await driver.listTools();
  assert.deepEqual(tools, [{ name: 'bahia_list_services', description: 'List services' }]);

  assert.deepEqual(requests.map(request => request.url), [
    'http://bahia.test:8080/mcp',
    'http://bahia.test:8080/mcp',
  ]);
  assert.deepEqual(requests.map(request => (request.body as { method: string }).method), [
    'tools/list',
    'tools/list',
  ]);

  await driver.disconnect();
  assert.equal(driver.isConnected(), false);
}

await testHarnessMcpUrlDefaultsToBahiaHttpEndpoint();
await testHarnessMcpUrlAllowsExplicitExternalConfig();
await testDriverFailsClosedForNonHttpPlaceholder();
await testDriverConnectsWithToolsListJsonRpc();

console.log('✅ MCP harness configuration verification passed');
