import { test, expect } from '@playwright/test';

function splitList(value) {
  return (value || '').split(',').map((item) => item.trim()).filter(Boolean);
}

const bootstrapRelays = splitList(
  process.env.PUBLIC_BAHIA_BOOTSTRAP_RELAYS ||
  process.env.VITE_BAHIA_BOOTSTRAP_RELAYS ||
  process.env.BAHIA_BOOTSTRAP_RELAYS ||
  process.env.BAHIA_NOSTR_RELAYS
);
const servicePubkeys = splitList(
  process.env.PUBLIC_BAHIA_SERVICE_PUBKEYS ||
  process.env.VITE_BAHIA_SERVICE_PUBKEYS ||
  process.env.PUBLIC_BAHIA_SERVICE_PUBKEY ||
  process.env.VITE_BAHIA_SERVICE_PUBKEY ||
  process.env.BAHIA_SERVICE_PUBKEYS ||
  process.env.BAHIA_SERVICE_PUBKEY
);

test.describe('Real sidecar system discovery smoke', () => {
  test.skip(
    process.env.BAHIA_REAL_SIDECAR_SMOKE !== '1',
    'Set BAHIA_REAL_SIDECAR_SMOKE=1 with real sidecar bootstrap relays and service pubkeys to run this smoke.'
  );

  test('accepts trusted discovery state before EOSE from the real sidecar', async ({ page }) => {
    expect(bootstrapRelays, 'real sidecar bootstrap relays').not.toHaveLength(0);
    expect(servicePubkeys, 'trusted Bahia service pubkeys').not.toHaveLength(0);

    await page.addInitScript(({ relay_urls, service_pubkeys }) => {
      window.__BAHIA_BOOTSTRAP__ = {
        schema: 'bahia.bootstrap.v1',
        relay_urls,
        service_pubkeys
      };
    }, { relay_urls: bootstrapRelays, service_pubkeys: servicePubkeys });

    await page.goto('/');

    const normalized = await page.evaluate(async () => {
      const discovery = await import('/src/lib/stores/discovery.svelte.js');
      return discovery.discoverSystemInfo({ force: true });
    });

    expect(servicePubkeys).toContain(normalized?.nostr?.service_pubkey);
    expect(normalized?.schema).toBe('bahia.system-discovery.v1');
    expect(normalized?.nostr?.browser_relays || []).not.toHaveLength(0);
    expect(normalized?._discovery?.event_id).toBeTruthy();
    expect(normalized?._discovery?.relay_sets?.['bahia-browser-v1'] || []).not.toHaveLength(0);
  });
});
