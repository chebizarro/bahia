import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = 'b'.repeat(64);
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
  test('shows service-authored NIP-51 relay-list data instead of raw system discovery relays', async ({ page }) => {
    await installE2EMocks(page, { systemInfo });

    await page.goto('/settings');

    await expect(page.getByRole('heading', { name: 'Settings', exact: true })).toBeVisible();
    const serverConfig = page.locator('section', { hasText: 'Server Configuration' });
    await expect(serverConfig.getByText('Service Relay List (NIP-51)')).toBeVisible();
    await expect(serverConfig.getByText(SERVICE_RELAYS.join(', '))).toBeVisible();
    await expect(serverConfig).not.toContainText(BROWSER_RELAY);
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

    await page.goto('/settings');

    const operatorPolicy = page.locator('section', { hasText: 'Operator Relay Policy' });
    await expect(operatorPolicy.getByText('hydrated from canonical 30900 state')).toBeVisible();
    await expect(operatorPolicy.locator('label', { hasText: 'Browser/bootstrap relays' }).locator('textarea')).toHaveValue('wss://canonical-browser.example');
    await expect(operatorPolicy.locator('label', { hasText: 'ContextVM request/reply relays' }).locator('textarea')).toHaveValue('wss://canonical-contextvm.example');
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

    await page.goto('/settings');

    const operatorPolicy = page.locator('section', { hasText: 'Operator Relay Policy' });
    const browserPolicy = operatorPolicy.locator('label', { hasText: 'Browser/bootstrap relays' }).locator('textarea');
    await expect(browserPolicy).toHaveValue('wss://canonical-initial.example');
    await browserPolicy.fill('wss://local-dirty.example');

    await page.evaluate((event) => window.__bahiaPushNostrEvent(event), relaySettingsStateEvent({ browserRelays: ['wss://canonical-newer.example'], createdAt: now }));

    await expect(operatorPolicy.getByText('canonical 30900 state pending; local edits preserved')).toBeVisible();
    await expect(browserPolicy).toHaveValue('wss://local-dirty.example');
    await expect(operatorPolicy.getByRole('button', { name: 'Apply Canonical State' })).toBeVisible();
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

    await page.goto('/settings');

    await expect(page.getByRole('heading', { name: 'Operator Relay Policy' })).toBeVisible();
    await expect(page.getByText('ContextVM request/reply relays')).toBeVisible();
    await expect(page.getByText('Trusted NIP-66 monitor pubkeys')).toBeVisible();
    await expect(page.getByText('Notification DM relays (NIP-51 kind 10050)')).toBeVisible();
    await expect(page.getByText('NIP-86 managed relay targets')).toBeVisible();
    await expect(page.getByRole('button', { name: 'Publish Relay Policy Mutation' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Browser Session Relays' })).toBeVisible();
    await expect(page.getByText('Local emergency override for this browser session only')).toBeVisible();
  });
});
