import { test, expect } from '@playwright/test';
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { base64Encode, CONFIDENTIAL_ALGORITHM, CONFIDENTIAL_SCHEMA, OCK_WRAP_SCHEMA } from '../../src/lib/nostr/confidential.js';
import { confidentialCpStateFixture } from './cp-state-fixtures.js';
import { E2E_SERVICE_PUBKEY, TEST_PUBKEY, e2eTestPubkey, installE2EMocks } from './helpers.js';

const ORG_ID = 'org-role-e2e';
const OTHER_MEMBER = e2eTestPubkey('other-org-member');
const OCK = new Uint8Array(32).fill(7);

function encryptedMemberRecord(pubkey, role, nonceByte) {
  const d = `org:member:${ORG_ID}:${pubkey}`;
  const associatedData = {
    d,
    key_org: ORG_ID,
    key_ref: `ock:${ORG_ID}`,
    key_version: 'v1',
    legacy_kind: '32006',
    schema: CONFIDENTIAL_SCHEMA,
    t: 'org-member'
  };
  const nonce = new Uint8Array(24);
  nonce[0] = nonceByte;
  const ciphertext = xchacha20poly1305(OCK, nonce, new TextEncoder().encode(JSON.stringify(associatedData)))
    .encrypt(new TextEncoder().encode(JSON.stringify({ org_id: ORG_ID, pubkey, role, deleted: false })));
  return confidentialCpStateFixture({
    d,
    topic: 'org-member',
    legacyKind: 32006,
    content: JSON.stringify({
      schema: CONFIDENTIAL_SCHEMA,
      algorithm: CONFIDENTIAL_ALGORITHM,
      key_org: ORG_ID,
      key_ref: `ock:${ORG_ID}`,
      key_version: 'v1',
      nonce: base64Encode(nonce),
      ciphertext: base64Encode(ciphertext),
      associated_data: associatedData
    })
  });
}

function encryptedOrgRecord() {
  const d = `org:${ORG_ID}`;
  const associatedData = {
    d, key_org: ORG_ID, key_ref: `ock:${ORG_ID}`, key_version: 'v1',
    legacy_kind: '32005', schema: CONFIDENTIAL_SCHEMA, t: 'org'
  };
  const nonce = new Uint8Array(24);
  nonce[0] = 3;
  const ciphertext = xchacha20poly1305(OCK, nonce, new TextEncoder().encode(JSON.stringify(associatedData)))
    .encrypt(new TextEncoder().encode(JSON.stringify({ id: ORG_ID, name: 'Test org' })));
  return confidentialCpStateFixture({
    d, topic: 'org', legacyKind: 32005,
    content: JSON.stringify({ schema: CONFIDENTIAL_SCHEMA, algorithm: CONFIDENTIAL_ALGORITHM,
      key_org: ORG_ID, key_ref: `ock:${ORG_ID}`, key_version: 'v1', nonce: base64Encode(nonce),
      ciphertext: base64Encode(ciphertext), associated_data: associatedData })
  });
}

function roleFixtureEvents() {
  const wrap = {
    schema: OCK_WRAP_SCHEMA,
    org_id: ORG_ID,
    key_ref: `ock:${ORG_ID}`,
    version: 1,
    key: base64Encode(OCK),
    recipient_pubkey: TEST_PUBKEY
  };
  return [
    confidentialCpStateFixture({
      d: `org-key:${ORG_ID}:v1:e2e`, topic: 'org-key-envelope', legacyKind: 32010,
      content: `mock-nip44:${Buffer.from(JSON.stringify(wrap)).toString('base64')}`
    }),
    encryptedOrgRecord(),
    encryptedMemberRecord(TEST_PUBKEY, 'viewer', 1),
    encryptedMemberRecord(OTHER_MEMBER, 'owner', 2)
  ];
}

test.beforeEach(async ({ page }) => {
  await page.route('**/api/v1/**', (route) => {
    const url = route.request().url();

    if (url.includes('/services') || url.includes('/environments') || url.includes('/workers') || url.includes('/events')) {
      return route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: JSON.stringify({ data: [] })
      });
    }

    return route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ data: null })
    });
  });
});

test('redirects unauthenticated users away from protected routes', async ({ page }) => {
  await installE2EMocks(page, { authenticated: false, extension: false });

  await page.goto('/services');

  await expect(page).toHaveURL('/');
});

test('shows permission denied when user lacks required route role', async ({ page }) => {
  await installE2EMocks(page, {
    authenticated: true,
    extension: true,
    routeRoleRequirements: {
      '/settings': ['admin']
    }
  });

  await page.goto('/settings');

  await expect(page).toHaveURL('/settings');
  await expect(page.getByText('You do not have permission to view this page.')).toBeVisible();
});

test('allows authenticated users to access routes with no role requirement', async ({ page }) => {
  // §6.2: authenticated = persisted signer-verified session, no REST probe.
  // /orgs has no role requirement, so authenticated users can access it directly.
  await installE2EMocks(page, {
    authenticated: true,
    extension: true
  });

  await page.goto('/orgs');

  // Authenticated user should stay on /orgs, not be redirected
  await expect(page).toHaveURL('/orgs');
});

test('renders protected route immediately for authenticated user without backend probe', async ({ page }) => {
  // §6.2: No REST probe, no /api/v1/orgs call — auth is from persisted session + relay roles.
  let apiProbeCount = 0;

  await installE2EMocks(page, {
    authenticated: true,
    extension: true
  });

  await page.route('**/api/v1/orgs**', (route) => {
    apiProbeCount += 1;
    return route.fulfill({ json: { data: [] } });
  });

  await page.goto('/settings');

  // Settings requires 'owner' role but we didn't set one, so it should show permission denied
  await expect(page).toHaveURL('/settings');
  // No /api/v1/orgs probe was needed for auth
  expect(apiProbeCount).toBe(0);
});

test('shows a viewer-only org detail despite another decryptable owner record', async ({ page }) => {
  const orgDetail = { org: { id: ORG_ID, name: 'Test org' }, members: [
    { pubkey: TEST_PUBKEY, role: 'viewer' }, { pubkey: OTHER_MEMBER, role: 'owner' }
  ], invites: [], my_role: 'viewer' };
  await installE2EMocks(page, {
    backendRole: null,
    routeRoleRequirements: { '/policies': ['viewer'] },
    systemInfo: {
      nostr: { browser_relays: ['ws://relay.test.local'], contextvm_relays: ['ws://relay.test.local'],
        service_relays: ['ws://relay.test.local'], service_pubkey: E2E_SERVICE_PUBKEY },
      features: { relay_sidecar: true, relay_read_models: true, encrypted_controlplane: true,
        encrypted_nostr_requests: true, legacy_sse: false }
    },
    nostrEvents: roleFixtureEvents(),
    // The NIP-59 mock consumes responses in request order. Repeated detail
    // snapshots also satisfy any retained-subscription refresh during this page.
    contextVMOperations: Array.from({ length: 8 }, () => ({ operation: 'orgs.detail', response: orgDetail }))
  });

  await page.goto(`/orgs/${ORG_ID}`);
  await expect(page.getByRole('heading', { name: 'Members (2)' })).toBeVisible({ timeout: 15000 });
  await expect(page.getByText(OTHER_MEMBER.slice(0, 8), { exact: false })).toBeVisible();
  await expect(page.getByRole('button', { name: 'Invite Member' })).toHaveCount(0);
  await expect(page.getByRole('button', { name: 'Delete Organization' })).toHaveCount(0);

  await page.goto('/policies');
  await expect(page.getByRole('heading', { name: 'Policies', exact: true })).toBeVisible();
  await page.goto('/settings');
  await expect(page.getByText('You do not have permission to view this page.')).toBeVisible();
});
