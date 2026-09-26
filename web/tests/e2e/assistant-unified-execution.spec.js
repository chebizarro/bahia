import { execSync } from 'node:child_process';
import { test, expect } from '@playwright/test';
import { finalizeEvent, getPublicKey, nip44 } from 'nostr-tools';

// Joined item 2+3 scenario: the real relay, ContextVM handlers, proposers,
// unified executor and downstream provider fixtures. Nothing here mocks a
// request, response or projection; every assertion reads canonical v2 state
// that the service published. The spec is skipped until the harness provides:
//
//   BAHIA_ASSISTANT_E2E_JOINED_URL        dashboard served against the joined backend
//   BAHIA_ASSISTANT_E2E_SECRET_HEX        isolated fleet-operator key (64 hex)
//   BAHIA_ASSISTANT_E2E_BATCH_PROMPT      prompt whose deterministic batch proposal has exactly 3 steps
//   BAHIA_ASSISTANT_E2E_EDITED_ARGS_JSON  valid tool_args for the proposal's first step
//   BAHIA_ASSISTANT_E2E_ITERATIVE_PROMPT  prompt whose model fixture stays in `proposing` until cancelled
// Optional:
//   BAHIA_ASSISTANT_E2E_RESTART_COMMAND   shell command restarting the backend (recovery check)
//   BAHIA_ASSISTANT_E2E_UNCERTAIN_SESSION_ID / _WORK_ID / _REQUEST_EVENT_ID  seeded uncertain work
//   BAHIA_ASSISTANT_E2E_TIMEOUT_MS        wait for backend-driven transitions (default 60000)
const joinedUrl = process.env.BAHIA_ASSISTANT_E2E_JOINED_URL;
const secretHex = process.env.BAHIA_ASSISTANT_E2E_SECRET_HEX;
const batchPrompt = process.env.BAHIA_ASSISTANT_E2E_BATCH_PROMPT;
const editedArgsText = process.env.BAHIA_ASSISTANT_E2E_EDITED_ARGS_JSON;
const iterativePrompt = process.env.BAHIA_ASSISTANT_E2E_ITERATIVE_PROMPT;
const restartCommand = process.env.BAHIA_ASSISTANT_E2E_RESTART_COMMAND;
const backendTimeout = Number(process.env.BAHIA_ASSISTANT_E2E_TIMEOUT_MS || 60000);
const ready = Boolean(joinedUrl && /^[0-9a-f]{64}$/i.test(secretHex || '') && batchPrompt && editedArgsText && iterativePrompt);

async function openPanel(page) {
  // The bubble's click handler attaches after hydration; clicking earlier is lost.
  const bubble = page.locator('button.assistant-bubble[data-toggle-attached="true"]');
  await bubble.waitFor();
  if (await bubble.getAttribute('aria-expanded') !== 'true') await bubble.click();
  const panel = page.getByRole('dialog', { name: 'Assistant chat' });
  await expect(panel).toBeVisible();
  await expect(panel).toContainText('live', { timeout: backendTimeout });
  return panel;
}

async function sendPrompt(panel, workflow, prompt) {
  await panel.getByRole('combobox', { name: 'Assistant workflow' }).selectOption(workflow);
  await panel.getByPlaceholder('Ask the Bahia assistant…').fill(prompt);
  await panel.getByRole('button', { name: 'Send' }).click();
}

test.describe('joined assistant unified execution', () => {
  test.skip(!ready, 'Requires the joined item 2+3 backend harness and isolated real relay/provider fixtures');

  test.beforeEach(async ({ page }) => {
    const secret = Buffer.from(secretHex, 'hex');
    const pubkey = getPublicKey(secret);
    // A real NIP-07 signer backed by the isolated operator key.
    await page.exposeFunction('__assistantSign', (event) => finalizeEvent(event, secret));
    await page.exposeFunction('__assistantEncrypt', (peer, plaintext) => nip44.encrypt(plaintext, nip44.getConversationKey(secret, peer)));
    await page.exposeFunction('__assistantDecrypt', (peer, ciphertext) => nip44.decrypt(ciphertext, nip44.getConversationKey(secret, peer)));
    await page.addInitScript((operatorPubkey) => {
      window.nostr = {
        getPublicKey: async () => operatorPubkey,
        signEvent: (event) => window.__assistantSign(event),
        getRelays: async () => ({}),
        nip44: {
          encrypt: (peer, plaintext) => window.__assistantEncrypt(peer, plaintext),
          decrypt: (peer, ciphertext) => window.__assistantDecrypt(peer, ciphertext)
        }
      };
      // Restores through the normal NIP-07 path, which re-verifies the signer pubkey.
      if (!localStorage.getItem('bahia_auth_session')) {
        localStorage.setItem('bahia_auth_session', JSON.stringify({ pubkey: operatorPubkey, relays: {}, authMethod: 'nip07',
          lastAuthenticatedAt: new Date().toISOString() }));
      }
    }, pubkey);
    await page.goto(joinedUrl);
    const panel = await openPanel(page);
    await panel.getByRole('button', { name: 'Start a new assistant session' }).click();
  });

  test('edits, reorders, removes and approves a batch, then survives reload and restart', async ({ page }) => {
    let panel = page.getByRole('dialog', { name: 'Assistant chat' });
    await sendPrompt(panel, 'batch', batchPrompt);
    const execution = panel.getByLabel('Current assistant execution');
    const card = panel.getByRole('region', { name: 'Assistant plan approval' });
    await expect(card).toBeVisible({ timeout: backendTimeout });
    await expect(execution).toHaveAttribute('data-workflow', 'batch');
    await expect(execution).toHaveAttribute('data-phase', 'awaiting_approval');
    const runId = await execution.getAttribute('data-run-id');
    expect(runId).toBeTruthy();
    // Cancellation is available for the run without any plan hash.
    await expect(panel.getByRole('button', { name: 'Cancel run' })).toBeEnabled();

    const steps = card.locator('ol.steps > li');
    await expect(steps).toHaveCount(3);
    const firstTitle = await steps.nth(0).locator('.step-title').innerText();
    const secondTitle = await steps.nth(1).locator('.step-title').innerText();
    // reorder: first step moves below the second
    await steps.nth(0).getByRole('button', { name: 'Move step down' }).click();
    await expect(steps.nth(0).locator('.step-title')).toHaveText(secondTitle);
    await expect(steps.nth(1).locator('.step-title')).toHaveText(firstTitle);
    // remove the third step
    await steps.nth(2).getByRole('button', { name: 'Remove step' }).click();
    await expect(steps).toHaveCount(2);

    // invalid JSON in one step stays invalid while another step is edited
    const moved = steps.nth(1).getByRole('textbox', { name: 'Tool args JSON' });
    const other = steps.nth(0).getByRole('textbox', { name: 'Tool args JSON' });
    await moved.fill('{"unterminated":');
    await other.fill(await other.inputValue());
    await expect(steps.nth(1)).toContainText('Invalid JSON');
    await expect(moved).toHaveValue('{"unterminated":');
    await expect(card.getByRole('button', { name: 'Approve' })).toBeDisabled();
    await moved.fill(editedArgsText);
    await expect(steps.nth(1)).not.toContainText('Invalid JSON');
    await expect(card).toContainText('Approval submits it as revision');

    // The service recomputes the RFC 8785 approval hash; acceptance proves the browser bound the same bytes.
    await card.getByRole('button', { name: 'Approve' }).click();
    await expect(card).toHaveCount(0, { timeout: backendTimeout });
    await expect(panel).not.toContainText('Approval refers to superseded state');
    await expect(panel).not.toContainText('Approval rejected by the assistant service');
    if (restartCommand) execSync(restartCommand, { stdio: 'inherit', timeout: backendTimeout });
    await expect(execution).toHaveAttribute('data-phase', 'completed', { timeout: backendTimeout });
    await expect(execution).toHaveAttribute('data-run-id', runId);
    await expect(execution).toHaveAttribute('data-uncertain-effects', '0');
    const submitted = await execution.getAttribute('data-submitted-effects');

    // Reload: the cached view is display-only until the relay re-delivers the projection.
    await page.reload();
    panel = await openPanel(page);
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-run-id', runId, { timeout: backendTimeout });
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-phase', 'completed');
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-submitted-effects', submitted);
    // Historical proposal rows never reactivate approval controls.
    await expect(panel.getByRole('region', { name: 'Assistant plan approval' })).toHaveCount(0);
    await expect(panel.getByRole('button', { name: 'Cancel run' })).toHaveCount(0);
  });

  test('cancels a hashless iterative run from its canonical run identity and keeps accounting', async ({ page }) => {
    let panel = page.getByRole('dialog', { name: 'Assistant chat' });
    await sendPrompt(panel, 'iterative', iterativePrompt);
    const execution = panel.getByLabel('Current assistant execution');
    await expect(execution).toHaveAttribute('data-phase', 'proposing', { timeout: backendTimeout });
    await expect(execution).toHaveAttribute('data-workflow', 'iterative');
    const runId = await execution.getAttribute('data-run-id');
    // No plan, no hash: the only identity is the run.
    await expect(panel.getByRole('region', { name: 'Assistant plan approval' })).toHaveCount(0);
    await expect(panel.getByPlaceholder('Ask the Bahia assistant…')).toBeDisabled();

    await panel.getByRole('button', { name: 'Cancel run' }).click();
    await expect(execution).toHaveAttribute('data-phase', /^(cancelling|cancelled)$/, { timeout: backendTimeout });
    await expect(execution).toHaveAttribute('data-run-id', runId);
    if (await execution.getAttribute('data-phase') === 'cancelling') {
      await expect(panel).toContainText('Assistant stopped; submitted operations may still finish. No rollback was attempted.');
    }
    await expect(panel).not.toContainText('Cancellation request rejected');
    await expect(panel).not.toContainText('Cancellation request refers to superseded state');

    await page.reload();
    panel = await openPanel(page);
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-run-id', runId, { timeout: backendTimeout });
    await expect(panel.getByLabel('Current assistant execution')).toHaveAttribute('data-phase', /^(cancelling|cancelled)$/);
    await expect(panel.getByRole('region', { name: 'Assistant action approval' })).toHaveCount(0);
    await expect(panel.getByRole('region', { name: 'Assistant plan approval' })).toHaveCount(0);
  });

  test('reconciles uncertain work only with an exact downstream request event', async ({ page }) => {
    const sessionId = process.env.BAHIA_ASSISTANT_E2E_UNCERTAIN_SESSION_ID;
    const workId = process.env.BAHIA_ASSISTANT_E2E_UNCERTAIN_WORK_ID;
    const requestEventId = process.env.BAHIA_ASSISTANT_E2E_UNCERTAIN_REQUEST_EVENT_ID;
    test.skip(!sessionId || !workId || !/^[0-9a-f]{64}$/.test(requestEventId || ''),
      'Requires a backend-seeded uncertain work item and its real downstream request event');
    const panel = page.getByRole('dialog', { name: 'Assistant chat' });
    await panel.locator(`.sessions button[data-session-id="${sessionId}"]`).click();
    const execution = panel.getByLabel('Current assistant execution');
    await expect(execution).toHaveAttribute('data-uncertain-effects', /^[1-9]\d*$/, { timeout: backendTimeout });
    const form = panel.getByRole('region', { name: 'Assistant execution reconciliation' });
    await expect(form).toBeVisible();
    await expect(form.getByRole('button')).toHaveCount(1);
    await expect(form.getByRole('button', { name: /mark (as )?complete/i })).toHaveCount(0);
    await form.getByRole('textbox', { name: 'Uncertain work ID' }).fill(workId);
    await form.getByRole('textbox', { name: 'Exact downstream request event ID' }).fill(requestEventId.toUpperCase());
    await expect(form.getByRole('button', { name: 'Submit evidence' })).toBeDisabled();
    await form.getByRole('textbox', { name: 'Exact downstream request event ID' }).fill(requestEventId);
    await form.getByRole('button', { name: 'Submit evidence' }).click();
    await expect(execution).toHaveAttribute('data-uncertain-effects', '0', { timeout: backendTimeout });
  });
});
