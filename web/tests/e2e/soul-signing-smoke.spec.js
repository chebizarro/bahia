import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { authenticated: true, extension: true, nostrEvents: [] });
});

test.describe('Soul Signing Smoke Test', () => {
  test('should complete soul provisioning flow with NIP-07 signing', async ({ page }) => {
    await page.goto('/souls/new');

    await expect(page.getByRole('heading', { name: 'Create New Soul' })).toBeVisible();
    await page.getByRole('button', { name: 'Continue' }).click();
    await page.getByRole('button', { name: 'Continue' }).click();

    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Configure")')).toBeVisible();

    await page.fill('#agentName', 'Test Agent');
    await page.fill('#agentId', 'test-agent');
    await page.selectOption('#tier', 'standard');
    await page.fill('#brief', 'This is a test agent for smoke testing');

    await expect(page.locator('.auth-status:has-text("Authenticated")')).toBeVisible();

    await page.getByRole('button', { name: 'Provision Soul' }).click();

    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Provision")')).toBeVisible();
    await expect(page.getByText('Event Signed & Published')).toBeVisible();
    await expect(page.getByText('Request ID:')).toBeVisible();
  });

  test('should show NIP-07 extension status', async ({ page }) => {
    await page.goto('/souls/new');

    await page.getByRole('button', { name: 'Continue' }).click();
    await page.getByRole('button', { name: 'Continue' }).click();

    await expect(page.locator('.auth-status')).toBeVisible();
    await expect(page.locator('.auth-status:has-text("Authenticated")')).toBeVisible();
  });

  test('should block the provisioning route when no signer session is available', async ({ page }) => {
    await installE2EMocks(page, { authenticated: false, extension: false, nostrEvents: [] });

    await page.goto('/souls/new');

    await expect(page).toHaveURL(/\/$/);
    await expect(page.getByRole('heading', { name: 'Dashboard' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Create New Soul' })).toHaveCount(0);
  });

  test('should generate agent ID from name', async ({ page }) => {
    await page.goto('/souls/new');

    await page.getByRole('button', { name: 'Continue' }).click();
    await page.getByRole('button', { name: 'Continue' }).click();

    await page.fill('#agentName', 'My Test Agent');
    await page.locator('#agentName').blur();

    await expect(page.locator('#agentId')).toHaveValue(/my-test-agent/);
  });

  test('should allow navigation between wizard steps', async ({ page }) => {
    await page.goto('/souls/new');

    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Template")')).toBeVisible();

    await page.getByRole('button', { name: 'Continue' }).click();
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Repository")')).toBeVisible();

    await page.getByRole('button', { name: 'Continue' }).click();
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Configure")')).toBeVisible();

    await page.getByRole('button', { name: 'Back' }).click();
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Repository")')).toBeVisible();
  });

  test('should show relay rejection error when publish is not accepted', async ({ page }) => {
    await page.addInitScript(() => {
      class RejectingWebSocket {
        static CONNECTING = 0;
        static OPEN = 1;
        static CLOSED = 3;

        constructor(url) {
          this.url = url;
          this.readyState = 1;
          setTimeout(() => {
            if (this.onopen) this.onopen({ type: 'open' });
          }, 0);
        }

        send(data) {
          setTimeout(() => {
            if (!this.onmessage) return;
            const event = JSON.parse(data);
            if (Array.isArray(event) && event[0] === 'REQ') {
              this.onmessage({ data: JSON.stringify(['EOSE', event[1]]) });
              return;
            }
            if (Array.isArray(event) && event[0] === 'EVENT') {
              this.onmessage({ data: JSON.stringify(['OK', event[1]?.id, false, 'auth required']) });
            }
          }, 0);
        }

        close() {
          this.readyState = 3;
          if (this.onclose) this.onclose({ type: 'close' });
        }
      }

      window.WebSocket = RejectingWebSocket;
    });

    await page.goto('/souls/new');

    await page.getByRole('button', { name: 'Continue' }).click();
    await page.getByRole('button', { name: 'Continue' }).click();

    await page.fill('#agentName', 'Reject Agent');
    await page.fill('#agentId', 'reject-agent');
    await page.fill('#brief', 'Should fail due to relay rejection');

    await page.getByRole('button', { name: 'Provision Soul' }).click();

    await expect(page.locator('.error-banner')).toContainText('not accepted by any relay');
    await expect(page.locator('.wizard-progress .progress-step.active:has-text("Configure")')).toBeVisible();
  });

  test('should include provisioning request tags', async ({ page }) => {
    await page.addInitScript(() => {
      const originalSign = window.nostr.signEvent;
      window.nostr.signEvent = async (event) => {
        window._capturedEvent = event;
        return originalSign(event);
      };
    });

    await page.goto('/souls/new');

    await page.getByRole('button', { name: 'Continue' }).click();
    await page.getByRole('button', { name: 'Continue' }).click();

    await page.fill('#agentName', 'Test Agent');
    await page.fill('#agentId', 'test-agent');
    await page.fill('#brief', 'Test brief');
    await page.selectOption('#tier', 'lightweight');

    await page.getByRole('button', { name: 'Provision Soul' }).click();
    await page.waitForFunction(() => Boolean(window._capturedEvent));

    const capturedEvent = await page.evaluate(() => window._capturedEvent);

    expect(capturedEvent.tags).toBeDefined();
    const tags = capturedEvent.tags;
    const agentIdTag = tags.find((tag) => tag[0] === 'agent-id');
    const nameTag = tags.find((tag) => tag[0] === 'name');
    const tierTag = tags.find((tag) => tag[0] === 'tier');
    const outputTag = tags.find((tag) => tag[0] === 'output');

    expect(agentIdTag).toBeDefined();
    expect(agentIdTag[1]).toContain('test-agent');
    expect(nameTag).toBeDefined();
    expect(tierTag).toBeDefined();
    expect(tierTag[1]).toBe('lightweight');
    expect(outputTag).toBeDefined();
  });
});
