/**
 * Fix proposal and application utilities for E2E self-healing
 */
import { readFile, writeFile } from 'node:fs/promises';
import path from 'node:path';
import type { DiagnosticReport, FailureDiagnostic } from './diagnostics.js';

export interface FixProposal {
  id: string;
  description: string;
  targetFile: string;
  search: string;
  replace: string;
  reason: string;
}

export interface AppliedFixRecord {
  proposal: FixProposal;
  appliedAt: string;
  backupPath: string;
}

export class Fixer {
  private readonly history: AppliedFixRecord[] = [];

  constructor(private readonly repoRoot: string = process.cwd()) {}

  proposeFixes(report: DiagnosticReport): FixProposal[] {
    const proposals: FixProposal[] = [];
    const seen = new Set<string>();

    for (const diagnostic of report.diagnostics) {
      const proposal = this.proposeFromDiagnostic(diagnostic);
      if (proposal && !seen.has(proposal.id)) {
        proposals.push(proposal);
        seen.add(proposal.id);
      }
    }

    return proposals;
  }

  async applyFix(proposal: FixProposal): Promise<AppliedFixRecord> {
    const absolutePath = path.resolve(this.repoRoot, proposal.targetFile);
    const original = await readFile(absolutePath, 'utf8');

    if (!original.includes(proposal.search)) {
      throw new Error(`Search snippet not found in ${proposal.targetFile} for fix ${proposal.id}`);
    }

    const updated = original.replace(proposal.search, proposal.replace);
    const backupPath = `${absolutePath}.heal-backup-${Date.now()}`;

    await writeFile(backupPath, original, 'utf8');
    await writeFile(absolutePath, updated, 'utf8');

    const record: AppliedFixRecord = {
      proposal,
      appliedAt: new Date().toISOString(),
      backupPath,
    };

    this.history.push(record);
    return record;
  }

  async rollbackLastFix(): Promise<void> {
    const record = this.history.pop();
    if (!record) {
      return;
    }

    const absolutePath = path.resolve(this.repoRoot, record.proposal.targetFile);
    const original = await readFile(record.backupPath, 'utf8');
    await writeFile(absolutePath, original, 'utf8');
  }

  getFixHistory(): AppliedFixRecord[] {
    return [...this.history];
  }

  private proposeFromDiagnostic(diagnostic: FailureDiagnostic): FixProposal | null {
    const hint = diagnostic.sourceHints[0];
    if (!hint) {
      return null;
    }

    switch (diagnostic.pattern) {
      case 'timeout':
        return {
          id: 'timeout-playwright-waitforselector',
          description: `Increase default selector timeout after ${diagnostic.scenario}`,
          targetFile: 'test/e2e-agent/drivers/playwright.ts',
          search: 'async waitForSelector(selector: string, timeout = 5000): Promise<void> {',
          replace: 'async waitForSelector(selector: string, timeout = 10000): Promise<void> {',
          reason: diagnostic.message,
        };
      case 'ui_element_not_found':
        return {
          id: 'ui-playwright-click-wait',
          description: `Add wait before click after ${diagnostic.scenario}`,
          targetFile: 'test/e2e-agent/drivers/playwright.ts',
          search: "  async click(selector: string): Promise<void> {\n    const page = this.getPage();\n    await page.click(selector);\n  }",
          replace: "  async click(selector: string): Promise<void> {\n    const page = this.getPage();\n    await page.waitForSelector(selector, { timeout: 5000, state: 'visible' });\n    await page.click(selector);\n  }",
          reason: diagnostic.message,
        };
      case 'api_error':
        return {
          id: 'api-driver-error-context',
          description: `Add richer API error context for ${diagnostic.scenario}`,
          targetFile: 'test/e2e-agent/drivers/api.ts',
          search: 'throw new Error(`API request failed (${response.status}): ${errorText}`);',
          replace: 'throw new Error(`API request failed (${response.status}) ${options.method ?? \"GET\"} ${path}: ${errorText}`);',
          reason: diagnostic.message,
        };
      default:
        return null;
    }
  }
}

function slug(value: string): string {
  return value.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-|-$/g, '');
}
