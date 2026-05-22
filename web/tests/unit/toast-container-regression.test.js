import { afterEach, describe, expect, it } from 'vitest';
import ToastContainer from '../../src/lib/components/ToastContainer.svelte';
import { addToast, clearToasts, removeToast } from '../../src/lib/components/toast.svelte.js';
import { renderComponent, textOf, tick } from './utils/svelte-component-test';

afterEach(() => {
  clearToasts();
});

describe('toast container regression', () => {
  it('renders and removes login toasts without crashing the runtime mount helpers', async () => {
    const target = renderComponent(ToastContainer, {});

    const id = addToast({ type: 'success', message: 'Signed in successfully', timeout: 0 });
    await tick();
    expect(textOf(target)).toContain('Signed in successfully');

    removeToast(id);
    await tick();
    expect(textOf(target)).not.toContain('Signed in successfully');
  });
});
