import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = E2E_SERVICE_PUBKEY;
const RUNTIME_PUBKEY = 'd'.repeat(64);
const BROWSER_RELAY = 'ws://relay.test.local';
const systemInfo = {
  nostr: {
    browser_relays: [BROWSER_RELAY],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    legacy_sse: false
  }
};

function runtimeCapabilityEvent() {
  return {
    id: 'runtime-capability-openclaw-provision',
    kind: 30317,
    pubkey: RUNTIME_PUBKEY,
    created_at: Math.floor(Date.now() / 1000),
    tags: [
      ['d', 'openclaw-soulfactory-provision'],
      ['runtime', 'openclaw'],
      ['schema', 'soulfactory-runtime-capability/v1'],
      ['control-schema', 'soulfactory-runtime-control/v1'],
      ['method', 'soulfactory.provision'],
      ['relay', BROWSER_RELAY, 'control']
    ],
    content: JSON.stringify({
      schema: 'soulfactory-runtime-capability/v1',
      runtime: 'openclaw',
      control_schema: 'soulfactory-runtime-control/v1',
      methods: ['soulfactory.provision'],
      relay_hints: { control: [BROWSER_RELAY] }
    }),
    sig: '0'.repeat(128)
  };
}

async function fillRequiredIdentity(page, { name = 'Test Agent', agentId = 'test-agent', brief = 'This is a test agent for smoke testing', tier = 'standard' } = {}) {
  await page.getByLabel('Name').fill(name);
  await page.getByLabel('Agent ID').fill(agentId);
  await page.getByLabel('Purpose').fill(brief);
  await page.getByLabel('Tier').selectOption(tier);
}

async function openPreview(page) {
  await page.getByRole('button', { name: /Runtime/ }).click();
  await expect(page.locator('.wizard-progress .progress-step[aria-current="step"]:has-text("Customize")')).toBeVisible();
  await page.getByRole('button', { name: /Preview draft/ }).click();
  await expect(page.locator('.wizard-progress .progress-step[aria-current="step"]:has-text("Preview")')).toBeVisible();
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { authenticated: true, extension: true, nostrEvents: [runtimeCapabilityEvent()], systemInfo });
});

test.describe('Soul Signing Smoke Test', () => {
  test('should complete soul provisioning flow with NIP-07 signing', async ({ page }) => {
    await page.goto('/souls/new');

    await expect(page.getByRole('heading', { name: 'Create New Soul' })).toBeVisible();
    await expect(page.locator('.wizard-progress .progress-step[aria-current="step"]:has-text("Customize")')).toBeVisible();
    await fillRequiredIdentity(page);

    await expect(page.locator('.auth-status:has-text("Authenticated")')).toBeVisible();

    await openPreview(page);
    await page.getByRole('button', { name: 'Provision Soul' }).click();

    await expect(page.locator('.wizard-progress .progress-step[aria-current="step"]:has-text("Provision")')).toBeVisible();
    await expect(page.getByText('Event Signed & Published')).toBeVisible();
    await expect(page.getByText('Request ID:')).toBeVisible();
  });

  test('should show NIP-07 extension status', async ({ page }) => {
    await page.goto('/souls/new');

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

    await page.fill('#agentName', 'My Test Agent');
    await page.locator('#agentName').blur();

    await expect(page.locator('#agentId')).toHaveValue(/my-test-agent/);
  });

  test('should allow navigation between wizard steps', async ({ page }) => {
    await page.goto('/souls/new');

    await expect(page.locator('.wizard-progress .progress-step[aria-current="step"]:has-text("Customize")')).toBeVisible();

    await fillRequiredIdentity(page, { name: 'Navigation Agent', agentId: 'navigation-agent', brief: 'Verify wizard navigation' });
    await openPreview(page);

    await page.getByRole('button', { name: /Back/ }).click();
    await expect(page.locator('.wizard-progress .progress-step[aria-current="step"]:has-text("Customize")')).toBeVisible();
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
            const message = JSON.parse(data);
            if (Array.isArray(message) && message[0] === 'REQ') {
              const subId = message[1];
              const filters = message.slice(2);
              const events = JSON.parse(localStorage.getItem('__bahia_e2e_nostr_events') || '[]');
              for (const storedEvent of events) {
                if (filters.some((filter) => !Array.isArray(filter.kinds) || filter.kinds.includes(storedEvent.kind))) {
                  this.onmessage({ data: JSON.stringify(['EVENT', subId, storedEvent]) });
                }
              }
              this.onmessage({ data: JSON.stringify(['EOSE', subId]) });
              return;
            }
            if (Array.isArray(message) && message[0] === 'EVENT') {
              const event = message[1];
              const accepted = event?.kind === 31952;
              this.onmessage({ data: JSON.stringify(['OK', event?.id, accepted, accepted ? '' : 'auth required']) });
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

    await fillRequiredIdentity(page, { name: 'Reject Agent', agentId: 'reject-agent', brief: 'Should fail due to relay rejection' });
    await openPreview(page);

    await page.getByRole('button', { name: 'Provision Soul' }).click();

    await expect(page.locator('.error-banner')).toContainText('not accepted by any relay');
    await expect(page.locator('.wizard-progress .progress-step[aria-current="step"]:has-text("Preview")')).toBeVisible();
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

    await fillRequiredIdentity(page, { name: 'Test Agent', agentId: 'test-agent', brief: 'Test brief', tier: 'lightweight' });
    await openPreview(page);

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
