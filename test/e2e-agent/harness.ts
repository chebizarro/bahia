/**
 * Test harness for launching and managing the Bahia docker-compose stack
 */
import { exec } from 'child_process';
import { promisify } from 'util';
import type { HarnessConfig, ServiceHealth } from './types.js';

const execAsync = promisify(exec);

export class DockerPreflightError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'DockerPreflightError';
  }
}

function commandFailureDetails(error: unknown): string {
  if (error && typeof error === 'object') {
    const details: string[] = [];
    const maybeError = error as { code?: unknown; stdout?: unknown; stderr?: unknown; message?: unknown };

    if (typeof maybeError.code !== 'undefined') {
      details.push(`exit code ${String(maybeError.code)}`);
    }
    if (typeof maybeError.stderr === 'string' && maybeError.stderr.trim()) {
      details.push(maybeError.stderr.trim());
    }
    if (typeof maybeError.stdout === 'string' && maybeError.stdout.trim()) {
      details.push(maybeError.stdout.trim());
    }
    if (details.length > 0) {
      return details.join('; ');
    }
    if (typeof maybeError.message === 'string' && maybeError.message.trim()) {
      return maybeError.message.trim();
    }
  }

  return String(error);
}

function dockerPreflightMessage(details: string): string {
  return `Docker daemon is not available for the e2e-agent harness. Start Docker Desktop or a compatible Docker daemon, verify \`docker info\` succeeds, then rerun \`pnpm test:smoke\`. Details: ${details}`;
}

function outputIncludesDaemonFailure(output: string): boolean {
  return /cannot connect to the docker daemon|is the docker daemon running/i.test(output);
}

export async function assertDockerDaemonAvailable(): Promise<void> {
  try {
    const { stdout, stderr } = await execAsync('docker info --format "{{.ServerVersion}}"');
    const output = `${stdout}\n${stderr}`.trim();

    if (!stdout.trim() || outputIncludesDaemonFailure(output)) {
      throw new DockerPreflightError(dockerPreflightMessage(output || 'docker info returned no daemon version'));
    }
  } catch (error) {
    if (error instanceof DockerPreflightError) {
      throw error;
    }

    throw new DockerPreflightError(dockerPreflightMessage(commandFailureDetails(error)));
  }
}

/**
 * Default configuration
 */
const DEFAULT_CONFIG: Required<HarnessConfig> = {
  composeFile: '../../docker-compose.yml',
  projectName: 'bahia-e2e-test',
  healthCheckTimeout: 60000, // 60 seconds
  healthCheckInterval: 2000, // 2 seconds
  apiBaseUrl: process.env.BAHIA_E2E_API_URL ?? 'http://localhost:8080',
  webBaseUrl: process.env.BAHIA_E2E_WEB_URL ?? 'http://localhost:3000',
  mcpServerUrl: `${process.env.BAHIA_E2E_API_URL ?? 'http://localhost:8080'}/mcp`,
  skipStackManagement: false, // if true, use existing stack instead of managing docker-compose
};

/**
 * TestHarness manages the docker-compose stack lifecycle
 */
export class TestHarness {
  private config: Required<HarnessConfig>;
  private isRunning = false;

  constructor(config: Partial<HarnessConfig> = {}) {
    this.config = {
      ...DEFAULT_CONFIG,
      ...config,
      mcpServerUrl: config.mcpServerUrl ?? `${config.apiBaseUrl ?? DEFAULT_CONFIG.apiBaseUrl}/mcp`,
    };
  }

  /**
   * Start the docker-compose stack (or verify existing stack if skipStackManagement is true)
   */
  async start(): Promise<void> {
    if (this.config.skipStackManagement) {
      console.log('⏭️  Skipping docker-compose management, using existing stack...');
      // Just verify the stack is healthy
      await this.waitForHealth();
      this.isRunning = true;
      return;
    }

    await assertDockerDaemonAvailable();

    console.log('🚀 Starting docker-compose stack...');
    
    const composeCmd = `docker compose -f ${this.config.composeFile} -p ${this.config.projectName} up -d`;
    
    try {
      const { stdout, stderr } = await execAsync(composeCmd);
      if (stderr && !stderr.includes('Creating') && !stderr.includes('Started')) {
        console.warn('Docker compose stderr:', stderr);
      }
      console.log('✅ Docker compose started');
      this.isRunning = true;
      
      // Wait for services to be healthy
      await this.waitForHealth();
    } catch (error) {
      throw new Error(`Failed to start docker-compose: ${error}`);
    }
  }

  /**
   * Stop the docker-compose stack
   */
  async stop(): Promise<void> {
    if (this.config.skipStackManagement) {
      console.log('⏭️  Skipping docker-compose stop (using external stack)');
      this.isRunning = false;
      return;
    }

    if (!this.isRunning) {
      return;
    }

    console.log('🛑 Stopping docker-compose stack...');
    
    const composeCmd = `docker compose -f ${this.config.composeFile} -p ${this.config.projectName} down`;
    
    try {
      await execAsync(composeCmd);
      console.log('✅ Docker compose stopped');
      this.isRunning = false;
    } catch (error) {
      throw new Error(`Failed to stop docker-compose: ${error}`);
    }
  }

  /**
   * Clean up: stop stack and remove volumes
   */
  async cleanup(): Promise<void> {
    if (this.config.skipStackManagement) {
      console.log('⏭️  Skipping docker-compose cleanup (using external stack)');
      this.isRunning = false;
      return;
    }

    if (!this.isRunning) {
      return;
    }

    console.log('🧹 Cleaning up docker-compose stack...');
    
    const composeCmd = `docker compose -f ${this.config.composeFile} -p ${this.config.projectName} down -v`;
    
    try {
      await execAsync(composeCmd);
      console.log('✅ Docker compose cleaned up');
      this.isRunning = false;
    } catch (error) {
      throw new Error(`Failed to cleanup docker-compose: ${error}`);
    }
  }

  /**
   * Wait for all services to be healthy
   */
  private async waitForHealth(): Promise<void> {
    console.log('⏳ Waiting for services to be healthy...');
    
    const startTime = Date.now();
    
    while (Date.now() - startTime < this.config.healthCheckTimeout) {
      const healthStatus = this.config.skipStackManagement
        ? await this.checkHealthViaHttp()
        : await this.checkHealth();
      const allHealthy = healthStatus.every(s => s.healthy);
      
      if (allHealthy) {
        console.log('✅ All services are healthy');
        return;
      }
      
      const unhealthy = healthStatus.filter(s => !s.healthy);
      console.log(`⏳ Waiting for: ${unhealthy.map(s => s.name).join(', ')}`);
      
      await this.sleep(this.config.healthCheckInterval);
    }
    
    throw new Error('Timeout waiting for services to be healthy');
  }

  /**
   * Check health via HTTP endpoints (for external stack)
   */
  private async checkHealthViaHttp(): Promise<ServiceHealth[]> {
    const results: ServiceHealth[] = [];

    // Check API health
    try {
      const apiResponse = await fetch(`${this.config.apiBaseUrl}/health`);
      results.push({
        name: 'bahia',
        healthy: apiResponse.ok,
        error: apiResponse.ok ? undefined : `HTTP ${apiResponse.status}`,
      });
    } catch (error) {
      results.push({ name: 'bahia', healthy: false, error: String(error) });
    }

    // Check web frontend
    try {
      const webResponse = await fetch(this.config.webBaseUrl);
      results.push({
        name: 'web',
        healthy: webResponse.ok,
        error: webResponse.ok ? undefined : `HTTP ${webResponse.status}`,
      });
    } catch (error) {
      results.push({ name: 'web', healthy: false, error: String(error) });
    }

    // Postgres health inferred from API health (API won't start without DB)
    const apiHealthy = results.find(r => r.name === 'bahia')?.healthy ?? false;
    results.push({
      name: 'postgres',
      healthy: apiHealthy,
      error: apiHealthy ? undefined : 'Inferred from API health',
    });

    return results;
  }

  /**
   * Check health status of all services
   */
  async checkHealth(): Promise<ServiceHealth[]> {
    const services = ['postgres', 'bahia', 'web'];
    const results: ServiceHealth[] = [];
    
    for (const service of services) {
      try {
        const { stdout } = await execAsync(
          `docker compose -f ${this.config.composeFile} -p ${this.config.projectName} ps --format json ${service}`
        );
        
        const lines = stdout.trim().split('\n').filter(l => l);
        if (lines.length === 0) {
          results.push({ name: service, healthy: false, error: 'Service not found' });
          continue;
        }
        
        const serviceInfo = JSON.parse(lines[0]);
        const isHealthy = serviceInfo.Health === 'healthy' || serviceInfo.State === 'running';
        
        results.push({
          name: service,
          healthy: isHealthy,
          error: isHealthy ? undefined : `State: ${serviceInfo.State}, Health: ${serviceInfo.Health}`,
        });
      } catch (error) {
        results.push({
          name: service,
          healthy: false,
          error: `Failed to check health: ${error}`,
        });
      }
    }
    
    return results;
  }

  /**
   * Get service logs
   */
  async getLogs(service?: string): Promise<string> {
    const serviceArg = service ? service : '';
    const { stdout } = await execAsync(
      `docker compose -f ${this.config.composeFile} -p ${this.config.projectName} logs ${serviceArg}`
    );
    return stdout;
  }

  /**
   * Get API base URL
   */
  getApiUrl(): string {
    return this.config.apiBaseUrl;
  }

  /**
   * Get Web UI base URL
   */
  getWebUrl(): string {
    return this.config.webBaseUrl;
  }

  /**
   * Get MCP JSON-RPC endpoint URL
   */
  getMcpUrl(): string {
    return process.env.BAHIA_E2E_MCP_URL ?? this.config.mcpServerUrl;
  }

  /**
   * Check if harness is running
   */
  isStackRunning(): boolean {
    return this.isRunning;
  }

  /**
   * Sleep helper
   */
  private sleep(ms: number): Promise<void> {
    return new Promise(resolve => setTimeout(resolve, ms));
  }
}
