import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const componentSource = readFileSync(
  resolve(process.cwd(), 'src/lib/components/LoadingButton.svelte'),
  'utf8'
);

describe('LoadingButton.svelte contract', () => {
  it('shows spinner while loading', () => {
    expect(componentSource).toContain('{#if loading}');
    expect(componentSource).toContain('class="spinner"');
  });

  it('disables button during loading/action', () => {
    expect(componentSource).toContain('disabled={disabled || loading}');
  });

  it('supports primary, secondary, and danger variants', () => {
    expect(componentSource).toContain("variant = 'primary'");
    expect(componentSource).toContain('.btn.primary');
    expect(componentSource).toContain('.btn.secondary');
    expect(componentSource).toContain('.btn.danger');
  });
});
