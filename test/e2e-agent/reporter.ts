/**
 * Reporting utilities for E2E agent test runner
 */
import { writeFile } from 'node:fs/promises';
import type { ScenarioResult, TestStatus } from './types.js';

export interface RunSummary {
  total: number;
  passed: number;
  failed: number;
  skipped: number;
  error: number;
}

export interface RunReport {
  startedAt: string;
  endedAt: string;
  duration: number;
  aborted: boolean;
  summary: RunSummary;
  results: ScenarioResult[];
}

const COLOR = {
  reset: '\x1b[0m',
  red: '\x1b[31m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  cyan: '\x1b[36m',
  gray: '\x1b[90m',
} as const;

function colorize(text: string, color: keyof typeof COLOR): string {
  if (!process.stdout.isTTY) return text;
  return `${COLOR[color]}${text}${COLOR.reset}`;
}

export function statusIcon(status: TestStatus): string {
  switch (status) {
    case 'passed':
      return colorize('✅', 'green');
    case 'failed':
      return colorize('❌', 'red');
    case 'skipped':
      return colorize('⏭️', 'yellow');
    case 'error':
      return colorize('💥', 'red');
  }
}

export function buildSummary(results: ScenarioResult[]): RunSummary {
  return {
    total: results.length,
    passed: results.filter(r => r.status === 'passed').length,
    failed: results.filter(r => r.status === 'failed').length,
    skipped: results.filter(r => r.status === 'skipped').length,
    error: results.filter(r => r.status === 'error').length,
  };
}

export function printProgress(index: number, total: number, name: string): void {
  const prefix = colorize(`[${index}/${total}]`, 'cyan');
  console.log(`${prefix} Running: ${name}`);
}

export function printScenarioResult(result: ScenarioResult): void {
  const icon = statusIcon(result.status);
  console.log(`  ${icon} ${result.name} ${colorize(`(${result.duration}ms)`, 'gray')}`);

  for (const step of result.steps) {
    const stepIcon = statusIcon(step.status);
    console.log(`     ${stepIcon} ${step.name} ${colorize(`${step.duration}ms`, 'gray')}`);
    if (step.error) {
      console.log(`       ${colorize(step.error, 'red')}`);
    }
  }

  if (result.error) {
    console.log(`     ${colorize(result.error, 'red')}`);
  }
}

export function printSummary(report: RunReport): void {
  const { summary } = report;
  console.log('\n📊 Test Summary');
  console.log(`  Total: ${summary.total}`);
  console.log(`  Passed: ${colorize(String(summary.passed), 'green')}`);
  console.log(`  Failed: ${colorize(String(summary.failed), summary.failed > 0 ? 'red' : 'green')}`);
  console.log(`  Skipped: ${colorize(String(summary.skipped), 'yellow')}`);
  console.log(`  Errors: ${colorize(String(summary.error), summary.error > 0 ? 'red' : 'green')}`);
  console.log(`  Duration: ${report.duration}ms`);
  console.log(`  Aborted: ${report.aborted ? 'yes' : 'no'}`);
}

export function toJSON(report: RunReport): string {
  return JSON.stringify(report, null, 2);
}

export function toHTML(report: RunReport): string {
  const rows = report.results.map(result => {
    const stepList = result.steps
      .map(step => `<li><strong>${escape(step.status)}</strong> ${escape(step.name)} (${step.duration}ms)${step.error ? ` - ${escape(step.error)}` : ''}</li>`)
      .join('');

    return `<tr>
      <td>${escape(result.name)}</td>
      <td>${escape(result.status)}</td>
      <td>${result.duration}ms</td>
      <td>${result.error ? escape(result.error) : ''}</td>
      <td><ul>${stepList}</ul></td>
    </tr>`;
  }).join('');

  return `<!doctype html>
<html>
<head>
  <meta charset="utf-8" />
  <title>Bahia E2E Agent Report</title>
  <style>
    body { font-family: system-ui, sans-serif; margin: 20px; }
    table { border-collapse: collapse; width: 100%; }
    th, td { border: 1px solid #ddd; padding: 8px; vertical-align: top; }
    th { background: #f4f4f4; text-align: left; }
    .summary { margin-bottom: 20px; }
  </style>
</head>
<body>
  <h1>Bahia E2E Agent Report</h1>
  <div class="summary">
    <p><strong>Started:</strong> ${escape(report.startedAt)}</p>
    <p><strong>Ended:</strong> ${escape(report.endedAt)}</p>
    <p><strong>Duration:</strong> ${report.duration}ms</p>
    <p><strong>Total:</strong> ${report.summary.total}, <strong>Passed:</strong> ${report.summary.passed}, <strong>Failed:</strong> ${report.summary.failed}, <strong>Skipped:</strong> ${report.summary.skipped}, <strong>Errors:</strong> ${report.summary.error}</p>
  </div>
  <table>
    <thead>
      <tr><th>Scenario</th><th>Status</th><th>Duration</th><th>Error</th><th>Steps</th></tr>
    </thead>
    <tbody>${rows}</tbody>
  </table>
</body>
</html>`;
}

export async function writeHTMLReport(path: string, report: RunReport): Promise<void> {
  await writeFile(path, toHTML(report), 'utf8');
}

function escape(value: string): string {
  return value
    .replaceAll('&', '&amp;')
    .replaceAll('<', '&lt;')
    .replaceAll('>', '&gt;')
    .replaceAll('"', '&quot;')
    .replaceAll("'", '&#39;');
}
