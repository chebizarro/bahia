import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = E2E_SERVICE_PUBKEY;
const BROWSER_RELAY = 'ws://browser-bootstrap.test.local';
const ENCRYPTED_RELAY = 'ws://encrypted-requests.test.local';
const SERVICE_RELAYS = ['ws://service-read.test.local', 'wss://service-write.example'];

function relaySettingsStateEvent({ browserRelays = [], contextVMRelays = [], serviceRelays = [], createdAt = 10 } = {}) {
  return {
    id: `relay-settings-${createdAt}`,
    kind: 30900,
    pubkey: SERVICE_PUBKEY,
    created_at: createdAt,
    tags: [['d', 'relay-settings:operator'], ['domain', 'relay-settings'], ['schema', 'bahia.relay-settings.v1']],
    content: JSON.stringify({
      schema: 'bahia.relay-settings.v1',
      browser_relays: browserRelays,
      contextvm_relays: contextVMRelays,
      service_relays: serviceRelays
    }),
    sig: '0'.repeat(128)
  };
}

const systemInfo = {
  nostr: {
    browser_relays: [BROWSER_RELAY],
    service_relays: SERVICE_RELAYS,
    service_pubkey: SERVICE_PUBKEY,
    service_npub: 'npub1serviceexample',
    publish_enabled: true
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    encrypted_nostr_requests: true,
    legacy_sse: false
  },
  registries: []
};

test.describe('Settings relay visibility', () => {
  test('shows service-authored relay-list data instead of raw system discovery relays', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });

    await page.goto('/settings');

    await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
    const serverConfig = page.locator('section', { hasText: 'Server Configuration' });
    await expect(serverConfig.getByText('Service Relay List')).toBeVisible();
    await expect(serverConfig.getByText(SERVICE_RELAYS.join(', '))).toBeVisible();
    await expect(serverConfig).not.toContainText(BROWSER_RELAY);
  });

  test('navigates from settings to the dedicated relay settings route', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });

    await page.goto('/settings');
    await page.getByRole('link', { name: /Relays/ }).click();

    await expect(page).toHaveURL(/\/settings\/relays$/);
    await expect(page.getByRole('heading', { name: 'Relay Settings', exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Browser Session Relays' })).toBeVisible();
  });

  test('hydrates canonical relay policy and subscribes through advertised service relays', async ({ page }) => {
    await installE2EMocks(page, {
      systemInfo: {
        ...systemInfo,
        nostr: {
          ...systemInfo.nostr,
          browser_relays: [BROWSER_RELAY],
          contextvm_relays: [],
          service_relays: ['ws://service-read.test.local']
        }
      },
      nostrEvents: [relaySettingsStateEvent({
        browserRelays: ['wss://canonical-browser.example'],
        contextVMRelays: ['wss://canonical-contextvm.example'],
        serviceRelays: ['wss://canonical-service.example']
      })]
    });

    await page.goto('/settings/relays');

    const operatorPolicy = page.locator('section', { hasText: 'Operator Relay Policy' });
    await expect(operatorPolicy.getByText('loaded from service relay policy')).toBeVisible();
    await expect(operatorPolicy.locator('label', { hasText: 'Browser/bootstrap relays' }).locator('textarea')).toHaveValue('wss://canonical-browser.example');
    await expect(operatorPolicy.locator('label', { hasText: 'Secure request relays' }).locator('textarea')).toHaveValue('wss://canonical-contextvm.example');
    await expect(operatorPolicy.locator('label', { hasText: 'Service publish/backfill relays' }).locator('textarea')).toHaveValue('wss://canonical-service.example');
    await expect.poll(async () => page.evaluate(() => (window.__BAHIA_E2E_WS_CONNECTIONS || []).map((socket) => socket.url))).toContain('ws://service-read.test.local/');
  });

  test('preserves dirty operator relay edits when newer canonical state arrives', async ({ page }) => {
    const now = Math.floor(Date.now() / 1000);
    await installE2EMocks(page, {
      systemInfo: {
        ...systemInfo,
        nostr: {
          ...systemInfo.nostr,
          contextvm_relays: [ENCRYPTED_RELAY]
        }
      },
      nostrEvents: [relaySettingsStateEvent({ browserRelays: ['wss://canonical-initial.example'], createdAt: now - 1 })]
    });

    await page.goto('/settings/relays');

    const operatorPolicy = page.locator('section', { hasText: 'Operator Relay Policy' });
    const browserPolicy = operatorPolicy.locator('label', { hasText: 'Browser/bootstrap relays' }).locator('textarea');
    await expect(browserPolicy).toHaveValue('wss://canonical-initial.example');
    await browserPolicy.fill('wss://local-dirty.example');

    await page.evaluate((event) => window.__bahiaPushNostrEvent(event), relaySettingsStateEvent({ browserRelays: ['wss://canonical-newer.example'], createdAt: now }));

    await expect(operatorPolicy.getByText('service relay policy pending; local edits preserved')).toBeVisible();
    await expect(browserPolicy).toHaveValue('wss://local-dirty.example');
    await expect(operatorPolicy.getByRole('button', { name: 'Apply Service Policy' })).toBeVisible();
    await expect(operatorPolicy.getByRole('button', { name: 'Keep Local Edits' })).toBeVisible();
  });

  test('shows persistent operator relay policy and separate local browser relay controls', async ({ page }) => {
    await installE2EMocks(page, { systemInfo: {
      ...systemInfo,
      nostr: {
        ...systemInfo.nostr,
        contextvm_relays: [ENCRYPTED_RELAY],
        trusted_relay_monitor_pubkeys: ['a'.repeat(64)],
        dm_relay_lists: [{ enabled: true, feature: 'notifications', identity: 'service', relays: ['wss://dm-relay.example'] }],
        relay_administration: { targets: [{ ref: 'sidecar', relay_url: 'wss://sidecar.example', authorization: 'bahia_owned', administrator_pubkeys: ['b'.repeat(64)] }] }
      }
    } });

    await page.goto('/settings/relays');

    await expect(page.getByRole('heading', { name: 'Operator Relay Policy' })).toBeVisible();
    await expect(page.getByText('Secure request relays')).toBeVisible();
    await expect(page.getByText('Trusted relay monitor pubkeys')).toBeVisible();
    await expect(page.getByText('Notification message relays')).toBeVisible();
    await expect(page.getByText('Managed relay targets')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Publish Relay Policy Update' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Browser Session Relays' })).toBeVisible();
    await expect(page.getByText('Local emergency override for this browser session only')).toBeVisible();
  });

  test('validates local relay URLs and reports reconnect outcomes for add and remove', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });

    await page.goto('/settings/relays');

    const browserRelays = page.locator('section', { hasText: 'Browser Session Relays' });
    const relayInput = browserRelays.getByPlaceholder('wss://relay.example.com');
    await relayInput.fill('https://not-a-relay.example');
    await browserRelays.getByRole('button', { name: 'Add & Reconnect Locally' }).click();
    await expect(browserRelays.getByText('Relay URL must start with wss:// or ws://.')).toBeVisible();

    await relayInput.fill('wss://added-relay.example');
    await browserRelays.getByRole('button', { name: 'Add & Reconnect Locally' }).click();
    await expect(browserRelays.getByText('wss://added-relay.example')).toBeVisible();
    await expect(browserRelays.getByText(/Reconnect succeeded: connected to \d+\/\d+ local browser relays\./)).toBeVisible();

    const addedRelay = browserRelays.locator('.relay-item', { hasText: 'wss://added-relay.example' });
    await addedRelay.getByTitle('Remove and reconnect').click();
    await expect(browserRelays.getByText('wss://added-relay.example')).toHaveCount(0);
    await expect(browserRelays.getByText(/Reconnect succeeded: connected to \d+\/\d+ local browser relays\./)).toBeVisible();
  });

  test('removing the final local browser relay is not overwritten by discovery fallback', async ({ page }) => {
    await page.addInitScript(() => {
      if (sessionStorage.getItem('__bahia_single_local_relay_seeded') !== 'true') {
        localStorage.setItem('bahia_nostr_relays', JSON.stringify(['ws://single-local-relay.test.local']));
        sessionStorage.setItem('__bahia_single_local_relay_seeded', 'true');
      }
    });
    await installE2EMocks(page, { systemInfo });

    await page.goto('/settings/relays');

    const browserRelays = page.locator('section', { hasText: 'Browser Session Relays' });
    await expect(browserRelays.getByText('ws://single-local-relay.test.local')).toBeVisible();
    await browserRelays.locator('.relay-item', { hasText: 'ws://single-local-relay.test.local' }).getByTitle('Remove and reconnect').click();

    await expect(browserRelays.getByText('ws://single-local-relay.test.local')).toHaveCount(0);
    await expect(browserRelays.getByText(BROWSER_RELAY)).toHaveCount(0);
    await expect(browserRelays.getByText('Relay configuration saved with no local browser relays configured.')).toBeVisible();
    await expect(browserRelays.getByText('No local browser relays configured.', { exact: true })).toBeVisible();

    await page.reload();
    const reloadedBrowserRelays = page.locator('section', { hasText: 'Browser Session Relays' });
    await expect(reloadedBrowserRelays.getByText('ws://single-local-relay.test.local')).toHaveCount(0);
    await expect(reloadedBrowserRelays.getByText(BROWSER_RELAY)).toHaveCount(0);
    await expect(reloadedBrowserRelays.getByText('No local browser relays configured.', { exact: true })).toBeVisible();
  });
});
