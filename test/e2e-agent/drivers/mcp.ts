/**
 * MCP HTTP JSON-RPC client driver for Bahia MCP server
 */
import type { MCPToolCall, MCPToolResult } from '../types.js';

interface JSONRPCResponse<T> {
  jsonrpc: '2.0';
  id: number;
  result?: T;
  error?: {
    code: number;
    message: string;
    data?: unknown;
  };
}

interface MCPToolDefinition {
  name: string;
  description?: string;
}

interface ListToolsResult {
  tools: MCPToolDefinition[];
}

interface CallToolResult {
  content: Array<{ type: string; text: string }>;
  isError?: boolean;
}

export interface MCPHTTPConnectionOptions {
  serverUrl: string;
  headers?: Record<string, string>;
  fetchImpl?: typeof fetch;
}

/**
 * MCPDriver provides MCP client functionality for testing Bahia MCP tools.
 * Bahia exposes MCP as HTTP JSON-RPC at /mcp and /api/v1/mcp.
 */
export class MCPDriver {
  private serverUrl: string | null = null;
  private headers: Record<string, string> = {};
  private fetchImpl: typeof fetch = fetch;
  private connected = false;
  private nextID = 1;

  /**
   * Connect to the Bahia MCP HTTP JSON-RPC endpoint and verify tool discovery.
   */
  async connect(options: MCPHTTPConnectionOptions): Promise<void> {
    const serverUrl = options.serverUrl.trim();
    if (!serverUrl) {
      throw new Error('MCP server URL is required. Configure BAHIA_E2E_MCP_URL or use TestHarness.getMcpUrl().');
    }
    if (!/^https?:\/\//.test(serverUrl)) {
      throw new Error(`MCP server URL must be an HTTP(S) URL for Bahia native MCP; received ${serverUrl}`);
    }

    console.log(`🔌 Connecting to MCP server at ${serverUrl}...`);

    this.serverUrl = serverUrl;
    this.headers = options.headers ?? {};
    this.fetchImpl = options.fetchImpl ?? fetch;

    await this.request<ListToolsResult>('tools/list');
    this.connected = true;

    console.log('✅ Connected to MCP server');
  }

  /**
   * Disconnect from the MCP server.
   */
  async disconnect(): Promise<void> {
    this.serverUrl = null;
    this.headers = {};
    this.fetchImpl = fetch;
    this.connected = false;
    console.log('🔌 Disconnected from MCP server');
  }

  /**
   * List available tools.
   */
  async listTools(): Promise<Array<{ name: string; description: string }>> {
    this.ensureConnected();
    const response = await this.request<ListToolsResult>('tools/list');
    return response.tools.map(tool => ({
      name: tool.name,
      description: tool.description ?? '',
    }));
  }

  /**
   * Call an MCP tool.
   */
  async callTool(call: MCPToolCall): Promise<MCPToolResult> {
    this.ensureConnected();

    const response = await this.request<CallToolResult>('tools/call', {
      name: call.name,
      arguments: call.arguments,
    });

    return {
      content: response.content,
      isError: response.isError,
    };
  }

  /**
   * Check if connected.
   */
  isConnected(): boolean {
    return this.connected;
  }

  private async request<T>(method: string, params?: unknown): Promise<T> {
    if (!this.serverUrl) {
      throw new Error('MCP client not configured. Call connect() first.');
    }

    const id = this.nextID++;
    const response = await this.fetchImpl(this.serverUrl, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
        ...this.headers,
      },
      body: JSON.stringify({
        jsonrpc: '2.0',
        id,
        method,
        ...(typeof params === 'undefined' ? {} : { params }),
      }),
    });

    if (!response.ok) {
      const body = await response.text().catch(() => '');
      throw new Error(`MCP ${method} request failed with HTTP ${response.status}${body ? `: ${body}` : ''}`);
    }

    const payload = await response.json() as JSONRPCResponse<T>;
    if (payload.error) {
      throw new Error(`MCP ${method} JSON-RPC error ${payload.error.code}: ${payload.error.message}`);
    }
    if (typeof payload.result === 'undefined') {
      throw new Error(`MCP ${method} response did not include a result`);
    }

    return payload.result;
  }

  /**
   * Ensure client is connected.
   */
  private ensureConnected(): void {
    if (!this.connected) {
      throw new Error('MCP client not connected. Call connect() first.');
    }
  }

  // ==================== Bahia-specific helpers ====================

  /**
   * List services via MCP.
   */
  async bahiaListServices(): Promise<MCPToolResult> {
    return this.callTool({
      name: 'bahia_list_services',
      arguments: {},
    });
  }

  /**
   * Deprecated: direct service creation via MCP now returns a signer-first Nostr migration error.
   */
  async bahiaCreateService(data: {
    name: string;
    artifact_repo: string;
    repo_url?: string;
    runtime_type?: string;
  }): Promise<MCPToolResult> {
    return this.callTool({
      name: 'bahia_create_service',
      arguments: data,
    });
  }

  /**
   * Get service via MCP.
   */
  async bahiaGetService(serviceId: string): Promise<MCPToolResult> {
    return this.callTool({
      name: 'bahia_get_service',
      arguments: { service_id: serviceId },
    });
  }

  /**
   * List environments via MCP.
   */
  async bahiaListEnvironments(): Promise<MCPToolResult> {
    return this.callTool({
      name: 'bahia_list_environments',
      arguments: {},
    });
  }

  /**
   * Deprecated: direct environment creation via MCP now returns a signer-first Nostr migration error.
   */
  async bahiaCreateEnvironment(data: {
    name: string;
    protected?: boolean;
    deploy_strategy?: string;
  }): Promise<MCPToolResult> {
    return this.callTool({
      name: 'bahia_create_environment',
      arguments: data,
    });
  }

  /**
   * Deploy via MCP.
   */
  async bahiaDeploy(data: {
    service_id: string;
    environment_id: string;
    artifact_id: string;
    requested_by: string;
  }): Promise<MCPToolResult> {
    return this.callTool({
      name: 'bahia_deploy',
      arguments: data,
    });
  }
}
