import { describe, it, expect } from 'vitest';
import { render } from 'svelte/server';
import LoadingButton from '../../src/lib/components/LoadingButton.svelte';

describe('LoadingButton', () => {
  it('shows spinner while loading', () => {
    const { body } = render(LoadingButton, { props: { loading: true } });

    expect(body).toContain('class="spinner"');
    expect(body).toContain('class="btn primary loading"');
  });

  it('is disabled while loading (and when explicitly disabled)', () => {
    const loading = render(LoadingButton, { props: { loading: true } }).body;
    const disabled = render(LoadingButton, { props: { disabled: true } }).body;

    expect(loading).toContain('disabled');
    expect(disabled).toContain('disabled');
  });

  it('supports primary, secondary, and danger variants', () => {
    const primary = render(LoadingButton, { props: { variant: 'primary' } }).body;
    const secondary = render(LoadingButton, { props: { variant: 'secondary' } }).body;
    const danger = render(LoadingButton, { props: { variant: 'danger' } }).body;

    expect(primary).toContain('class="btn primary"');
    expect(secondary).toContain('class="btn secondary"');
    expect(danger).toContain('class="btn danger"');
  });
});
