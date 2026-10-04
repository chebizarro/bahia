import { describe, expect, it } from 'vitest';
import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';

function files(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap(entry => {
    const path = join(dir, entry.name);
    return entry.isDirectory() ? files(path) : [path];
  });
}

describe('acceptance 16: no REST client imports', () => {
  it('has no lib/api/client reference anywhere in web/src', () => {
    const src = join(import.meta.dirname, '..', '..', 'src');
    expect(files(src).filter(path => /\.(js|ts|svelte)$/.test(path))
      .filter(path => readFileSync(path, 'utf8').includes('lib/api/client'))).toEqual([]);
  });
});
