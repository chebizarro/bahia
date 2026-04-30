/**
 * Test harness for launching and managing the Bahia docker-compose stack
 */
import { exec } from 'child_process';
import { promisify } from 'util';
import type { HarnessConfig, ServiceHealth } from './types.js';

const execAsync = promisify(exec);

/**
 * Default configuration
 */
const DEFAULT_CONFIG: Required<HarnessConfig> = {
  composeFile: '../../docker-compose.yml',
  projectName: 'bahia-e2e-test',
  healthCheckTimeout: 60000, // 60 seconds
  healthCheckInterval: 2000, // 2 seconds
  apiBaseUrl: 'http://localhost:8080',
  webBaseUrl: 'http://localhost:3000',
  mcpServerUrl: 'stdio://bahia-mcp', // placeholder for MCP server connection
  skipStackManagement: false, // if true, use existing stack instead of managing docker-compose
};

/**
 * TestHarness manages the docker-compose stack lifecycle
 */
export class TestHarness {
  private config: Required<HarnessConfig>;
  private isRunning = false;

  constructor(config: Partial<HarnessConfig> = {}) {
    this.config = { ...DEFAULT_CONFIG, ...config };
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
