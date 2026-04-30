/**
 * Self-healing orchestration for E2E agent runs
 */
import { analyzeFailures, formatDiagnosticReport } from './diagnostics.js';
import { Fixer, type AppliedFixRecord, type FixProposal } from './fixer.js';
import { runScenarios, type RunnerOptions } from './runner.js';
import type { RunReport } from './reporter.js';

export interface HealingOptions {
  runner: RunnerOptions;
  maxIterations?: number;
  requireApproval?: boolean;
  approveFix?: (proposal: FixProposal) => Promise<boolean>;
}

export interface HealingIteration {
  iteration: number;
  report: RunReport;
  diagnosticsText?: string;
  appliedFixes: AppliedFixRecord[];
}

export interface HealingRunResult {
  success: boolean;
  iterations: HealingIteration[];
}

export async function runSelfHealingLoop(options: HealingOptions): Promise<HealingRunResult> {
  const maxIterations = Math.max(1, options.maxIterations ?? 3);
  const fixer = new Fixer();
  const iterations: HealingIteration[] = [];

  for (let iteration = 1; iteration <= maxIterations; iteration += 1) {
    const report = await runScenarios({
      ...options.runner,
      continueOnFailure: true,
    });

    const failed = report.summary.failed + report.summary.error;
    if (failed === 0) {
      iterations.push({ iteration, report, appliedFixes: [] });
      return { success: true, iterations };
    }

    const diagnostics = analyzeFailures(report);
    const diagnosticsText = formatDiagnosticReport(diagnostics);
    console.log(`\n${diagnosticsText}`);

    const proposals = fixer.proposeFixes(diagnostics);
    if (proposals.length === 0) {
      iterations.push({ iteration, report, diagnosticsText, appliedFixes: [] });
      return { success: false, iterations };
    }

    const appliedFixes: AppliedFixRecord[] = [];

    for (const proposal of proposals) {
      if (options.requireApproval) {
        const approved = await (options.approveFix?.(proposal) ?? Promise.resolve(false));
        if (!approved) {
          continue;
        }
      }

      try {
        console.log(`🛠️ Applying fix: ${proposal.description}`);
        const record = await fixer.applyFix(proposal);
        appliedFixes.push(record);
      } catch (error) {
        console.warn(`⚠️ Failed to apply fix ${proposal.id}: ${String(error)}`);
      }
    }

    if (appliedFixes.length === 0) {
      iterations.push({ iteration, report, diagnosticsText, appliedFixes });
      return { success: false, iterations };
    }

    iterations.push({ iteration, report, diagnosticsText, appliedFixes });
  }

  return { success: false, iterations };
}
