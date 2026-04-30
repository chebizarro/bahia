/**
 * Failure diagnostics for E2E agent test runs
 */
import type { RunReport } from './reporter.js';
import type { ScenarioResult } from './types.js';

export type FailurePattern =
  | 'api_error'
  | 'ui_element_not_found'
  | 'timeout'
  | 'assertion_failure'
  | 'mcp_error'
  | 'unknown';

export interface FailureDiagnostic {
  scenario: string;
  pattern: FailurePattern;
  message: string;
  sourceHints: string[];
}

export interface DiagnosticReport {
  failedCount: number;
  diagnostics: FailureDiagnostic[];
  groupedByPattern: Record<FailurePattern, number>;
}

export function analyzeFailures(report: RunReport): DiagnosticReport {
  const failedScenarios = report.results.filter(r => r.status === 'failed' || r.status === 'error');
  const diagnostics = failedScenarios.map(buildScenarioDiagnostic);

  const groupedByPattern: Record<FailurePattern, number> = {
    api_error: 0,
    ui_element_not_found: 0,
    timeout: 0,
    assertion_failure: 0,
    mcp_error: 0,
    unknown: 0,
  };

  for (const diag of diagnostics) {
    groupedByPattern[diag.pattern] += 1;
  }

  return {
    failedCount: failedScenarios.length,
    diagnostics,
    groupedByPattern,
  };
}

export function formatDiagnosticReport(report: DiagnosticReport): string {
  const lines: string[] = [];
  lines.push('🩺 Failure Diagnostics');
  lines.push(`  Failed scenarios: ${report.failedCount}`);
  lines.push('  Pattern counts:');

  for (const [pattern, count] of Object.entries(report.groupedByPattern)) {
    if (count > 0) lines.push(`    - ${pattern}: ${count}`);
  }

  if (report.diagnostics.length > 0) {
    lines.push('  Details:');
    for (const diag of report.diagnostics) {
      lines.push(`    - [${diag.pattern}] ${diag.scenario}`);
      lines.push(`      ${diag.message}`);
      if (diag.sourceHints.length > 0) {
        lines.push(`      Hints: ${diag.sourceHints.join(', ')}`);
      }
    }
  }

  return lines.join('\n');
}

function buildScenarioDiagnostic(result: ScenarioResult): FailureDiagnostic {
  const combined = [result.error, ...result.steps.map(s => s.error)]
    .filter(Boolean)
    .join('\n')
    .toLowerCase();

  const pattern = classifyFailurePattern(combined);

  return {
    scenario: result.name,
    pattern,
    message: result.error ?? firstStepError(result) ?? 'No explicit error message found',
    sourceHints: inferSourceHints(result.name, pattern),
  };
}

function firstStepError(result: ScenarioResult): string | undefined {
  return result.steps.find(step => step.error)?.error;
}

function classifyFailurePattern(text: string): FailurePattern {
  if (!text) return 'unknown';
  if (text.includes('status') || text.includes('response') || text.includes('fetch') || text.includes('http')) {
    return 'api_error';
  }
  if (text.includes('locator') || text.includes('element') || text.includes('selector') || text.includes('not found')) {
    return 'ui_element_not_found';
  }
  if (text.includes('timeout') || text.includes('timed out')) {
    return 'timeout';
  }
  if (text.includes('assert') || text.includes('expected') || text.includes('mismatch')) {
    return 'assertion_failure';
  }
  if (text.includes('mcp') || text.includes('tool') || text.includes('rpc')) {
    return 'mcp_error';
  }

  return 'unknown';
}

function inferSourceHints(scenarioName: string, pattern: FailurePattern): string[] {
  const lowerName = scenarioName.toLowerCase();
  const hints = new Set<string>();

  if (lowerName.includes('service')) hints.add('test/e2e-agent/scenarios/services.ts');
  if (lowerName.includes('environment')) hints.add('test/e2e-agent/scenarios/environments.ts');
  if (lowerName.includes('deployment')) hints.add('test/e2e-agent/scenarios/deployments.ts');
  if (lowerName.includes('policy')) hints.add('test/e2e-agent/scenarios/policies.ts');
  if (lowerName.includes('worker')) hints.add('test/e2e-agent/scenarios/workers.ts');
  if (lowerName.includes('secret')) hints.add('test/e2e-agent/scenarios/secrets.ts');
  if (lowerName.includes('event')) hints.add('test/e2e-agent/scenarios/events.ts');

  switch (pattern) {
    case 'api_error':
      hints.add('test/e2e-agent/drivers/api.ts');
      break;
    case 'ui_element_not_found':
    case 'timeout':
      hints.add('test/e2e-agent/drivers/playwright.ts');
      break;
    case 'mcp_error':
      hints.add('test/e2e-agent/drivers/mcp.ts');
      break;
    default:
      break;
  }

  return Array.from(hints);
}
