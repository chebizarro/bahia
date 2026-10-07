/**
 * CLI entrypoint for E2E agent runner
 */
import { writeFile } from 'node:fs/promises';
import readline from 'node:readline/promises';
import { stdin as input, stdout as output } from 'node:process';
import { runScenarios, type RunnerOptions } from './runner.js';
import { toJSON, writeHTMLReport } from './reporter.js';
import { runSelfHealingLoop } from './healing-loop.js';
import type { FixProposal } from './fixer.js';

interface ParsedArgs {
  options: RunnerOptions;
  json: boolean;
  htmlPath?: string;
  heal: boolean;
  maxIterations?: number;
  approveFixes: boolean;
}

function parseArgs(argv: string[]): ParsedArgs {
  const options: RunnerOptions = {};
  const scenarioNames: string[] = [];
  const tags: string[] = [];
  let json = false;
  let htmlPath: string | undefined;
  let heal = false;
  let maxIterations: number | undefined;
  let approveFixes = false;

  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];

    switch (arg) {
      case '--all':
        options.all = true;
        break;
      case '--tags': {
        const value = argv[++i];
        if (!value) throw new Error('--tags requires a value');
        tags.push(...value.split(',').map(t => t.trim()).filter(Boolean));
        break;
      }
      case '--scenario': {
        const value = argv[++i];
        if (!value) throw new Error('--scenario requires a value');
        scenarioNames.push(value);
        break;
      }
      case '--json':
        json = true;
        break;
      case '--html': {
        const value = argv[++i];
        if (!value) throw new Error('--html requires a file path');
        htmlPath = value;
        break;
      }
      case '--continue-on-failure':
        options.continueOnFailure = true;
        break;
      case '--headed':
        options.headless = false;
        break;
      case '--skip-mcp':
        options.skipMcp = true;
        break;
      case '--mcp-url': {
        const value = argv[++i];
        if (!value) throw new Error('--mcp-url requires a value');
        options.mcpServerUrl = value;
        break;
      }
      case '--heal':
        heal = true;
        break;
      case '--max-iterations': {
        const value = argv[++i];
        if (!value) throw new Error('--max-iterations requires a number');
        const parsed = Number.parseInt(value, 10);
        if (!Number.isFinite(parsed) || parsed <= 0) {
          throw new Error('--max-iterations must be a positive integer');
        }
        maxIterations = parsed;
        break;
      }
      case '--approve-fixes':
        approveFixes = true;
        break;
      case '--use-existing-stack':
        options.skipStackManagement = true;
        break;
      case '--help':
        printHelp();
        process.exit(0);
      default:
        throw new Error(`Unknown argument: ${arg}`);
    }
  }

  if (tags.length > 0) options.tags = tags;
  if (scenarioNames.length > 0) options.scenarioNames = scenarioNames;

  return { options, json, htmlPath, heal, maxIterations, approveFixes };
}

function printHelp(): void {
  console.log(`Bahia E2E Agent Runner

Usage:
  tsx cli.ts --all
  tsx cli.ts --tags smoke
  tsx cli.ts --scenario "Service CRUD"

Options:
  --all                     Run all scenarios
  --tags <tag[,tag2]>       Run scenarios matching all tags
  --scenario <name>         Run specific scenario (repeatable)
  --json                    Print machine-readable JSON report
  --html <path>             Write optional HTML report
  --continue-on-failure     Continue after scenario failures
  --headed                  Run browser in headed mode
  --skip-mcp                Explicitly skip MCP driver initialization
  --mcp-url <url>           MCP JSON-RPC URL (default: BAHIA_E2E_MCP_URL or http://localhost:8080/mcp)
  --heal                    Enable self-healing loop
  --max-iterations <n>      Max healing attempts (default: 3)
  --approve-fixes           Prompt to approve applying each proposed fix
  --help                    Show this help
`);
}

async function askForFixApproval(proposal: FixProposal): Promise<boolean> {
  const rl = readline.createInterface({ input, output });

  try {
    console.log(`\n📝 Proposed fix: ${proposal.description}`);
    console.log(`   File: ${proposal.targetFile}`);
    console.log(`   Reason: ${proposal.reason}`);
    const answer = await rl.question('Apply this fix? [y/N]: ');
    return answer.trim().toLowerCase() === 'y';
  } finally {
    rl.close();
  }
}

async function main(): Promise<void> {
  try {
    const parsed = parseArgs(process.argv.slice(2));

    if (parsed.heal) {
      const healing = await runSelfHealingLoop({
        runner: parsed.options,
        maxIterations: parsed.maxIterations,
        requireApproval: true,
        approveFix: parsed.approveFixes ? askForFixApproval : undefined,
      });

      const finalIteration = healing.iterations.at(-1);
      if (!finalIteration) {
        throw new Error('Self-healing loop produced no iterations');
      }

      const report = finalIteration.report;
      console.log(`\nSelf-healing outcome: ${healing.status}`);

      if (parsed.json) {
        const jsonReport = toJSON(report);
        console.log(`\n${jsonReport}`);
        await writeFile('e2e-agent-report.json', jsonReport, 'utf8');
      }

      if (parsed.htmlPath) {
        await writeHTMLReport(parsed.htmlPath, report);
        console.log(`\n📄 HTML report written: ${parsed.htmlPath}`);
      }

      process.exit(healing.success ? 0 : 1);
    }

    const report = await runScenarios(parsed.options);

    if (parsed.json) {
      const jsonReport = toJSON(report);
      console.log(`\n${jsonReport}`);
      await writeFile('e2e-agent-report.json', jsonReport, 'utf8');
    }

    if (parsed.htmlPath) {
      await writeHTMLReport(parsed.htmlPath, report);
      console.log(`\n📄 HTML report written: ${parsed.htmlPath}`);
    }

    const failed = report.summary.failed + report.summary.error;
    process.exit(failed > 0 ? 1 : 0);
  } catch (error) {
    console.error(`❌ ${String(error)}`);
    process.exit(1);
  }
}

main();
