import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';
import { attachRuntimeErrorGuards } from './helpers-console.js';

const BROWSER_RELAY = 'ws://relay.test.local';
const SERVICE_PUBKEY = 'b'.repeat(64);

const systemInfo = {
  nostr: {
    browser_relays: [BROWSER_RELAY],
    service_relays: [BROWSER_RELAY],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    legacy_sse: false,
    publish_enabled: true
  }
};

const fixtures = {
  service: {
    id: 'svc-1',
    name: 'Checkout API',
    slug: 'checkout-api',
    artifact_repo: 'registry.example/checkout',
    status: 'running'
  },
  environment: {
    id: 'env-1',
    name: 'Production',
    slug: 'production',
    status: 'active'
  },
  deployment: {
    id: 'dep-1',
    run_id: 'run-1',
    service_id: 'svc-1',
    environment_id: 'env-1',
    status: 'running'
  },
  artifact: {
    id: 'art-1',
    service_id: 'svc-1',
    digest: 'sha256:abc',
    status: 'available'
  },
  organization: {
    id: 'org-1',
    name: 'Acme'
  },
  package: {
    id: 'pkg-1',
    name: 'pkg-one',
    version: '1.0.0'
  },
  policy: {
    id: 'policy-1',
    name: 'Default policy'
  },
  notification: {
    id: 'notif-1',
    name: 'Ops alert',
    type: 'email',
    channel: 'email',
    target: 'ops@example.test'
  },
  soul: {
    id: 'soul-1',
    name: 'Scout',
    status: 'ready'
  },
  worker: {
    id: 'worker-1',
    pubkey: 'c'.repeat(64),
    status: 'online'
  }
};

const routeCases = [
  ['dashboard', '/'],
  ['services list', '/services'],
  ['service detail', '/services/svc-1'],
  ['environments list', '/environments'],
  ['environment detail', '/environments/env-1'],
  ['deployments list', '/deployments'],
  ['pending deployments', '/deployments/pending'],
  ['deployment detail', '/deployments/dep-1'],
  ['deployment run detail', '/deployments/runs/run-1'],
  ['artifacts list', '/artifacts'],
  ['artifact detail', '/artifacts/art-1'],
  ['backup overview', '/backup'],
  ['backup section', '/backup/plans'],
  ['backup section item', '/backup/plans/plan-1'],
  ['continuity', '/continuity'],
  ['dns', '/dns'],
  ['docs index', '/docs'],
  ['docs topic', '/docs/features-services'],
  ['events', '/events'],
  ['fleet health', '/fleet-health'],
  ['llm', '/llm'],
  ['ml', '/ml'],
  ['notifications list', '/notifications'],
  ['notification new', '/notifications/new'],
  ['notification log', '/notifications/log'],
  ['notification edit', '/notifications/notif-1/edit'],
  ['organizations list', '/orgs'],
  ['organization new', '/orgs/new'],
  ['organization detail', '/orgs/org-1'],
  ['packages list', '/packages'],
  ['package detail', '/packages/pkg-1'],
  ['payments', '/payments'],
  ['policies list', '/policies'],
  ['policy detail', '/policies/policy-1'],
  ['settings', '/settings'],
  ['profile settings', '/settings/profile'],
  ['relay settings', '/settings/relays'],
  ['souls list', '/souls'],
  ['soul new', '/souls/new'],
  ['soul detail', '/souls/soul-1'],
  ['soul edit', '/souls/soul-1/edit'],
  ['workers list', '/workers'],
  ['worker detail', `/workers/${fixtures.worker.pubkey}`]
];

function dataResponse(data) {
  return {
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ data })
  };
}

async function installApiMocks(page) {
  await page.route('http://relay.test.local/**', (route) => route.fulfill({
    status: 200,
    contentType: 'application/nostr+json',
    headers: {
      'Access-Control-Allow-Origin': '*'
    },
    body: JSON.stringify({
      name: 'Bahia E2E relay',
      supported_nips: [1, 11, 42, 65],
      limitation: { auth_required: false }
    })
  }));

  await page.route('**/api/v1/**', (route) => {
    const url = route.request().url();

    if (url.includes('/system/info')) {
      return route.fulfill(dataResponse({
        nostr: systemInfo.nostr,
        features: systemInfo.features
      }));
    }

    if (url.match(/\/services\/svc-1(?:\?|$)/)) return route.fulfill(dataResponse(fixtures.service));
    if (url.includes('/services')) return route.fulfill(dataResponse([fixtures.service]));

    if (url.match(/\/environments\/env-1(?:\?|$)/)) return route.fulfill(dataResponse(fixtures.environment));
    if (url.includes('/environments')) return route.fulfill(dataResponse([fixtures.environment]));

    if (url.match(/\/deployments\/(dep-1|runs\/run-1)(?:\?|$)/)) return route.fulfill(dataResponse(fixtures.deployment));
    if (url.includes('/deployments')) return route.fulfill(dataResponse([fixtures.deployment]));

    if (url.match(/\/artifacts\/art-1(?:\?|$)/)) return route.fulfill(dataResponse(fixtures.artifact));
    if (url.includes('/artifacts')) return route.fulfill(dataResponse([fixtures.artifact]));

    if (url.match(/\/(organizations|orgs)\/org-1(?:\?|$)/)) return route.fulfill(dataResponse(fixtures.organization));
    if (url.includes('/organizations') || url.includes('/orgs')) return route.fulfill(dataResponse([fixtures.organization]));

    if (url.match(/\/packages\/pkg-1(?:\?|$)/)) return route.fulfill(dataResponse(fixtures.package));
    if (url.includes('/packages')) return route.fulfill(dataResponse([fixtures.package]));

    if (url.match(/\/policies\/policy-1(?:\?|$)/)) return route.fulfill(dataResponse(fixtures.policy));
    if (url.includes('/policies')) return route.fulfill(dataResponse([fixtures.policy]));

    if (url.match(/\/notifications\/notif-1(?:\?|$)/)) return route.fulfill(dataResponse(fixtures.notification));
    if (url.includes('/notifications')) return route.fulfill(dataResponse([fixtures.notification]));

    if (url.match(/\/souls\/soul-1(?:\?|$)/)) return route.fulfill(dataResponse(fixtures.soul));
    if (url.includes('/souls')) return route.fulfill(dataResponse([fixtures.soul]));

    if (url.includes('/workers')) return route.fulfill(dataResponse([fixtures.worker]));

    return route.fulfill(dataResponse([]));
  });
}

test.describe('route console regressions', () => {
  test.beforeEach(async ({ page }) => {
    await installE2EMocks(page, {
      authenticated: true,
      extension: true,
      nostrEvents: [],
      systemInfo
    });
    await installApiMocks(page);
  });

  for (const [name, path] of routeCases) {
    test(`${name} renders without runtime console errors`, async ({ page }) => {
      const assertNoRuntimeErrors = await attachRuntimeErrorGuards(page);

      const response = await page.goto(path);
      expect(response?.ok(), `${path} should not return HTTP ${response?.status()}`).toBe(true);
      await page.waitForLoadState('networkidle');

      await expect(page.locator('body')).toBeVisible();
      await assertNoRuntimeErrors();
    });
  }
});
