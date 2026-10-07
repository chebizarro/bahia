/**
 * Self-healing orchestration for E2E agent runs
 */
import { mkdir, writeFile } from 'node:fs/promises';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
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
  proposedFixes: FixProposal[];
  proposalArtifactPath?: string;
  appliedFixes: AppliedFixRecord[];
}

export interface HealingRunResult {
  status: 'success' | 'failed' | 'healed';
  success: boolean;
  iterations: HealingIteration[];
}

const artifactDirectory = path.join(
  path.dirname(fileURLToPath(import.meta.url)),
  'test-results',
  'healing',
);

async function writeProposalArtifact(iteration: number, proposals: FixProposal[]): Promise<string> {
  await mkdir(artifactDirectory, { recursive: true });
  const artifactPath = path.join(artifactDirectory, `iteration-${iteration}-proposed-fixes.json`);
  await writeFile(
    artifactPath,
    `${JSON.stringify({ iteration, proposedFixes: proposals }, null, 2)}\n`,
    'utf8',
  );
  return artifactPath;
}

export async function runSelfHealingLoop(options: HealingOptions): Promise<HealingRunResult> {
  const maxIterations = Math.max(1, options.maxIterations ?? 3);
  const requireApproval = options.requireApproval ?? true;
  const fixer = new Fixer();
  const iterations: HealingIteration[] = [];

  for (let iteration = 1; iteration <= maxIterations; iteration += 1) {
    const report = await runScenarios({
      ...options.runner,
      continueOnFailure: true,
    });

    const failed = report.summary.failed + report.summary.error;
    if (failed === 0) {
      iterations.push({ iteration, report, proposedFixes: [], appliedFixes: [] });
      const healed = iterations.some(result => result.appliedFixes.length > 0);
      return {
        status: healed ? 'healed' : 'success',
        success: !healed,
        iterations,
      };
    }

    const diagnostics = analyzeFailures(report);
    const diagnosticsText = formatDiagnosticReport(diagnostics);
    console.log(`\n${diagnosticsText}`);

    const proposals = fixer.proposeFixes(diagnostics);
    if (proposals.length === 0) {
      iterations.push({ iteration, report, diagnosticsText, proposedFixes: [], appliedFixes: [] });
      return { status: 'failed', success: false, iterations };
    }

    const proposalArtifactPath = await writeProposalArtifact(iteration, proposals);
    console.log(`📝 Proposed fixes artifact: ${proposalArtifactPath}`);
    const appliedFixes: AppliedFixRecord[] = [];

    for (const proposal of proposals) {
      if (requireApproval) {
        const approved = await (options.approveFix?.(proposal) ?? Promise.resolve(false));
        if (!approved) {
          continue;
        }
      }

      try {
        console.log(`🛠️ Applying approved fix: ${proposal.description}`);
        const record = await fixer.applyFix(proposal);
        appliedFixes.push(record);
      } catch (error) {
        console.warn(`⚠️ Failed to apply fix ${proposal.id}: ${String(error)}`);
      }
    }

    iterations.push({
      iteration,
      report,
      diagnosticsText,
      proposedFixes: proposals,
      proposalArtifactPath,
      appliedFixes,
    });

    if (appliedFixes.length === 0) {
      return { status: 'failed', success: false, iterations };
    }
  }

  return { status: 'failed', success: false, iterations };
}
