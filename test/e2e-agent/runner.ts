/**
 * Main scenario runner for E2E agent tests
 */
import { TestHarness } from './harness.js';
import { BahiaAPIDriver } from './drivers/api.js';
import { PlaywrightDriver } from './drivers/playwright.js';
import { MCPDriver } from './drivers/mcp.js';
import { allScenarios, getScenarioByName, getScenariosByTags } from './scenarios/index.js';
import type { Scenario, ScenarioDrivers, ScenarioResult } from './types.js';
import {
  buildSummary,
  printProgress,
  printScenarioResult,
  printSummary,
  type RunReport,
} from './reporter.js';

export interface RunnerOptions {
  all?: boolean;
  tags?: string[];
  scenarioNames?: string[];
  continueOnFailure?: boolean;
  headless?: boolean;
  skipMcp?: boolean;
  mcpCommand?: string;
  mcpArgs?: string[];
  skipStackManagement?: boolean;
}

export function selectScenarios(options: RunnerOptions): Scenario[] {
  if (options.all || (!options.tags?.length && !options.scenarioNames?.length)) {
    return allScenarios;
  }

  const selected = new Map<string, Scenario>();

  if (options.tags?.length) {
    for (const scenario of getScenariosByTags(options.tags)) {
      selected.set(scenario.name, scenario);
    }
  }

  if (options.scenarioNames?.length) {
    for (const name of options.scenarioNames) {
      const scenario = getScenarioByName(name);
      if (!scenario) {
        throw new Error(`Unknown scenario: ${name}`);
      }
      selected.set(scenario.name, scenario);
    }
  }

  return Array.from(selected.values());
}

export async function runScenarios(options: RunnerOptions = {}): Promise<RunReport> {
  const startedAt = new Date();
  const startTime = Date.now();
  const harness = new TestHarness({ skipStackManagement: options.skipStackManagement });
  const scenarios = selectScenarios(options);

  if (scenarios.length === 0) {
    throw new Error('No scenarios selected');
  }

  const drivers: ScenarioDrivers = {
    api: new BahiaAPIDriver(harness.getApiUrl()),
    web: new PlaywrightDriver(harness.getWebUrl()),
    mcp: new MCPDriver(),
  };

  const results: ScenarioResult[] = [];
  let aborted = false;

  try {
    await harness.start();
    await drivers.web.launch({ headless: options.headless ?? true });

    if (!options.skipMcp) {
      const mcpCommand = options.mcpCommand ?? 'bahia';
      const mcpArgs = options.mcpArgs ?? ['mcp'];
      try {
        await drivers.mcp.connect({ command: mcpCommand, args: mcpArgs });
      } catch (error) {
        console.warn(`⚠️ MCP connection failed: ${String(error)}`);
      }
    }

    for (let i = 0; i < scenarios.length; i += 1) {
      const scenario = scenarios[i];
      printProgress(i + 1, scenarios.length, scenario.name);

      let result: ScenarioResult;
      try {
        result = await scenario.run(drivers);
      } catch (error) {
        result = {
          name: scenario.name,
          status: 'error',
          duration: 0,
          steps: [],
          error: `Unhandled exception: ${String(error)}`,
        };
      }

      results.push(result);
      printScenarioResult(result);

      if (!options.continueOnFailure && (result.status === 'failed' || result.status === 'error')) {
        aborted = true;
        break;
      }
    }
  } finally {
    try {
      if (drivers.mcp.isConnected()) {
        await drivers.mcp.disconnect();
      }
    } catch (error) {
      console.warn(`⚠️ MCP disconnect failed: ${String(error)}`);
    }

    try {
      await drivers.web.close();
    } catch (error) {
      console.warn(`⚠️ Browser close failed: ${String(error)}`);
    }

    try {
      await harness.cleanup();
    } catch (error) {
      console.warn(`⚠️ Harness cleanup failed: ${String(error)}`);
    }
  }

  const report: RunReport = {
    startedAt: startedAt.toISOString(),
    endedAt: new Date().toISOString(),
    duration: Date.now() - startTime,
    aborted,
    summary: buildSummary(results),
    results,
  };

  printSummary(report);
  return report;
}
