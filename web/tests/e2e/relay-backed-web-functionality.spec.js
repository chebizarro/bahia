import { test, expect } from '@playwright/test';
import { attachRuntimeErrorGuards } from './helpers-console.js';
import {
  installEmptyRestFallbacks,
  installRelayBackedBrowserContext,
  startBahiaTestRelay
} from './relay-harness.js';

let relay;

test.describe.serial('relay-backed Bahia web functionality', () => {
  test.beforeAll(async () => {
    relay = await startBahiaTestRelay();
  });

  test.afterAll(async () => {
    await relay?.stop();
  });

  test.beforeEach(async ({ page }) => {
    await installRelayBackedBrowserContext(page, relay, { authenticated: true });
    await installEmptyRestFallbacks(page);
  });

  test('dashboard hydrates first-party read models through real REQ/EVENT/EOSE', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/');

    await expect(page.locator('.card:has-text("Services") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Environments") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Workers") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Drifted") .card-value')).toHaveText('1');
    await expect(page.getByText('service.created')).toBeVisible();
    await assertNoRuntimeErrors();
  });

  test('service, deployment, package, and worker routes render seeded relay projections', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/services');
    await expect(page.getByText('Checkout API')).toBeVisible();

    await page.goto('/deployments');
    await expect(page.locator('body')).toContainText(/intent-1|run-1|Checkout API/);

    await page.goto('/packages');
    await expect(page.locator('body')).toContainText('packages');

    await page.goto('/workers');
    await expect(page.getByText('worker-one')).toBeVisible();
    await assertNoRuntimeErrors();
  });

  test('DNS and FIPS mesh pages use relay metadata and DNS read models', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/dns');
    await expect(page.getByRole('heading', { name: 'DNS management' })).toBeVisible();
    await expect(page.getByText('prod.example.com')).toBeVisible();

    await page.getByRole('button', { name: 'Endpoints' }).click();
    await expect(page.getByText('checkout.prod.example.com')).toBeVisible();

    await page.getByRole('button', { name: 'FIPS/Mesh' }).click();
    await expect(page.getByRole('heading', { name: 'FIPS mesh' })).toBeVisible();
    await expect(page.getByRole('cell', { name: /worker-one/ })).toBeVisible();
    await assertNoRuntimeErrors();
  });

  test('events consume relay-backed audit events', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/events');
    await expect(page.getByRole('heading', { name: 'Live Events' })).toBeVisible();
    await expect(page.getByText('service.created')).toBeVisible();
    await expect(page.locator('.hint')).toContainText(relay.wsUrl);
    await assertNoRuntimeErrors();
  });

  test('assistant panel hydrates relay-backed session and status events', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/');
    await page.evaluate(async () => {
      const { toggleAssistantPanel } = await import('/src/lib/stores/assistant.svelte.js');
      toggleAssistantPanel();
    });

    const panel = page.locator('.assistant-panel[data-state="open"]');
    await expect(panel).toBeVisible();
    await expect(panel).toContainText('Assistant');
    await expect(panel).toContainText('live');
    await expect(panel).toContainText('Relay-backed assistant is ready.');
    await assertNoRuntimeErrors();
  });

  test('Soul Factory hydrates template, soul, draft, and runtime capability events', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/souls');

    await expect(page.getByRole('heading', { name: 'Soul Gallery' })).toBeVisible();
    await expect(page.locator('.card:has-text("Total Souls") .card-value')).toHaveText('1');
    await expect(page.locator('.card:has-text("Runtime Targets") .card-value')).toHaveText('1');
    await expect(page.getByRole('link', { name: /Scout/ })).toContainText('Relay-backed research assistant');
    await expect(page.getByText('local-runtime')).toBeVisible();
    await assertNoRuntimeErrors();
  });

  test('encrypted ContextVM mutation publishes to relay, verifies OK, and observes encrypted result', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page, {
      allowConsole: ['WebSocket is already in CLOSING or CLOSED state.']
    });

    await page.goto('/');
    const response = await page.evaluate(async ({ relayUrl, servicePubkey }) => {
      const {
        CONTEXTVM_GIFT_WRAP_KIND,
        requestEncryptedResult
      } = await import('/src/lib/nostr/encrypted-controlplane.js');
      const { initializeAuth } = await import('/src/lib/stores/auth.js');

      await initializeAuth();

      const result = await requestEncryptedResult({
        operation: 'services.secrets.list',
        payload: { service_id: 'svc-1' },
        tags: [['domain', 'service-secrets']],
        kind: CONTEXTVM_GIFT_WRAP_KIND,
        resultKinds: [CONTEXTVM_GIFT_WRAP_KIND],
        transport: {
          relays: [relayUrl],
          servicePubkey
        },
        timeoutMs: 10_000
      });

      return {
        requestKind: result.event.kind,
        resultKind: result.resultEvent.kind,
        requestAccepted: result.acceptedRelays.length,
        requestRejected: result.rejectedRelays.length,
        secretName: result.result?.payload?.secrets?.[0]?.name || ''
      };
    }, { relayUrl: relay.wsUrl, servicePubkey: relay.servicePubkey });

    expect(response).toMatchObject({
      requestKind: 1059,
      resultKind: 1059,
      requestAccepted: 1,
      requestRejected: 0,
      secretName: 'RELAY_BACKED_SECRET'
    });
    await assertNoRuntimeErrors();
  });

  test('docs fetch bypasses stale cache and reads relay-backed NIP-23 topic deterministically', async ({ page }) => {
    const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

    await page.goto('/');
    const docsProbe = await page.evaluate(async (relayUrl) => {
      localStorage.setItem('bahia_docs_cache', JSON.stringify({ cachedAt: Date.now(), events: [] }));
      const { fetchDoc, fetchDocsCatalog } = await import('/src/lib/docs/nostr.js');
      const { nostr } = await import('/src/lib/nostr/subscriptions.js');
      await nostr.connect([relayUrl], { force: true });
      const rawEvents = await nostr.queryUntilEose([{ kinds: [30023], limit: 10 }]);
      const catalog = await fetchDocsCatalog({ bypassCache: true, timeoutMs: 10_000 });
      const doc = await fetchDoc('features-services', {
        bypassCache: true,
        timeoutMs: 10_000
      });
      return {
        relayConfig: localStorage.getItem('bahia_nostr_relays'),
        rawKinds: rawEvents.map((event) => ({ kind: event.kind, tags: event.tags })),
        catalogCount: catalog.count,
        topics: catalog.topics.map((topic) => topic.topic),
        doc
      };
    }, relay.wsUrl);

    expect(docsProbe.doc?.metadata?.topic, JSON.stringify(docsProbe)).toBe('features-services');
    expect(docsProbe.doc?.markdown).toContain('Relay-backed service documentation.');
    await assertNoRuntimeErrors();
  });
});
