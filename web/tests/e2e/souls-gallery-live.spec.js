import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const FACTORY_PUBKEY = 'b'.repeat(64);
const AGENT_PUBKEY = 'c'.repeat(64);

function soulEvent({ id, agentId, name, purpose, status = 'active', deployStatus = '', createdAt }) {
  const tags = [
    ['d', agentId],
    ['name', name],
    ['purpose', purpose],
    ['tier', 'standard'],
    ['status', status],
    ['p', AGENT_PUBKEY, 'agent'],
    ['npub', `npub1${agentId}`]
  ];
  if (deployStatus) {
    tags.push(['deploy-status', deployStatus]);
  }
  return {
    id,
    kind: 31951,
    pubkey: FACTORY_PUBKEY,
    created_at: createdAt,
    content: `# ${name}`,
    tags
  };
}

test('Souls gallery bootstraps from relays and reflects live soul updates', async ({ page }) => {
  const now = Math.floor(Date.now() / 1000);
  const bootstrapEvents = [
    soulEvent({ id: 'soul-alpha', agentId: 'alpha', name: 'Alpha', purpose: 'Investigate incidents', status: 'active', deployStatus: 'healthy', createdAt: now - 10 }),
    soulEvent({ id: 'soul-bravo', agentId: 'bravo', name: 'Bravo', purpose: 'Handle escalations', status: 'suspended', deployStatus: 'stopped', createdAt: now - 5 })
  ];

  await installE2EMocks(page, { authenticated: true, extension: true, nostrEvents: bootstrapEvents });

  await page.goto('/souls');

  await expect(page.getByRole('heading', { name: 'Soul Gallery' })).toBeVisible();
  await expect(page.getByRole('link', { name: /Alpha/ })).toBeVisible();
  await expect(page.getByRole('link', { name: /Bravo/ })).toBeVisible();
  const statsGrid = page.locator('.stats-grid');
  await expect(statsGrid.getByText('Total Souls').locator('..')).toContainText('2');
  await expect(statsGrid.getByText('Suspended').locator('..')).toContainText('1');

  const liveUpdate = soulEvent({
    id: 'soul-bravo-live',
    agentId: 'bravo',
    name: 'Bravo Prime',
    purpose: 'Handle escalations with live updates',
    status: 'active',
    deployStatus: 'healthy',
    createdAt: now + 5
  });

  await page.evaluate((event) => window.__bahiaPushNostrEvent(event), liveUpdate);

  await expect(page.getByRole('link', { name: /Bravo Prime/ })).toBeVisible();
  await expect(page.getByText('Handle escalations with live updates')).toBeVisible();
  await expect(page.getByRole('link', { name: /Bravo(?! Prime)/ })).toHaveCount(0);
  await expect(statsGrid.getByText('Active').locator('..')).toContainText('2');
  await expect(statsGrid.getByText('Suspended').locator('..')).toContainText('0');
});
