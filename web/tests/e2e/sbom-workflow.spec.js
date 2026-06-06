import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = 'b'.repeat(64);
const ARTIFACT_ID = 'artifact-sbom-1';
const NO_SBOM_ARTIFACT_ID = 'artifact-no-sbom';
const SERVICE_ID = 'svc-sbom';

const KINDS = {
  SERVICE_REGISTRY: 30900,
  ARTIFACT_REGISTRY: 30900,
  SBOM_ATTESTATION: 30078,
  SBOM_INDEX: 30078,
  AUDIT: 4903
};

const relaySystemInfo = {
  nostr: {
    browser_relays: ['ws://relay.test.local'],
    service_pubkey: SERVICE_PUBKEY
  },
  features: {
    relay_sidecar: true,
    relay_read_models: true,
    legacy_sse: false
  }
};

function nostrEvent({ id, kind, pubkey = SERVICE_PUBKEY, created_at = Math.floor(Date.now() / 1000), tags = [], content = {} }) {
  return {
    id,
    kind,
    pubkey,
    created_at,
    tags,
    content: JSON.stringify(content),
    sig: '0'.repeat(128)
  };
}

function serviceEvent() {
  return nostrEvent({
    id: 'svc-sbom-event',
    kind: KINDS.SERVICE_REGISTRY,
    tags: [['domain', 'controlplane'], ['schema', 'bahia.registry.service.v1'], ['d', SERVICE_ID], ['deleted', 'false'], ['name', 'sbom-service']],
    content: {
      schema: 'bahia.registry.service.v1',
      id: SERVICE_ID,
      name: 'sbom-service',
      runtime_type: 'docker',
      deleted: false
    }
  });
}

function artifactEvent({ id = ARTIFACT_ID, packages = [] } = {}) {
  return nostrEvent({
    id: `${id}-event`,
    kind: KINDS.ARTIFACT_REGISTRY,
    tags: [['domain', 'controlplane'], ['schema', 'bahia.registry.artifact.v1'], ['d', id], ['artifact', id], ['service', SERVICE_ID], ['deleted', 'false']],
    content: {
      schema: 'bahia.registry.artifact.v1',
      id,
      service_id: SERVICE_ID,
      name: id === ARTIFACT_ID ? 'registry.example.com/bahia/sbom-demo' : 'registry.example.com/bahia/no-sbom',
      version: '1.2.3',
      artifact_type: 'container_image',
      image_tag: '1.2.3',
      digest: 'sha256:111122223333444455556666777788889999aaaabbbbccccddddeeeeffff0000',
      size_bytes: 52428800,
      sbom_packages: packages,
      created_at: '2026-05-13T12:00:00.000Z',
      deleted: false
    }
  });
}

function escapeRegExp(value) {
  return String(value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

async function mockSBOMEndpoints(page, artifactId, { sbom = null, attestation = null } = {}) {
  const escapedArtifactId = escapeRegExp(artifactId);
  await page.route(new RegExp(`/api/v1/artifacts/${escapedArtifactId}/sbom$`), (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ data: sbom })
  }));
  await page.route(new RegExp(`/api/v1/artifacts/${escapedArtifactId}/sbom/attestation$`), (route) => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ data: attestation })
  }));
}

async function openCreatePolicyDialog(page) {
  await page.goto('/policies');
  await expect(page.getByRole('heading', { name: 'Policies', exact: true })).toBeVisible();
  await page.getByRole('button', { name: 'Create Policy' }).first().click();
  const dialog = page.getByRole('dialog', { name: 'Create Policy' });
  await expect(dialog).toBeVisible();
  return dialog;
}

async function addVisualRule(page, dialog, ruleName, configure) {
  await dialog.getByRole('button', { name: '+ Add Rule' }).click();
  const builderModal = page.locator('.rule-builder .modal');
  await expect(builderModal.getByRole('heading', { name: 'Add Policy Rule' })).toBeVisible();
  await builderModal.getByRole('button', { name: /SBOM Requirements/ }).click();
  await builderModal.getByRole('button', { name: new RegExp(ruleName) }).click();
  if (configure) {
    await configure(builderModal);
  }
  await builderModal.getByRole('button', { name: /^Add Rule$/ }).click();
  await expect(builderModal).toBeHidden();
}

test.describe('SBOM workflow', () => {
  test('artifact page displays SBOM attestation details', async ({ page }) => {
    const sbomHash = 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa';
    const subjectDigest = 'bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb';
    const packages = [
      { name: 'openssl', version: '3.2.1', ecosystem: 'apk', license: 'Apache-2.0', purl: 'pkg:apk/alpine/openssl@3.2.1' },
      { name: 'svelte', version: '5.0.0', ecosystem: 'npm', license: 'MIT', purl: 'pkg:npm/svelte@5.0.0' }
    ];
    const sbom = {
      artifact_id: ARTIFACT_ID,
      format: 'spdx',
      generator: { id: 'syft', version: '0.95.0' },
      source_url: 'blossom://sboms/artifact-sbom-1.spdx.json',
      raw_hash: sbomHash,
      package_count: packages.length,
      packages,
      ntia: {
        isCompliant: true,
        hasSupplierName: true,
        hasComponentName: true,
        hasComponentVersion: true,
        hasUniqueID: true,
        hasRelationship: true,
        hasAuthor: true,
        hasTimestamp: true
      },
      created_at: '2026-05-13T12:05:00.000Z'
    };
    const attestation = {
      subject: [{ name: ARTIFACT_ID, digest: { sha256: subjectDigest } }],
      predicate: {
        format: 'spdx',
        generator: { id: 'syft', version: '0.95.0' },
        location: { type: 'blossom', uri: 'blossom://sboms/artifact-sbom-1.spdx.json' },
        digest: { sha256: sbomHash },
        timestamp: '2026-05-13T12:05:00.000Z',
        ntia: sbom.ntia
      }
    };

    await installE2EMocks(page, {
      systemInfo: relaySystemInfo,
      nostrEvents: [serviceEvent(), artifactEvent()]
    });
    await mockSBOMEndpoints(page, ARTIFACT_ID, { sbom, attestation });

    await page.goto(`/artifacts/${ARTIFACT_ID}`);
    await expect(page.getByRole('heading', { name: 'registry.example.com/bahia/sbom-demo' })).toBeVisible();
    await page.getByRole('button', { name: /^SBOM/ }).click();

    await expect(page.getByRole('heading', { name: 'Attestation Details' })).toBeVisible();
    await expect(page.locator('.attestation-item').filter({ hasText: 'Format' }).getByText('SPDX', { exact: true })).toBeVisible();
    await expect(page.getByText('syft@0.95.0')).toBeVisible();
    await expect(page.getByText('Blossom', { exact: true })).toBeVisible();
    await expect(page.getByText('blossom://sboms/artifact-sbom-1.spdx.json')).toBeVisible();
    await expect(page.locator(`code[title="${sbomHash}"]`)).toBeVisible();
    await expect(page.locator(`code[title="sha256:${subjectDigest}"]`)).toBeVisible();
    await expect(page.locator('.attestation-item:has-text("Package Count")')).toContainText('2');
    await expect(page.getByRole('heading', { name: 'NTIA Minimum Elements' })).toBeVisible();
    await expect(page.getByText('Compliant', { exact: true })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Packages (2)' })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'openssl', exact: true })).toBeVisible();
    await expect(page.getByRole('cell', { name: 'svelte', exact: true })).toBeVisible();
  });

  test('artifact SBOM tab shows an empty state when no attestation exists', async ({ page }) => {
    await installE2EMocks(page, {
      systemInfo: relaySystemInfo,
      nostrEvents: [serviceEvent(), artifactEvent({ id: NO_SBOM_ARTIFACT_ID })]
    });
    await mockSBOMEndpoints(page, NO_SBOM_ARTIFACT_ID);

    await page.goto(`/artifacts/${NO_SBOM_ARTIFACT_ID}`);
    await expect(page.getByRole('heading', { name: 'registry.example.com/bahia/no-sbom' })).toBeVisible();
    await page.getByRole('button', { name: /^SBOM/ }).click();

    await expect(page.getByRole('heading', { name: 'No SBOM available' })).toBeVisible();
    await expect(page.getByText('This artifact does not have an SBOM or it has not been ingested yet')).toBeVisible();
  });

  test('policy rule builder adds SBOM-related rules and syncs them to JSON', async ({ page }) => {
    await installE2EMocks(page, { systemInfo: relaySystemInfo });
    const dialog = await openCreatePolicyDialog(page);

    await dialog.locator('#policy-name').fill('sbom-policy');
    await expect(dialog.getByText('No rules configured. Add rules to define policy requirements.')).toBeVisible();

    await addVisualRule(page, dialog, 'Require SBOM');
    await addVisualRule(page, dialog, 'SBOM Format', async (builderModal) => {
      await builderModal.getByLabel('spdx').check();
      await builderModal.getByLabel('cyclonedx').check();
    });

    const rulesList = dialog.locator('.rules-list');
    await expect(rulesList.getByText('Require SBOM')).toBeVisible();
    await expect(rulesList.getByText('require_sbom')).toBeVisible();
    await expect(rulesList.getByText('SBOM Format')).toBeVisible();
    await expect(rulesList.getByText('formats:')).toBeVisible();
    await expect(rulesList.getByText('spdx, cyclonedx')).toBeVisible();

    await dialog.getByRole('button', { name: 'JSON Editor' }).click();
    const rules = JSON.parse(await dialog.locator('#rules').inputValue());
    expect(rules).toEqual([
      { type: 'require_sbom' },
      { type: 'sbom_format', params: { formats: ['spdx', 'cyclonedx'] } }
    ]);
  });

  test('policy visual builder reports an error when submitted without rules', async ({ page }) => {
    await installE2EMocks(page, { systemInfo: relaySystemInfo });
    const dialog = await openCreatePolicyDialog(page);

    await dialog.locator('#policy-name').fill('empty-sbom-policy');
    await dialog.getByRole('button', { name: /^Create$/ }).click();

    await expect(dialog.getByText('Please add at least one rule')).toBeVisible();
  });

  test('events page filters canonical SBOM events', async ({ page }) => {
    const now = Math.floor(Date.now() / 1000);
    const nostrEvents = [
      nostrEvent({
        id: 'sbom-attestation-event',
        kind: KINDS.SBOM_ATTESTATION,
        created_at: now,
        tags: [['domain', 'artifact'], ['schema', 'bahia.sbom.attestation.v1'], ['type', 'sbom.attestation'], ['op', 'sbom.attestation'], ['d', `${ARTIFACT_ID}:attestation`], ['artifact', ARTIFACT_ID]],
        content: {
          schema: 'bahia.sbom.attestation.v1',
          domain: 'artifact',
          event_type: 'sbom.attestation',
          artifact_id: ARTIFACT_ID,
          format: 'spdx',
          generator: 'syft'
        }
      }),
      nostrEvent({
        id: 'sbom-index-event',
        kind: KINDS.SBOM_INDEX,
        created_at: now - 1,
        tags: [['domain', 'artifact'], ['schema', 'bahia.sbom.index.v1'], ['type', 'sbom.index'], ['op', 'sbom.index'], ['d', `${ARTIFACT_ID}:index`], ['artifact', ARTIFACT_ID]],
        content: {
          schema: 'bahia.sbom.index.v1',
          domain: 'artifact',
          event_type: 'sbom.index',
          artifact_id: ARTIFACT_ID,
          package_count: 2
        }
      }),
      nostrEvent({
        id: 'deployment-audit-event',
        kind: KINDS.AUDIT,
        created_at: now - 2,
        tags: [['domain', 'controlplane'], ['schema', 'bahia.audit.v1'], ['type', 'deployment.started'], ['event_type', 'deployment.started'], ['service', SERVICE_ID]],
        content: {
          schema: 'bahia.audit.v1',
          type: 'deployment.started',
          event_type: 'deployment.started',
          entity_id: SERVICE_ID,
          data: { environment_id: 'prod' }
        }
      })
    ];

    await installE2EMocks(page, { systemInfo: relaySystemInfo, nostrEvents });

    await page.goto('/events');
    await expect(page.getByRole('heading', { name: 'Live Events' })).toBeVisible();
    await expect(page.getByText('2 SBOM events')).toBeVisible();
    await expect(page.getByText('Showing 3 of 3 events')).toBeVisible();

    await page.selectOption('#event-type-filter', 'sbom');

    await expect(page.getByText('Showing 2 of 3 events')).toBeVisible();
    const filteredRows = page.locator('tbody tr');
    await expect(filteredRows.filter({ hasText: 'SBOM Attestation' })).toHaveCount(2);
    await expect(filteredRows.filter({ hasText: 'SBOM' })).toHaveCount(2);
    await expect(page.getByText('deployment.started')).toBeHidden();

    await page.selectOption('#event-type-filter', 'policy');
    await expect(page.getByText('Showing 0 of 3 events')).toBeVisible();
    await expect(page.getByText('0 events')).toBeVisible();
  });
});
