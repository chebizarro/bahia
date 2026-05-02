import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

test.describe('Critical deployment workflow E2E', () => {
  test('covers create service, deploy intent, approve, run logs, and deployment history', async ({ page }) => {
    await installE2EMocks(page, { authenticated: true, extension: true });

    const serviceId = 'svc-critical-1';
    const intentId = 'intent-critical-1';
    const runId = 'run-critical-1';

    let services = [];
    let intentApproved = false;
    let intentCreated = false;

    const environments = [
      { id: 'env-prod', name: 'production', protected: true }
    ];

    const builds = [
      {
        id: 'build-critical-1',
        git_sha: '0123456789abcdef0123456789abcdef01234567',
        git_ref: 'refs/heads/main',
        status: 'succeeded',
        ci_system: 'hive-ci'
      }
    ];

    const artifacts = [
      {
        id: 'artifact-critical-1',
        service_id: serviceId,
        build_id: 'build-critical-1',
        image_repo: 'ghcr.io/test/critical-service',
        image_tag: 'v2.0.0',
        image_digest: 'sha256:abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcd',
        metadata: { build_id: 'build-critical-1' },
        created_at: '2026-05-02T10:00:00.000Z'
      }
    ];

    await page.route('**/api/v1/system/info', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: {
          registries: [],
          nostr: { browser_relays: [], service_pubkey: 'b'.repeat(64) },
          features: {
            relay_sidecar: true,
            relay_read_models: true,
            legacy_sse: false,
            direct_nostr_http_auth: true
          }
        }
      })
    }));

    await page.route('**/api/v1/repositories*', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    }));

    await page.route('**/api/v1/services', async (route) => {
      const method = route.request().method();

      if (method === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: services })
        });
      }

      if (method === 'POST') {
        const payload = route.request().postDataJSON();
        services = [{
          id: serviceId,
          ...payload,
          created_at: '2026-05-02T10:05:00.000Z'
        }];

        return route.fulfill({
          status: 201,
          contentType: 'application/json',
          body: JSON.stringify({ data: services[0] })
        });
      }

      return route.fallback();
    });

    await page.route(`**/api/v1/services/${serviceId}*`, async (route) => {
      const method = route.request().method();
      if (method === 'GET') {
        return route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ data: services[0] })
        });
      }
      return route.fallback();
    });

    await page.route(`**/api/v1/services/${serviceId}/builds`, (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: builds })
    }));

    await page.route(`**/api/v1/services/${serviceId}/artifacts`, (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: artifacts })
    }));

    await page.route(`**/api/v1/services/${serviceId}/secrets`, (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    }));

    await page.route('**/api/v1/environments', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: environments })
    }));

    await page.route('**/api/v1/environments/env-prod', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: environments[0] })
    }));

    await page.route('**/api/v1/workers', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    }));

    await page.route('**/api/v1/state', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: [] })
    }));

    await page.route('**/api/v1/policies/evaluate', (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: {
          allowed: true,
          warnings: 0,
          blockers: 0,
          results: [
            { policy_id: 'policy-signatures', policy_name: 'Signature required', passed: true, enforcement: 'block' }
          ]
        }
      })
    }));

    await page.route('**/api/v1/deployments/intents', async (route) => {
      if (route.request().method() !== 'POST') return route.fallback();
      intentCreated = true;

      const payload = route.request().postDataJSON();
      return route.fulfill({
        status: 201,
        contentType: 'application/json',
        body: JSON.stringify({
          data: {
            id: intentId,
            ...payload,
            approval_status: 'pending',
            deployment_status: 'pending',
            requested_by: 'critical@example.com',
            created_at: '2026-05-02T10:10:00.000Z'
          }
        })
      });
    });

    await page.route('**/api/v1/services/*/environments/*/intents', (route) => {
      const url = route.request().url();
      const match = url.match(/services\/([^/]+)\/environments\/([^/]+)\/intents/);
      if (!match) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) });
      }
      const [_, routedServiceId, routedEnvId] = match;
      if (routedServiceId !== serviceId || routedEnvId !== 'env-prod' || !intentCreated) {
        return route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ data: [] }) });
      }

      const deployment_status = intentApproved ? 'succeeded' : 'pending';
      const approval_status = intentApproved ? 'approved' : 'pending';
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({
          data: [{
            id: intentId,
            service_id: serviceId,
            environment_id: 'env-prod',
            artifact_id: 'artifact-critical-1',
            approval_status,
            deployment_status,
            requested_by: 'critical@example.com',
            created_at: '2026-05-02T10:10:00.000Z',
            updated_at: '2026-05-02T10:12:00.000Z'
          }]
        })
      });
    });

    await page.route(`**/api/v1/deployments/intents/${intentId}`, (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: {
          id: intentId,
          service_id: serviceId,
          environment_id: 'env-prod',
          artifact_id: 'artifact-critical-1',
          approval_status: intentApproved ? 'approved' : 'pending',
          deployment_status: intentApproved ? 'succeeded' : 'pending',
          requested_by: 'critical@example.com',
          created_at: '2026-05-02T10:10:00.000Z',
          updated_at: '2026-05-02T10:12:00.000Z'
        }
      })
    }));

    await page.route(`**/api/v1/deployments/intents/${intentId}/approve`, (route) => {
      intentApproved = true;
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: { status: 'approved' } })
      });
    });

    await page.route(`**/api/v1/deployments/intents/${intentId}/runs`, (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: intentApproved
          ? [{ id: runId, status: 'succeeded', worker_pubkey: 'f'.repeat(64), exit_code: 0, created_at: '2026-05-02T10:12:00.000Z', finished_at: '2026-05-02T10:13:10.000Z' }]
          : []
      })
    }));

    await page.route(`**/api/v1/deployments/runs/${runId}`, (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({
        data: {
          id: runId,
          deployment_intent_id: intentId,
          status: 'succeeded',
          exit_code: 0,
          worker_pubkey: 'f'.repeat(64),
          started_at: '2026-05-02T10:12:00.000Z',
          finished_at: '2026-05-02T10:13:10.000Z'
        }
      })
    }));

    await page.route(`**/api/v1/deployments/runs/${runId}/logs?tail=500&stream=stdout`, (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: { stdout: 'deploy started\ndeploy complete' } })
    }));

    await page.route(`**/api/v1/deployments/runs/${runId}/logs?tail=500&stream=stderr`, (route) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: { stderr: 'warning: none' } })
    }));

    await page.goto('/services');
    await page.getByRole('button', { name: 'Create Service' }).first().click();
    await expect(page.getByRole('dialog', { name: 'Create Service' })).toBeVisible();
    await page.fill('#service-name', 'critical-service');
    await page.fill('#artifact-repo-path', 'ghcr.io/test/critical-service');
    await page.click('button[type="submit"]:has-text("Create")');
    await expect(page.getByRole('dialog', { name: 'Create Service' })).not.toBeVisible();
    await expect(page.getByRole('cell', { name: 'critical-service', exact: true })).toBeVisible();

    await page.goto(`/services/${serviceId}`);
    await expect(page.getByRole('heading', { name: 'critical-service' })).toBeVisible();
    await page.getByRole('button', { name: 'Deploy' }).click();
    await expect(page.getByRole('dialog', { name: 'Create Deployment Intent' })).toBeVisible();
    await page.locator('#deploy-environment').selectOption('env-prod');
    await page.locator('#deploy-artifact').selectOption('artifact-critical-1');
    await page.getByRole('button', { name: 'Create Intent' }).click();
    await expect(page).toHaveURL(/\/deployments$/);

    await page.goto('/deployments/pending');
    await expect(page.locator('text=critical-service')).toBeVisible();
    await page.locator('button:has-text("Approve")').first().click();
    await expect(page.getByRole('dialog', { name: 'Approve Deployment' })).toBeVisible();
    await page.getByRole('dialog', { name: 'Approve Deployment' }).getByRole('button', { name: 'Approve' }).click();
    await expect(page.locator('text=No pending approvals')).toBeVisible();

    await page.goto(`/deployments/${intentId}`);
    await expect(page.locator('h1:has-text("Deployment Intent")')).toBeVisible();
    await page.locator('tbody tr').first().click();
    await expect(page).toHaveURL(`/deployments/runs/${runId}`);

    await expect(page.locator('h1:has-text("Deployment Run")')).toBeVisible();
    await expect(page.locator('pre.logs')).toContainText('deploy started');

    await page.getByRole('button', { name: 'stderr' }).click();
    await expect(page.locator('pre.logs')).toContainText('warning: none');

    // Nearest currently supported equivalent to rollback in this UI: verify completed deployment appears in history.
    await page.goto('/deployments');
    await expect(page.locator('h1:has-text("Deployment History")')).toBeVisible();
    await expect(page.locator('tbody')).toContainText('critical-service');
  });
});
