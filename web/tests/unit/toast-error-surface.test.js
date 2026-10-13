import { beforeEach, describe, expect, it } from 'vitest';
import { get } from 'svelte/store';
import { clearToasts, removeToast, toast, toasts } from '../../src/lib/components/toast.js';

describe('operator error toasts', () => {
  beforeEach(clearToasts);

  it('keeps bootstrap errors visible without stacking duplicates', () => {
    const first = toast.error('Check relay access and reconnect.', { key: 'bootstrap:relay' });
    expect(toast.error('Check relay access and reconnect.', { key: 'bootstrap:relay' })).toBe(first);
    expect(get(toasts)).toHaveLength(1);
    expect(get(toasts)[0]).toMatchObject({ type: 'error', timeout: 0 });

    removeToast(first);
    toast.error('Check relay access and reconnect.', { key: 'bootstrap:relay' });
    expect(get(toasts)).toHaveLength(1);
  });
});
