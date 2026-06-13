import { test, expect } from '@playwright/test';
import { installE2EMocks } from './helpers.js';

const SERVICE_PUBKEY = '79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798';
const ARTIFACT_ID = 'artifact-sbom-1';
const NO_SBOM_ARTIFACT_ID = 'artifact-no-sbom';
const SERVICE_ID = 'svc-sbom';

const KINDS = {
  SERVICE_REGISTRY: 30900,
  ARTIFACT_REGISTRY: 30900,
  SBOM_STATUS: 30315,
  SBOM_REFERENCE: 30078,
  SBOM_AVAILABILITY_LIST: 30004,
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

function artifactPayload({ id = ARTIFACT_ID, name = null, packages = [], sbom = null, attestation = null } = {}) {
  return {
    schema: 'bahia.registry.artifact.v1',
    id,
    service_id: SERVICE_ID,
    name: name || (id === ARTIFACT_ID ? 'registry.example.com/bahia/sbom-demo' : 'registry.example.com/bahia/no-sbom'),
    image_repo: id === ARTIFACT_ID ? 'registry.example.com/bahia/sbom-demo' : 'registry.example.com/bahia/no-sbom',
    artifact_type: 'container_image',
    image_tag: '1.2.3',
    digest: 'sha256:111122223333444455556666777788889999aaaabbbbccccddddeeeeffff0000',
    size_bytes: 52428800,
    sbom_packages: packages,
    ...(sbom ? { sbom } : {}),
    ...(attestation ? { sbom_attestation: attestation } : {}),
    created_at: '2026-05-13T12:00:00.000Z',
    deleted: false
  };
}

function artifactEvent(options = {}) {
  const artifact = artifactPayload(options);
  return nostrEvent({
    id: `${artifact.id}-event`,
    kind: KINDS.ARTIFACT_REGISTRY,
    tags: [['domain', 'controlplane'], ['schema', 'bahia.registry.artifact.v1'], ['legacy_kind', '31966'], ['d', artifact.id], ['artifact', artifact.id], ['service', SERVICE_ID], ['deleted', 'false']],
    content: artifact
  });
}

function escapeRegExp(value) {
  return String(value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

async function failOnUnsupportedSBOMEndpoints(page, artifactId) {
  const escapedArtifactId = escapeRegExp(artifactId);
  await page.route(new RegExp(`/api/v1/artifacts/${escapedArtifactId}/sbom(?:/attestation)?$`), (route) => {
    throw new Error(`Artifact SBOM tab should not call unsupported endpoint: ${route.request().url()}`);
  });
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
  test('artifact registry list maps name, version, and digest columns from projection fields', async ({ page }) => {
    await installE2EMocks(page, {
      authenticated: true,
      extension: true,
      systemInfo: relaySystemInfo,
      nostrEvents: [serviceEvent(), artifactEvent({ name: '1.2.3' })]
    });

    await page.goto('/artifacts');

    await expect(page.getByRole('heading', { name: 'Artifacts' })).toBeVisible();
    const row = page.locator('tbody tr', { hasText: 'registry.example.com/bahia/sbom-demo' }).first();
    await expect(row.locator('td').nth(0)).toContainText('registry.example.com/bahia/sbom-demo');
    await expect(row.locator('td').nth(1)).toContainText('sbom-service');
    await expect(row.locator('td').nth(2)).toHaveText('1.2.3');
    await expect(row.locator('td').nth(3)).toContainText('sha256:111122223333…ff0000');
  });

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
      authenticated: true,
      extension: true,
      systemInfo: relaySystemInfo,
      nostrEvents: [serviceEvent(), artifactEvent({ packages, sbom, attestation })]
    });
    await failOnUnsupportedSBOMEndpoints(page, ARTIFACT_ID);

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

  test('artifact SBOM tab publishes signer-backed ContextVM generation request', async ({ page }) => {
    await installE2EMocks(page, {
      authenticated: true,
      extension: true,
      systemInfo: relaySystemInfo,
      nostrEvents: [serviceEvent(), artifactEvent({ id: NO_SBOM_ARTIFACT_ID })]
    });
    await failOnUnsupportedSBOMEndpoints(page, NO_SBOM_ARTIFACT_ID);

    await page.goto(`/artifacts/${NO_SBOM_ARTIFACT_ID}`);
    await expect(page.getByRole('heading', { name: 'registry.example.com/bahia/no-sbom' })).toBeVisible();
    await page.evaluate(({ artifactId, digest }) => {
      window.__BAHIA_E2E_NEXT_CONTEXTVM_OPERATION = {
        operation: 'sbom/generate',
        payload: {
          subject: { type: 'artifact', id: artifactId, digest },
          formats: ['spdx', 'cyclonedx'],
          generator: 'syft'
        }
      };
    }, { artifactId: NO_SBOM_ARTIFACT_ID, digest: artifactPayload({ id: NO_SBOM_ARTIFACT_ID }).digest });
    const generated = page.evaluate(() => new Promise((resolve) => {
      window.addEventListener('__bahia_e2e_sbom_generated', (event) => resolve(event.detail), { once: true });
    }));
    await page.getByRole('button', { name: /^SBOM/ }).click();
    await page.getByRole('button', { name: 'Generate SBOM' }).click();
    const generatedDetail = await generated;
    expect(generatedDetail).toMatchObject({ artifactId: NO_SBOM_ARTIFACT_ID, formats: ['spdx', 'cyclonedx'], generator: 'syft' });

    const generatedEvents = await page.evaluate((artifactId) => {
      const events = JSON.parse(localStorage.getItem('__bahia_e2e_nostr_events') || '[]');
      const hasTag = (event, name, value) => Array.isArray(event.tags) && event.tags.some((tag) => Array.isArray(tag) && tag[0] === name && (value === undefined || tag[1] === value));
      return {
        request: events.find((event) => event.kind === 1059 && hasTag(event, 'p')) || null,
        status: events.find((event) => event.kind === 30315 && hasTag(event, 'artifact', artifactId)) || null,
        references: events.filter((event) => event.kind === 30078 && hasTag(event, 'artifact', artifactId)).map((event) => ({ tags: event.tags, content: JSON.parse(event.content || '{}') })),
        availability: events.find((event) => event.kind === 30004 && hasTag(event, 'artifact', artifactId)) || null,
        audit: events.find((event) => event.kind === 4903 && hasTag(event, 'event_type', 'sbom.generated')) || null
      };
    }, NO_SBOM_ARTIFACT_ID);

    expect(generatedEvents.request).toBeTruthy();
    expect(generatedEvents.request.id).toBe(generatedDetail.requestEventId);
    expect(generatedEvents.status).toBeTruthy();
    expect(generatedEvents.references).toHaveLength(2);
    expect(generatedEvents.references.map((event) => event.content.format).sort()).toEqual(['cyclonedx', 'spdx']);
    expect(generatedEvents.references.every((event) => event.content.storage?.type === 'blossom')).toBe(true);
    expect(generatedEvents.availability).toBeTruthy();
    expect(generatedEvents.audit).toBeTruthy();
  });

  test('artifact SBOM tab shows an empty state when no attestation exists', async ({ page }) => {
    await installE2EMocks(page, {
      authenticated: true,
      extension: true,
      systemInfo: relaySystemInfo,
      nostrEvents: [serviceEvent(), artifactEvent({ id: NO_SBOM_ARTIFACT_ID })]
    });
    await failOnUnsupportedSBOMEndpoints(page, NO_SBOM_ARTIFACT_ID);

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
        kind: KINDS.SBOM_REFERENCE,
        created_at: now,
        tags: [['domain', 'sbom'], ['schema', 'bahia.sbom.ref.v1'], ['type', 'sbom.ref'], ['op', 'sbom.ref'], ['d', `sbom:ref:${ARTIFACT_ID}:spdx:abc123`], ['artifact', ARTIFACT_ID]],
        content: {
          schema: 'bahia.sbom.ref.v1',
          domain: 'sbom',
          event_type: 'sbom.ref',
          artifact_id: ARTIFACT_ID,
          format: 'spdx',
          generator: 'syft'
        }
      }),
      nostrEvent({
        id: 'sbom-index-event',
        kind: KINDS.SBOM_AVAILABILITY_LIST,
        created_at: now - 1,
        tags: [['domain', 'sbom'], ['schema', 'bahia.sbom.available-list.v1'], ['type', 'sbom.available-list'], ['op', 'sbom.available-list'], ['d', `sbom:available:artifact:${ARTIFACT_ID}`], ['artifact', ARTIFACT_ID]],
        content: {
          schema: 'bahia.sbom.available-list.v1',
          domain: 'sbom',
          event_type: 'sbom.available-list',
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
    await expect(filteredRows.filter({ hasText: 'SBOM Reference' })).toHaveCount(1);
    await expect(filteredRows.filter({ hasText: 'SBOM Availability List' })).toHaveCount(1);
    await expect(filteredRows.filter({ hasText: 'SBOM' })).toHaveCount(2);
    await expect(page.getByText('deployment.started')).toBeHidden();

    await page.selectOption('#event-type-filter', 'policy');
    await expect(page.getByText('Showing 0 of 3 events')).toBeVisible();
    await expect(page.getByText('0 events')).toBeVisible();
  });
});
