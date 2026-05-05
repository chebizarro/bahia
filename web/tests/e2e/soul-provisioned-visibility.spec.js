import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const FACTORY_PUBKEY = 'b'.repeat(64);
const AGENT_PUBKEY = 'c'.repeat(64);

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
  await installE2EMocks(page, { authenticated: true, extension: true, nostrEvents: [] });

  await page.goto('/souls/new');
  await page.getByRole('button', { name: 'Continue' }).click();
  await page.getByRole('button', { name: 'Continue' }).click();

  await page.fill('#agentName', 'Scout Prime');
  await page.fill('#agentId', 'scout-prime');
  await page.fill('#brief', 'Guide release operators safely');

  await page.getByRole('button', { name: 'Provision Soul' }).click();
  await expect(page.getByText('Event Signed & Published')).toBeVisible();

  const requestId = (await page.locator('.event-details code').first().textContent())?.trim();
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

  await expect(page.getByText('Scout Prime')).toBeVisible();
  await expect(page.getByText('Guide release operators safely')).toBeVisible();
});
