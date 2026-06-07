import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = 'b'.repeat(64);
const BROWSER_RELAY = 'ws://browser-bootstrap.test.local';
const ENCRYPTED_RELAY = 'ws://encrypted-requests.test.local';
const SERVICE_RELAYS = ['ws://service-read.test.local', 'wss://service-write.example'];

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
