import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { createPublicState, createPublicSystemInfo, installPublicServiceDeploymentHarness, PUBLIC_RELAY, SERVICE_PUBKEY } from './harnesses/service-deployment-public.js';

const ENCRYPTED_RELAY = 'ws://encrypted.test.local';

function deploymentHistoryState() {
  const services = [
    {
      id: 'service-1',
      name: 'web-app',
      repo_url: '',
      artifact_repo: 'ghcr.io/test/web-app',
      runtime_type: 'docker',
      default_branch: 'main',
      deleted: false,
      created_at: '2026-05-01T10:00:00.000Z'
    },
    {
      id: 'service-2',
      name: 'api-service',
      repo_url: '',
      artifact_repo: 'ghcr.io/test/api-service',
      runtime_type: 'docker',
      default_branch: 'main',
      deleted: false,
      created_at: '2026-05-01T11:00:00.000Z'
    }
  ];

  const environments = [
    { id: 'env-1', name: 'production', protected: true, deleted: false, created_at: '2026-05-01T10:00:00.000Z' },
    { id: 'env-2', name: 'staging', protected: false, deleted: false, created_at: '2026-05-01T10:00:00.000Z' }
  ];

  const builds = [
    {
      id: 'build-1',
      service_id: 'service-1',
      git_sha: '0123456789abcdef0123456789abcdef01234567',
      git_ref: 'refs/heads/main',
      status: 'succeeded',
      ci_system: 'hive-ci',
      created_at: '2026-05-01T10:05:00.000Z'
    }
  ];

  const artifacts = [
    {
      id: 'artifact-rollback-1',
      service_id: 'service-1',
      build_id: 'build-1',
      image_repo: 'ghcr.io/test/web-app',
      image_tag: 'v1.2.0',
      image_digest: 'sha256:1111111111111111111111111111111111111111111111111111111111111111',
      metadata: { build_id: 'build-1' },
      created_at: '2026-05-01T10:06:00.000Z'
    },
    {
      id: 'artifact-rollback-2',
      service_id: 'service-1',
      build_id: 'build-1',
      image_repo: 'ghcr.io/test/web-app',
      image_tag: 'v1.1.0',
      image_digest: 'sha256:2222222222222222222222222222222222222222222222222222222222222222',
      metadata: { build_id: 'build-1' },
      created_at: '2026-04-25T10:06:00.000Z'
    }
  ];

  const deploymentIntents = Array.from({ length: 30 }, (_, index) => ({
    id: `intent-page-${index + 1}`,
    service_id: index < 26 ? 'service-1' : 'service-2',
    environment_id: index < 28 ? 'env-1' : 'env-2',
    artifact_id: index % 2 === 0 ? 'artifact-rollback-1' : 'artifact-rollback-2',
    approval_status: 'approved',
    deployment_status: index === 0 ? 'running' : 'succeeded',
    requested_by: `user-${index + 1}@example.com`,
    created_at: `2026-04-${String((index % 28) + 1).padStart(2, '0')}T10:00:00.000Z`,
    updated_at: `2026-04-${String((index % 28) + 1).padStart(2, '0')}T10:30:00.000Z`
  }));

  const deploymentRuns = [
    {
      id: 'run-completed-1',
      deployment_intent_id: 'intent-page-2',
      intent_id: 'intent-page-2',
      status: 'succeeded',
      exit_code: 0,
      worker_pubkey: 'c'.repeat(64),
      started_at: '2026-05-01T10:12:00.000Z',
      finished_at: '2026-05-01T10:13:10.000Z',
      created_at: '2026-05-01T10:12:00.000Z'
    }
  ];

  return createPublicState({
    services,
    environments,
    builds,
    artifacts,
    deploymentIntents,
    deploymentRuns,
    nextIntentId: 31,
    nextRunId: 2
  });
}

const systemInfo = createPublicSystemInfo({
  publicRelay: PUBLIC_RELAY,
  servicePubkey: SERVICE_PUBKEY,
  extraFeatures: { encrypted_nostr_requests: true }
});
systemInfo.nostr.browser_relays = [ENCRYPTED_RELAY];

async function installEncryptedRunLogHarness(page) {
  await page.addInitScript(({ servicePubkey, encryptedRelay }) => {
    window.__BAHIA_E2E_ENCRYPTED_PUBLISHES = [];
    window.__BAHIA_E2E_ENCRYPTED_OPERATIONS = [];

    window.nostr = {
      ...(window.nostr || {}),
      nip44: {
        encrypt: async (_recipientPubkey, plaintext) => `enc44:${plaintext}`,
        decrypt: async (_senderPubkey, ciphertext) => {
          if (typeof ciphertext !== 'string' || !ciphertext.startsWith('enc44:')) {
            throw new Error('bad ciphertext');
          }
          return ciphertext.slice('enc44:'.length);
        }
      }
    };

    function matchesFilter(event, filter) {
      if (!filter || typeof filter !== 'object') return true;
      if (Array.isArray(filter.kinds) && !filter.kinds.includes(event.kind)) return false;
      if (Array.isArray(filter.authors) && !filter.authors.includes(event.pubkey)) return false;
      for (const [key, values] of Object.entries(filter)) {
        if (!key.startsWith('#') || !Array.isArray(values)) continue;
        const tagName = key.slice(1);
        const tags = Array.isArray(event.tags) ? event.tags : [];
        if (!tags.some((tag) => Array.isArray(tag) && tag[0] === tagName && values.includes(tag[1]))) {
          return false;
        }
      }
      return true;
    }

    const OriginalWebSocket = window.WebSocket;
    const originalSend = OriginalWebSocket.prototype.send;

    OriginalWebSocket.prototype.send = function patchedSend(data) {
      let message;
      try {
        message = JSON.parse(data);
      } catch {
        return originalSend.call(this, data);
      }

      if (Array.isArray(message) && message[0] === 'REQ') {
        this.__bahiaSubs ??= new Map();
        this.__bahiaSubs.set(message[1], message.slice(2));
        return originalSend.call(this, data);
      }

      if (Array.isArray(message) && message[0] === 'CLOSE') {
        this.__bahiaSubs?.delete(message[1]);
        return originalSend.call(this, data);
      }

      if (Array.isArray(message) && message[0] === 'EVENT' && message[1]?.kind === 5980) {
        const event = message[1];
        const relay = this.url;
        const plaintext = String(event.content || '').replace(/^enc44:/, '');
        const envelope = JSON.parse(plaintext);

        if (envelope.operation !== 'deployments.run_logs.get') {
          return originalSend.call(this, data);
        }

        window.__BAHIA_E2E_ENCRYPTED_PUBLISHES.push({ relay, eventId: event.id });
        window.__BAHIA_E2E_ENCRYPTED_OPERATIONS.push(envelope.operation);
        originalSend.call(this, data);

        const resultEvent = {
          id: `result-${event.id}`,
          kind: 7980,
          pubkey: servicePubkey,
          created_at: Math.floor(Date.now() / 1000),
          tags: [
            ['e', event.id],
            ['p', event.pubkey],
            ['encrypted', 'bahia-encrypted-v1']
          ],
          content: `enc44:${JSON.stringify({
            request_event_id: event.id,
            status: 'ok',
            payload: {
              logs: {
                stdout: 'deploy started\ndeploy complete',
                stderr: 'warning: none'
              }
            }
          })}`,
          sig: '0'.repeat(128)
        };

        setTimeout(() => {
          if (this.readyState !== OriginalWebSocket.OPEN) return;
          const subs = this.__bahiaSubs || new Map();
          for (const [subId, filters] of subs.entries()) {
            if (Array.isArray(filters) && filters.some((filter) => matchesFilter(resultEvent, filter))) {
              this.onmessage?.({ data: JSON.stringify(['EVENT', subId, resultEvent]) });
            }
          }
        }, 0);

        return;
      }

      return originalSend.call(this, data);
    };

    window.__BAHIA_E2E_ENCRYPTED_EXPECTED_RELAY = encryptedRelay;
  }, { servicePubkey: SERVICE_PUBKEY, encryptedRelay: ENCRYPTED_RELAY });
}

test.beforeEach(async ({ page }) => {
  await installE2EMocks(page, { systemInfo });
  await installPublicServiceDeploymentHarness(page, { initialState: deploymentHistoryState() });
  await installEncryptedRunLogHarness(page);
});

test.describe('Deployment history and run details current-contract smoke', () => {
  test('filters and paginates deployment history from relay-backed state', async ({ page }) => {
    await page.goto('/deployments');

    await expect(page.locator('h1:has-text("Deployment History")')).toBeVisible();
    await expect(page.locator('#status-filter')).toBeVisible();
    await expect(page.locator('#service-filter')).toBeVisible();
    await expect(page.locator('#environment-filter')).toBeVisible();
    await expect(page.locator('#start-date-filter')).toBeVisible();
    await expect(page.locator('#end-date-filter')).toBeVisible();

    await page.selectOption('#status-filter', 'running');
    await expect(page.locator('text=1 of 30 deployments')).toBeVisible();

    await page.selectOption('#status-filter', 'all');
    await expect(page.locator('text=30 of 30 deployments')).toBeVisible();
    await expect(page.locator('text=Page 1 of 2')).toBeVisible();
    await expect(page.locator('tbody tr')).toHaveCount(25);

    await page.getByRole('button', { name: 'Next' }).click();
    await expect(page.locator('text=Page 2 of 2')).toBeVisible();
    await expect(page.locator('tbody tr')).toHaveCount(5);

    await page.selectOption('#service-filter', 'service-2');
    await expect(page.locator('text=4 of 30 deployments')).toBeVisible();

    await page.selectOption('#environment-filter', 'env-2');
    await expect(page.locator('text=2 of 30 deployments')).toBeVisible();

    await page.fill('#start-date-filter', '2026-04-27');
    await expect(page.locator('text=No deployments match current filters')).toBeVisible();
  });

  test('creates a specific-artifact rollback intent from deployment history through public commands', async ({ page }) => {
    await page.goto('/deployments');

    const row = page.locator('tbody tr', { hasText: 'web-app' }).first();
    await row.getByRole('button', { name: 'Rollback' }).click();
    const dialog = page.getByRole('dialog', { name: 'Confirm Rollback' });
    await expect(dialog).toBeVisible();

    await dialog.getByLabel('Specific artifact').check();
    await dialog.locator('#rollback-artifact-history').selectOption('artifact-rollback-2');
    await dialog.getByRole('button', { name: 'Create Rollback Intent' }).click();

    await expect(dialog).not.toBeVisible();
    await expect.poll(() => page.evaluate(() => ({
      requestKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS],
      latestIntent: window.__BAHIA_E2E_PUBLIC_STATE.deploymentIntents[0]
    }))).toMatchObject({
      requestKinds: expect.arrayContaining([5961]),
      latestIntent: expect.objectContaining({
        service_id: 'service-1',
        environment_id: 'env-1',
        artifact_id: 'artifact-rollback-2'
      })
    });
  });

  test('loads completed run logs through encrypted request/result transport', async ({ page }) => {
    await page.goto('/deployments/runs/run-completed-1');

    await expect(page.getByRole('heading', { name: 'Deployment Run' })).toBeVisible();
    await expect(page.getByText('Transport: public relay run projection + encrypted Nostr request/result fetch for stored stdout/stderr snapshots.')).toBeVisible();
    await expect(page.locator('pre.logs')).toContainText('deploy started');

    await page.getByRole('button', { name: 'stderr' }).click();
    await expect(page.locator('pre.logs')).toContainText('warning: none');

    const encryptedTrace = await page.evaluate(() => ({
      relays: [...window.__BAHIA_E2E_ENCRYPTED_PUBLISHES.map((entry) => entry.relay)],
      operations: [...window.__BAHIA_E2E_ENCRYPTED_OPERATIONS],
      publicKinds: [...window.__BAHIA_E2E_PUBLIC_REQUEST_KINDS]
    }));

    expect(encryptedTrace.operations.length).toBeGreaterThanOrEqual(1);
    expect(encryptedTrace.operations.every((operation) => operation === 'deployments.run_logs.get')).toBe(true);
    expect(encryptedTrace.relays.length).toBeGreaterThanOrEqual(1);
    expect(encryptedTrace.relays.every((relay) => relay === 'ws://encrypted.test.local')).toBe(true);
    expect(encryptedTrace.publicKinds).not.toContain(5980);
  });
});
