// Regression for bahia-1wb8l: a mutation control must not be usable before the
// app can submit its signed intent, and the page a user is typing into must
// not be remounted by a repeated auth bootstrap.
//
// Both mutations run under CPU throttling, the condition that exposed the
// flakes on slow CI runners. The assertions themselves do not depend on speed:
// the relay holds system discovery back until the test releases it, and the
// page counts how often its mutation region is attached.
import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';
import { createLLMState, createLLMSystemInfo, installPublicLLMControlplaneHarness } from './harnesses/llm-controlplane-public.js';

const CPU_THROTTLING_RATE = 6;
const SYSTEM_DISCOVERY_KIND = 11316;
const ORG_REQUIRED = 'Select an organization before submitting this intent';
const systemInfoWithoutOrg = { nostr: { browser_relays: ['ws://relay.test.local'], service_pubkey: E2E_SERVICE_PUBKEY },
  features: { relay_sidecar: true, relay_read_models: true, encrypted_nostr_requests: true, legacy_sse: false } };

async function throttleCPU(page) {
  const cdp = await page.context().newCDPSession(page);
  await cdp.send('Emulation.setCPUThrottlingRate', { rate: CPU_THROTTLING_RATE });
}

/**
 * Make the mock relay withhold system discovery (the events and the EOSE of
 * every REQ that asks for it) until window.__releaseSystemDiscovery() runs.
 * Install after the mocks that define the relay socket.
 */
async function holdSystemDiscovery(page) {
  await page.addInitScript((discoveryKind) => {
    const MockRelaySocket = window.WebSocket;
    const sockets = [];
    let holding = true;
    window.WebSocket = class HeldDiscoverySocket extends MockRelaySocket {
      constructor(url) {
        super(url);
        this.discoverySubscriptions = new Set();
        this.heldMessages = [];
        sockets.push(this);
      }

      send(data) {
        try {
          const message = JSON.parse(data);
          if (message[0] === 'REQ' && message.slice(2).some(filter => filter?.kinds?.includes(discoveryKind))) {
            this.discoverySubscriptions.add(message[1]);
          }
        } catch { /* the relay ignores malformed frames too */ }
        super.send(data);
      }

      emitMessage(data) {
        if (holding) {
          const message = JSON.parse(data);
          const withheld = (message[0] === 'EVENT' && message[2]?.kind === discoveryKind) ||
            (message[0] === 'EOSE' && this.discoverySubscriptions.has(message[1]));
          if (withheld) { this.heldMessages.push(data); return; }
        }
        super.emitMessage(data);
      }
    };
    window.__releaseSystemDiscovery = () => {
      holding = false;
      for (const socket of sockets) for (const data of socket.heldMessages.splice(0)) socket.emitMessage(data);
    };
  }, SYSTEM_DISCOVERY_KIND);
}

/** Count how many times the element with this test id is attached to the document. */
async function countAttachments(page, testId) {
  await page.addInitScript((testId) => {
    window.__attachments = 0;
    let attached = null;
    new MutationObserver(() => {
      const current = document.querySelector(`[data-testid="${testId}"]`);
      if (current && current !== attached) window.__attachments += 1;
      attached = current;
    }).observe(document, { childList: true, subtree: true });
  }, testId);
}

function intentTag(intent, name) { return intent.tags.find(tag => tag[0] === name)?.[1]; }

async function signedIntent(page, domain, op) {
  const find = ({ domain, op }) => window.__BAHIA_E2E_SIGNED_INTENTS.find(event =>
    event.tags.some(tag => tag[0] === 'domain' && tag[1] === domain) &&
    event.tags.some(tag => tag[0] === 'op' && tag[1] === op)) || null;
  await expect.poll(() => page.evaluate(find, { domain, op })).not.toBeNull();
  return page.evaluate(find, { domain, op });
}

test('org-scoped mutation stays disabled until the organization is known, then submits', async ({ page }) => {
  const systemInfo = createLLMSystemInfo();
  await installPublicLLMControlplaneHarness(page, { initialState: createLLMState({
    routes: [{ id: 'llm-route-1', route_id: 'llm-route-1', name: 'chat-prod',
      gateway_config: { public_model: 'bahia/chat', path: '/v1/models/chat-prod' },
      created_at: '2026-05-04T00:00:00.000Z' }],
    releases: [{ id: 'llm-release-1', route_id: 'llm-route-1', version: 'v1',
      model_ref: 'hf://meta-llama/Llama-3', created_at: '2026-05-04T00:05:00.000Z' }],
    routeStates: [{ route_id: 'llm-route-1', environment_id: 'env-prod',
      desired_release_id: 'llm-release-1', drift_status: 'in_sync', gateway_status: 'synced',
      updated_at: '2026-05-04T00:10:00.000Z' }]
  }) });
  await holdSystemDiscovery(page);
  await throttleCPU(page);
  await page.goto('/llm');

  // The route is on screen, but nothing has named the organization yet.
  const routeState = page.getByTestId('llm-route-state-table');
  const rollback = routeState.getByRole('button', { name: 'Rollback' }).first();
  await expect(routeState).toContainText('chat-prod');
  const gate = page.locator('fieldset[data-intent-domain="llm"]', { has: routeState });
  await expect(gate).toHaveAttribute('aria-busy', 'true');
  await expect(gate).toHaveAccessibleDescription('Connecting…');
  await expect(rollback).toBeDisabled();
  await expect(page.locator('input[name="route-name"]')).toBeDisabled();

  // A user who clicks now waits for the control instead of getting a failure.
  const clicked = rollback.click();
  await page.evaluate(() => window.__releaseSystemDiscovery());
  await clicked;

  await expect(page.getByTestId('llm-pending-intents')).toContainText('Pending');
  await expect(page.getByTestId('llm-notice')).toHaveText('LLM rollback intent pending daemon acceptance');
  const intent = await signedIntent(page, 'llm', 'rollback');
  expect(intentTag(intent, 'org')).toBe(systemInfo.organization_id);
  await expect(gate).toHaveAttribute('data-intent-ready', 'true');
  await expect(gate).not.toHaveAttribute('aria-describedby');
});

test('fleet-scoped mutation keeps what was typed while the session finishes booting', async ({ page }) => {
  await installE2EMocks(page, { systemInfo: systemInfoWithoutOrg });
  await countAttachments(page, 'dns-registry-mutations');
  await throttleCPU(page);
  await page.goto('/dns');
  await page.evaluate(() => { window.__BAHIA_E2E_INTENT_AUTO_STATUS = false; });

  const registry = page.getByTestId('dns-registry-mutations');
  const backend = registry.locator('form').nth(2);
  await backend.getByLabel('Reference').fill('tertiary');
  await backend.getByRole('button', { name: 'Create backend' }).click();

  await expect(page.getByTestId('dns-pending-intents')).toContainText('Pending');
  await expect(registry.getByRole('status')).toHaveText('backend-create intent pending daemon acceptance');
  const intent = await signedIntent(page, 'dns', 'backend-create');
  expect(intentTag(intent, 'd')).toBe('dnsbackend:tertiary');
  expect(JSON.parse(intent.content)).toMatchObject({ ref: 'tertiary', type: 'coredns' });

  // The session is fully booted now. The page the user typed into is still the
  // one that was first rendered: it was attached exactly once.
  await expect(page.locator('fieldset[data-intent-domain="dns"]', { has: registry })).toHaveAttribute('data-intent-ready', 'true');
  await expect(backend.getByLabel('Reference')).toHaveValue('tertiary');
  expect(await page.evaluate(() => window.__attachments)).toBe(1);
});

test('an organization that can never be resolved still reports the explicit error', async ({ page }) => {
  await installE2EMocks(page, { systemInfo: systemInfoWithoutOrg });
  await page.goto('/llm');

  // Discovery and relay catch-up finished without naming an organization, so
  // the form is usable and submitting says what the operator has to do.
  const form = page.getByTestId('llm-create-route-form');
  const gate = page.locator('fieldset[data-intent-domain="llm"]', { has: form });
  await expect(gate).toHaveAttribute('aria-busy', 'false');
  await expect(gate).toHaveAttribute('data-intent-ready', 'false');
  await form.locator('input[name="route-name"]').fill('chat-prod');
  await form.locator('input[name="public-model"]').fill('bahia/chat');
  await form.getByRole('button', { name: 'Create route' }).click();

  await expect(page.getByTestId('llm-notice')).toHaveText(ORG_REQUIRED);
  await expect(page.getByTestId('llm-pending-intents')).toHaveCount(0);
  expect(await page.evaluate(() => window.__BAHIA_E2E_SIGNED_INTENTS.length)).toBe(0);
});
