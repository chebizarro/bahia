import { test, expect } from '@playwright/test';
import { E2E_SERVICE_PUBKEY, installE2EMocks } from './helpers.js';

const FACTORY_PUBKEY = E2E_SERVICE_PUBKEY;
const AGENT_PUBKEY = 'c'.repeat(64);
const RUNTIME_PUBKEY = 'd'.repeat(64);
const BROWSER_RELAY = 'ws://relay.test.local';

const systemInfo = {
  nostr: {
    browser_relays: [BROWSER_RELAY],
    service_pubkey: FACTORY_PUBKEY
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

async function fillRequiredIdentity(page, { name, agentId, brief }) {
  await page.getByLabel('Name').fill(name);
  await page.getByLabel('Agent ID').fill(agentId);
  await page.getByLabel('Purpose').fill(brief);
}

async function openPreview(page) {
  await page.getByRole('button', { name: /Runtime/ }).click();
  await page.getByRole('button', { name: /Preview draft/ }).click();
  await expect(page.locator('.wizard-progress .progress-step[aria-current="step"]:has-text("Preview")')).toBeVisible();
}

function provisioningResultEvent({ requestId, soulId, npub }) {
  return {
    id: `result-${requestId}`,
    kind: 7950,
    pubkey: FACTORY_PUBKEY,
    created_at: Math.floor(Date.now() / 1000) + 1,
    tags: [
      ['e', requestId, '', 'reply'],
      ['p', 'f'.repeat(64)],
      ['status', 'success'],
      ['soul', `31951:${FACTORY_PUBKEY}:${soulId}`],
      ['agent-pubkey', AGENT_PUBKEY],
      ['npub', npub]
    ],
    content: JSON.stringify({
      soul_id: soulId,
      npub,
      pubkey: AGENT_PUBKEY,
      avatar_url: '',
      workspace_url: '',
      qdrant_collection: ''
    })
  };
}

function soulEvent({ agentId, name, purpose }) {
  return {
    id: `soul-${agentId}`,
    kind: 31951,
    pubkey: FACTORY_PUBKEY,
    created_at: Math.floor(Date.now() / 1000) + 2,
    content: `# ${name}`,
    tags: [
      ['d', agentId],
      ['name', name],
      ['purpose', purpose],
      ['tier', 'standard'],
      ['status', 'active'],
      ['p', AGENT_PUBKEY, 'agent'],
      ['npub', `npub1${agentId}`],
      ['deploy-status', 'healthy']
    ]
  };
}

test('A newly provisioned soul becomes visible through relay-backed browsing', async ({ page }) => {
  await installE2EMocks(page, { authenticated: true, extension: true, nostrEvents: [runtimeCapabilityEvent()], systemInfo });

  await page.goto('/souls/new');
  await fillRequiredIdentity(page, {
    name: 'Scout Prime',
    agentId: 'scout-prime',
    brief: 'Guide release operators safely'
  });
  await openPreview(page);

  await page.getByRole('button', { name: 'Provision Soul' }).click();
  await expect(page.getByText('Event Signed & Published')).toBeVisible();

  const requestId = (await page.locator('dt:has-text("Request ID:") + dd code').textContent())?.trim();
  if (!requestId) {
    throw new Error('request id was not rendered');
  }

  await page.evaluate(({ resultEvent, publishedSoul }) => {
    window.__bahiaPushNostrEvent(resultEvent);
    window.__bahiaPushNostrEvent(publishedSoul);
  }, {
    resultEvent: provisioningResultEvent({ requestId, soulId: 'scout-prime', npub: 'npub1scoutprime' }),
    publishedSoul: soulEvent({ agentId: 'scout-prime', name: 'Scout Prime', purpose: 'Guide release operators safely' })
  });

  await expect(page.getByText('Provisioning Complete!')).toBeVisible();

  await page.goto('/souls');

  const scoutCard = page.getByRole('link', { name: /Scout Prime/ });
  await expect(scoutCard).toBeVisible();
  await expect(scoutCard).toContainText('Guide release operators safely');
});
