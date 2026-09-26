import { test, expect } from '@playwright/test';
import { finalizeEvent, getPublicKey, nip44 } from 'nostr-tools';

// This spec deliberately uses the real relay, ContextVM handler, proposer,
// executor and downstream provider. No request or projection is mocked.
// The joined harness supplies an isolated operator key and deterministic
// model/provider fixtures; items 2 and 3 must land before enabling it.
const joinedUrl = process.env.BAHIA_ASSISTANT_E2E_JOINED_URL;
const secretHex = process.env.BAHIA_ASSISTANT_E2E_SECRET_HEX;
const batchPrompt = process.env.BAHIA_ASSISTANT_E2E_BATCH_PROMPT;
const editedArgsText = process.env.BAHIA_ASSISTANT_E2E_EDITED_ARGS_JSON;
const iterativePrompt = process.env.BAHIA_ASSISTANT_E2E_ITERATIVE_PROMPT;
const ready = Boolean(joinedUrl && /^[0-9a-f]{64}$/i.test(secretHex || '') && batchPrompt && editedArgsText && iterativePrompt);

test.describe('joined assistant unified execution', () => {
  test.skip(!ready, 'Requires the joined item 2+3 backend harness and isolated real relay/provider fixtures');
  test.beforeEach(async ({ page }) => {
    const secret = Buffer.from(secretHex, 'hex');
    const pubkey = getPublicKey(secret);
    await page.exposeFunction('__assistantGetPublicKey', () => pubkey);
    await page.exposeFunction('__assistantSign', (event) => finalizeEvent(event, secret));
    await page.exposeFunction('__assistantEncrypt', (peer, plaintext) =>
      nip44.v2.encrypt(plaintext, nip44.getConversationKey(secret, peer)));
    await page.exposeFunction('__assistantDecrypt', (peer, ciphertext) =>
      nip44.v2.decrypt(ciphertext, nip44.getConversationKey(secret, peer)));
    await page.addInitScript(() => {
      window.nostr = {
        getPublicKey: () => window.__assistantGetPublicKey(),
        signEvent: (event) => window.__assistantSign(event),
        getRelays: async () => ({}),
        nip44: {
          encrypt: (peer, plaintext) => window.__assistantEncrypt(peer, plaintext),
          decrypt: (peer, ciphertext) => window.__assistantDecrypt(peer, ciphertext)
        }
      };
    });
    await page.goto(joinedUrl);
    await page.getByRole('button', { name: 'Open assistant chat' }).click();
    await expect(page.getByRole('dialog', { name: 'Assistant chat' })).toBeVisible();
    await page.getByRole('button', { name: 'Start a new assistant session' }).click();
  });

  test('edits, reorders, removes, approves and reloads a canonical batch run', async ({ page }) => {
    const panel = page.getByRole('dialog', { name: 'Assistant chat' });
    await panel.getByRole('combobox', { name: 'Assistant workflow' }).selectOption('batch');
    await panel.getByPlaceholder('Ask the Bahia assistant…').fill(batchPrompt);
    await panel.getByRole('button', { name: 'Send' }).click();
    const card = panel.getByRole('region', { name: 'Assistant plan approval' });
    await expect(card).toBeVisible();
    const originalRun = await panel.getByLabel('Current assistant execution').getAttribute('data-run-id');
    const steps = card.locator('ol.steps > li');
    await expect(steps).toHaveCount(3);
    const firstTitle = await steps.nth(0).locator('.step-title').innerText();
    const secondTitle = await steps.nth(1).locator('.step-title').innerText();
    await steps.nth(0).getByRole('button', { name: '↓' }).click();
    await expect(steps.nth(0).locator('.step-title')).toHaveText(secondTitle);
    await expect(steps.nth(1).locator('.step-title')).toHaveText(firstTitle);
    await steps.nth(2).getByRole('button', { name: 'Remove step' }).click();
    await expect(steps).toHaveCount(2);
    await steps.nth(1).getByRole('textbox', { name: 'Tool args JSON' }).fill(editedArgsText);
    await expect(card.getByRole('button', { name: 'Approve' })).toBeEnabled();
    await card.getByRole('button', { name: 'Approve' }).click();
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-phase', 'completed');
    await expect(card).toHaveCount(0);
    const submitted = await panel.getByLabel('Current assistant execution').getAttribute('data-submitted-effects');
    await page.reload();
    await expect(panel).toBeVisible();
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-run-id', originalRun);
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-submitted-effects', submitted);
    await expect(card).toHaveCount(0);
  });

  test('reconciles uncertain work only with an exact downstream request event', async ({ page }) => {
    const sessionId = process.env.BAHIA_ASSISTANT_E2E_UNCERTAIN_SESSION_ID;
    const workId = process.env.BAHIA_ASSISTANT_E2E_UNCERTAIN_WORK_ID;
    const requestEventId = process.env.BAHIA_ASSISTANT_E2E_UNCERTAIN_REQUEST_EVENT_ID;
    test.skip(!sessionId || !workId || !/^[0-9a-f]{64}$/.test(requestEventId || ''),
      'Requires a backend-seeded uncertain work item and its real downstream request event');
    const panel = page.getByRole('dialog', { name: 'Assistant chat' });
    await panel.locator(`.sessions button[data-session-id="${sessionId}"]`).click();
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-uncertain-effects', /[1-9]/);
    const form = panel.getByRole('region', { name: 'Assistant execution reconciliation' });
    await expect(form).toBeVisible();
    await expect(form.getByRole('button', { name: /mark complete/i })).toHaveCount(0);
    await form.getByRole('textbox', { name: 'Uncertain work ID' }).fill(workId);
    await form.getByRole('textbox', { name: 'Exact downstream request event ID' }).fill(requestEventId);
    await form.getByRole('button', { name: 'Submit evidence' }).click();
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-uncertain-effects', '0');
  });

  test('cancels a hashless iterative run during a pending prompt and retains accounting', async ({ page }) => {
    const panel = page.getByRole('dialog', { name: 'Assistant chat' });
    await panel.getByRole('combobox', { name: 'Assistant workflow' }).selectOption('iterative');
    await panel.getByPlaceholder('Ask the Bahia assistant…').fill(iterativePrompt);
    await panel.getByRole('button', { name: 'Send' }).click();
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-phase', 'proposing');
    await panel.getByRole('button', { name: 'Cancel run' }).click();
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-phase', /cancelling|cancelled/);
    if (await panel.getByLabel('Current assistant execution').getAttribute('data-phase') === 'cancelling') {
      await expect(panel).toContainText('Assistant stopped; submitted operations may still finish. No rollback was attempted.');
    }
    await page.reload();
    await expect(panel).toBeVisible();
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-phase', /cancelling|cancelled/);
    await expect(panel.getByRole('button', { name: 'Approve action' })).toHaveCount(0);
  });
});
