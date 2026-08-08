/**
 * Shared types for E2E agent testing infrastructure
 */

/**
 * Test result status
 */
export type TestStatus = 'passed' | 'failed' | 'skipped' | 'error';

/**
 * Result of a single test step
 */
export interface TestStepResult {
  name: string;
  status: TestStatus;
  duration: number;
  error?: string;
  screenshot?: string;
}

/**
 * Result of a test scenario
 */
export interface TestResult {
  scenario: string;
  status: TestStatus;
  duration: number;
  steps: TestStepResult[];
  error?: string;
}

/**
 * Docker Compose service health status
 */
export interface ServiceHealth {
  name: string;
  healthy: boolean;
  error?: string;
}

/**
 * Test harness configuration
 */
export interface HarnessConfig {
  composeFile?: string;
  projectName?: string;
  healthCheckTimeout?: number;
  healthCheckInterval?: number;
  apiBaseUrl?: string;
  webBaseUrl?: string;
  mcpServerUrl?: string;
  /** If true, skip docker-compose management and use existing stack */
  skipStackManagement?: boolean;
}

/**
 * API response wrapper
 */
export interface APIResponse<T = unknown> {
  data?: T;
  error?: string;
  message?: string;
}

/**
 * Service entity (from bahia API)
 */
export interface Service {
  id: string;
  name: string;
  artifact_repo: string;
  repo_url?: string;
  runtime_type: 'docker' | 'compose' | 'kubernetes' | 'podman' | 'vm-firecracker' | 'vm-qemu';
  created_at: string;
  updated_at: string;
}

/**
 * Environment entity (from bahia API)
 */
export interface Environment {
  id: string;
  name: string;
  protected: boolean;
  deploy_strategy: 'replace' | 'blue_green' | 'canary';
  created_at: string;
  updated_at: string;
}

/**
 * MCP tool call request
 */
export interface MCPToolCall {
  name: string;
  arguments: Record<string, unknown>;
}

/**
 * MCP tool call result
 */
export interface MCPToolResult {
  content: Array<{
    type: string;
    text: string;
  }>;
  isError?: boolean;
}

/**
 * Driver capabilities
 */
export interface DriverCapabilities {
  canTakeScreenshots: boolean;
  canRecordVideo: boolean;
  canInspectDOM: boolean;
}

/**
 * Test scenario result
 */
export interface ScenarioResult {
  name: string;
  status: TestStatus;
  duration: number;
  steps: TestStepResult[];
  error?: string;
  metadata?: Record<string, unknown>;
}

/**
 * Scenario drivers collection
 */
export interface ScenarioDrivers {
  api: import('./drivers/api.js').BahiaAPIDriver;
  web: import('./drivers/playwright.js').PlaywrightDriver;
  mcp: import('./drivers/mcp.js').MCPDriver;
}

/**
 * Test scenario definition
 */
export interface Scenario {
  name: string;
  description: string;
  tags: string[];
  run(drivers: ScenarioDrivers): Promise<ScenarioResult>;
}
