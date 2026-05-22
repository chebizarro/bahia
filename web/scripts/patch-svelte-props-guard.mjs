import { readdirSync, readFileSync, writeFileSync, statSync } from 'node:fs';
import { join } from 'node:path';

const root = new URL('../build/_app/immutable/chunks/', import.meta.url);
const dir = root.pathname;

function walk(path) {
  const out = [];
  for (const name of readdirSync(path)) {
    const full = join(path, name);
    const stat = statSync(full);
    if (stat.isDirectory()) out.push(...walk(full));
    else if (name.endsWith('.js')) out.push(full);
  }
  return out;
}

let patched = 0;
for (const file of walk(dir)) {
  const text = readFileSync(file, 'utf8');
  if (!text.includes('$$legacy') || !text.includes(' in e||') || text.includes('e=e??{};')) continue;
  const next = text.replace(/function\s+([A-Za-z_$][\w$]*)\(e,a,i,r\)\{/, (match) => `${match}e=e??{};`);
  if (next !== text) {
    writeFileSync(file, next);
    patched += 1;
  }
}

console.log(`patched ${patched} chunk(s)`);
