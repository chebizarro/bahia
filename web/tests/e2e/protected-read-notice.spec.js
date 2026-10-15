import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

test('signed-in protected read denial is visible and clears after recovery', async ({ page }) => {
  await installE2EMocks(page);
  await page.goto('/');
  await expect(page.getByRole('heading', { name: /Dashboard/ })).toBeVisible();
  await expect.poll(() => page.evaluate(async () => {
    const { controlplaneConnection } = await import('/src/lib/stores/controlplane.svelte.js');
    return controlplaneConnection.bootstrapComplete && controlplaneConnection.protectedReadsReady;
  })).toBe(true);

  await page.evaluate(async () => {
    const { authState } = await import('/src/lib/stores/auth.svelte.js');
    const { controlplaneConnection } = await import('/src/lib/stores/controlplane.svelte.js');
    authState.status = 'authenticated';
    controlplaneConnection.protectedReadsReady = false;
    controlplaneConnection.protectedReadError = 'wss://relay.example: restricted: not admitted';
  });
  const notice = page.getByTestId('protected-read-notice');
  await expect(notice).toBeVisible();
  await expect(notice).toContainText('Protected data may be missing');
  await expect(notice).toContainText('restricted: not admitted');

  await page.evaluate(async () => {
    const { controlplaneConnection } = await import('/src/lib/stores/controlplane.svelte.js');
    controlplaneConnection.protectedReadsReady = true;
    controlplaneConnection.protectedReadError = null;
  });
  await expect(notice).toHaveCount(0);
});
