import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

const emptyStateSource = readFileSync(
  resolve(process.cwd(), 'src/lib/components/EmptyState.svelte'),
  'utf8'
);

const errorStateSource = readFileSync(
  resolve(process.cwd(), 'src/lib/components/ErrorState.svelte'),
  'utf8'
);

const loadingStateSource = readFileSync(
  resolve(process.cwd(), 'src/lib/components/LoadingState.svelte'),
  'utf8'
);

const cardSource = readFileSync(
  resolve(process.cwd(), 'src/lib/components/Card.svelte'),
  'utf8'
);

describe('state components contract', () => {
  it('keeps EmptyState configurable for title/message/icon/actions', () => {
    expect(emptyStateSource).toContain("title = 'No data'");
    expect(emptyStateSource).toContain("message = ''");
    expect(emptyStateSource).toContain("icon = ''");
    expect(emptyStateSource).toContain('iconComponent = null');
    expect(emptyStateSource).toContain('EmptyIcon');
    expect(emptyStateSource).toContain('actionLabel');
    expect(emptyStateSource).toContain('{@render action?.()}');
  });

  it('keeps ErrorState configurable for message, details, and reset', () => {
    expect(errorStateSource).toContain("title = 'Error'");
    expect(errorStateSource).toContain("message = 'An error occurred.'");
    expect(errorStateSource).toContain('details');
    expect(errorStateSource).toContain('resetLabel');
    expect(errorStateSource).toContain('LoadingButton');
  });

  it('provides LoadingState with spinner and loading copy', () => {
    expect(loadingStateSource).toContain("title = 'Loading'");
    expect(loadingStateSource).toContain("message = 'Please wait while we load data.'");
    expect(loadingStateSource).toContain('showSpinner = true');
    expect(loadingStateSource).toContain('class="spinner"');
    expect(loadingStateSource).toContain('aria-busy="true"');
  });

  it('renders zero-valued Card metrics instead of hiding them behind truthy checks', () => {
    expect(cardSource).toContain("const hasValue = $derived(value !== '' && value !== null && value !== undefined);");
    expect(cardSource).toContain('{#if hasValue}');
  });
});
