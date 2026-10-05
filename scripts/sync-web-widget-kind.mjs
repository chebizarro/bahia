// The frontend kind catalog has a Go drift test but no full-file generator.
// Generate the NIP-CAS-0009 addition from the canonical Go declaration.
import { readFileSync, writeFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const root = new URL('../', import.meta.url);
const go = readFileSync(new URL('internal/kinds/kinds.go', root), 'utf8');
const match = go.match(/^\s*DashboardWidget\s*=\s*(\d+)\b/m);
if (!match) throw new Error('internal/kinds.DashboardWidget must be a numeric kind');

const path = fileURLToPath(new URL('web/src/lib/nostr/kinds.gen.js', root));
const source = readFileSync(path, 'utf8');
const declaration = `export const DASHBOARD_WIDGET = ${match[1]};`;
const next = source.replace(/^export const DASHBOARD_WIDGET = \d+;\n/m, '');
const anchor = /^export const ASSISTANT_TRANSCRIPT = \d+;\n/m;
if (!anchor.test(next)) throw new Error('Missing frontend kind catalog anchor');
const generated = next.replace(anchor, (line) => line + declaration + '\n');
if (process.argv.includes('--check')) {
  if (generated !== source) throw new Error('Run node scripts/sync-web-widget-kind.mjs');
} else if (generated !== source) {
  writeFileSync(path, generated);
}
