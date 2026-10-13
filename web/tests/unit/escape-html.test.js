import { readdirSync, readFileSync } from 'node:fs';
import { join, relative } from 'node:path';
import { describe, expect, it } from 'vitest';
import { escapeHtml } from '../../src/lib/escape-html.js';

const src = join(process.cwd(), 'src');

function definitions(dir) {
  return readdirSync(dir, { withFileTypes: true }).flatMap((entry) => {
    const path = join(dir, entry.name);
    if (entry.isDirectory()) return definitions(path);
    if (!/\.(?:js|ts|svelte)$/.test(entry.name)) return [];
    const source = readFileSync(path, 'utf8');
    const pattern = /\b(?:function\s+escapeHtml\s*\(|(?:const|let|var)\s+escapeHtml\s*=)/g;
    return [...source.matchAll(pattern)].map(() => relative(src, path));
  });
}

describe('shared HTML escaping', () => {
  it('escapes text inserted into generated HTML', () => {
    expect(escapeHtml('&<>"\'')).toBe('&amp;&lt;&gt;&quot;&#39;');
    expect(escapeHtml(null)).toBe('');
  });

  it('has exactly one definition across web/src', () => {
    expect(definitions(src)).toEqual(['lib/escape-html.js']);
  });
});
