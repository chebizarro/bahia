import { describe, it, expect } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join, relative, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const webRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const routesRoot = join(webRoot, 'src', 'routes');

function routeFiles(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return routeFiles(path);
    return /\.(svelte|js|ts)$/.test(entry.name) ? [path] : [];
  });
}

describe('route error surfaces', () => {
  it('does not leave route failures visible only in the browser console', () => {
    const offenders = routeFiles(routesRoot).flatMap((file) => {
      const source = readFileSync(file, 'utf8');
      return [...source.matchAll(/\bconsole\.(?:error|warn)\s*\(/g)].map((match) =>
        `${relative(webRoot, file)}:${source.slice(0, match.index).split('\n').length}`);
    });
    expect(offenders).toEqual([]);
  });
});
