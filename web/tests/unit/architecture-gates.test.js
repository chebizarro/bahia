// Architecture ratchet for web stores (bahia-irsry.8, audit "Preventive
// Measures"). Stores keep state topped up from long-lived relay
// subscriptions; they must not poll with setInterval or reach the daemon's
// REST client ($lib/api/client.js). Existing violations are recorded in
// architecture-gates.baseline.json and may only shrink.
//
// Regenerate the baseline (Go and web) from the repo root with:
//   make arch-baseline
import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync, writeFileSync, existsSync } from 'node:fs';
import { join, relative, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const here = dirname(fileURLToPath(import.meta.url));
const webRoot = join(here, '..', '..');
const storesDir = join(webRoot, 'src', 'lib', 'stores');
const baselinePath = join(here, 'architecture-gates.baseline.json');
const updateBaseline = process.env.ARCHTEST_UPDATE_BASELINE === '1';

const API_CLIENT_IMPORT = /(?:from\s*|import\s*\(\s*)['"]([^'"]*\/api\/client(?:\.js)?)['"]/g;
const SET_INTERVAL = /\bsetInterval\s*\(/g;

function sourceFiles(dir) {
  const out = [];
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) out.push(...sourceFiles(path));
    else if (/\.(js|ts|svelte)$/.test(entry.name)) out.push(path);
  }
  return out.sort();
}

// Comments may mention the banned APIs; only code counts.
function stripComments(source) {
  return source.replace(/\/\*[\s\S]*?\*\//g, '').replace(/(^|[^:'"`])\/\/.*$/gm, '$1');
}

function isApiClientSpecifier(specifier) {
  return specifier === '$lib/api/client.js' || specifier === '$lib/api/client' || /(^|\/)api\/client(\.js)?$/.test(specifier);
}

function scanStores() {
  const found = {};
  for (const file of sourceFiles(storesDir)) {
    const rel = relative(webRoot, file).split('\\').join('/');
    const code = stripComments(readFileSync(file, 'utf8'));
    const intervals = code.match(SET_INTERVAL)?.length ?? 0;
    if (intervals > 0) found[`${rel} setInterval`] = intervals;
    const apiImports = [...code.matchAll(API_CLIENT_IMPORT)].filter((m) => isApiClientSpecifier(m[1])).length;
    if (apiImports > 0) found[`${rel} import:api/client.js`] = apiImports;
  }
  return found;
}

function readBaseline() {
  if (!existsSync(baselinePath)) return {};
  return JSON.parse(readFileSync(baselinePath, 'utf8')).violations ?? {};
}

describe('web architecture gates (bahia-irsry.8)', () => {
  it('stores gain no new setInterval polling or $lib/api/client.js imports', () => {
    const current = scanStores();
    if (updateBaseline) {
      const previous = readBaseline();
      const keys = [...new Set([...Object.keys(previous), ...Object.keys(current)])].sort();
      const added = keys.filter((key) => (current[key] ?? 0) > (previous[key] ?? 0))
        .map((key) => `  + ${key} (${previous[key] ?? 0} -> ${current[key] ?? 0})`);
      const removed = keys.filter((key) => (current[key] ?? 0) < (previous[key] ?? 0))
        .map((key) => `  - ${key} (${previous[key] ?? 0} -> ${current[key] ?? 0})`);
      process.stdout.write(`${[`BASELINE SUMMARY web-stores: ${added.length} added/grown, ${removed.length} removed/shrunk`, ...added, ...removed].join('\n')}\n`);
      const sorted = Object.fromEntries(Object.entries(current).sort(([a], [b]) => a.localeCompare(b)));
      writeFileSync(baselinePath, `${JSON.stringify({
        comment: 'Pre-existing violations only; entries may shrink, never grow. Regenerate with: make arch-baseline',
        violations: sorted
      }, null, 2)}\n`);
      return;
    }
    const baseline = readBaseline();
    const regressions = Object.entries(current)
      .filter(([key, count]) => count > (baseline[key] ?? 0))
      .map(([key, count]) => `${key}: ${count} found, baseline allows ${baseline[key] ?? 0}`);
    expect(regressions, 'stores must subscribe to relay state, not poll or call the daemon REST client. Regenerate after removing violations with: make arch-baseline').toEqual([]);
  });
});
