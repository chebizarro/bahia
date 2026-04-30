/**
 * MCP client driver for Bahia MCP server
 */
import { Client } from '@modelcontextprotocol/sdk/client/index.js';
import { StdioClientTransport } from '@modelcontextprotocol/sdk/client/stdio.js';
import type { MCPToolCall, MCPToolResult } from '../types.js';

/**
 * MCPDriver provides MCP client functionality for testing Bahia MCP tools
 */
export class MCPDriver {
  private client: Client | null = null;
  private transport: StdioClientTransport | null = null;
  private connected = false;

  /**
   * Connect to the MCP server
   * 
   * For stdio-based MCP servers (like Bahia), we need to launch the server process
   * and communicate via stdin/stdout.
   */
  async connect(options: {
    command: string;
    args?: string[];
    env?: Record<string, string>;
  }): Promise<void> {
    console.log('🔌 Connecting to MCP server...');

    // Create stdio transport
    this.transport = new StdioClientTransport({
      command: options.command,
      args: options.args ?? [],
      env: options.env,
    });

    // Create MCP client
    this.client = new Client(
      {
        name: 'bahia-e2e-test-client',
        version: '1.0.0',
      },
      {
        capabilities: {},
      }
    );

    // Connect client to transport
    await this.client.connect(this.transport);
    this.connected = true;

    console.log('✅ Connected to MCP server');
  }

  /**
   * Disconnect from the MCP server
   */
  async disconnect(): Promise<void> {
    if (this.client) {
      await this.client.close();
      this.client = null;
    }
    if (this.transport) {
      await this.transport.close();
      this.transport = null;
    }
    this.connected = false;
    console.log('🔌 Disconnected from MCP server');
  }

  /**
   * List available tools
   */
  async listTools(): Promise<Array<{ name: string; description: string }>> {
    this.ensureConnected();
    const response = await this.client!.listTools();
    return response.tools.map(tool => ({
      name: tool.name,
      description: tool.description ?? '',
    }));
  }

  /**
   * Call an MCP tool
   */
  async callTool(call: MCPToolCall): Promise<MCPToolResult> {
    this.ensureConnected();

    const response = await this.client!.callTool({
      name: call.name,
      arguments: call.arguments,
    });

    return {
      content: response.content as Array<{ type: string; text: string }>,
      isError: response.isError as boolean | undefined,
    };
  }

  /**
   * Check if connected
   */
  isConnected(): boolean {
    return this.connected;
  }

  /**
   * Ensure client is connected
   */
  private ensureConnected(): void {
    if (!this.connected || !this.client) {
      throw new Error('MCP client not connected. Call connect() first.');
    }
  }

  // ==================== Bahia-specific helpers ====================

  /**
   * List services via MCP
   */
  async bahiaListServices(): Promise<MCPToolResult> {
    return this.callTool({
      name: 'bahia_list_services',
      arguments: {},
    });
  }

  /**
   * Create service via MCP
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
   * Get service via MCP
   */
  async bahiaGetService(serviceId: string): Promise<MCPToolResult> {
    return this.callTool({
      name: 'bahia_get_service',
      arguments: { service_id: serviceId },
    });
  }

  /**
   * List environments via MCP
   */
  async bahiaListEnvironments(): Promise<MCPToolResult> {
    return this.callTool({
      name: 'bahia_list_environments',
      arguments: {},
    });
  }

  /**
   * Create environment via MCP
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
   * Deploy via MCP
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
