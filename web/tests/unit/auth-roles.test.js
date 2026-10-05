/**
 * Tests for auth-roles: role derivation from fixture envelopes + records.
 *
 * - Session restore without network
 * - Role derivation from fixture key envelopes + encrypted member records
 * - No-NIP-44 degradation
 * - AuthGuard role gating
 * - OCK is never written to IndexedDB/localStorage
 */

import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

// Mock $app/environment before importing modules that use it
vi.mock('$app/environment', () => ({
  browser: true,
  dev: false,
  building: false,
  version: 'test'
}));

// Mock $app/navigation
vi.mock('$app/navigation', () => ({
  goto: vi.fn()
}));

// Mock nostr modules
vi.mock('$lib/nostr/nip07.js', () => ({
  waitForNip07: vi.fn().mockResolvedValue({ available: true }),
  getPublicKey: vi.fn(),
  getRelays: vi.fn().mockResolvedValue({}),
  getCapabilities: vi.fn().mockReturnValue({ getPublicKey: true, signEvent: true, nip44: true }),
  getNip07Signer: vi.fn().mockReturnValue({
    getPublicKey: vi.fn(),
    signEvent: vi.fn(),
    encryptNip44: vi.fn(),
    decryptNip44: vi.fn()
  }),
  detectNip07: vi.fn().mockReturnValue({ available: true }),
  watchNip07Availability: vi.fn().mockReturnValue(() => {})
}));

vi.mock('$lib/nostr/nip46.js', () => ({
  detectNip46: vi.fn().mockReturnValue({ available: false }),
  parseNostrConnectUri: vi.fn(),
  connectNip46: vi.fn(),
  disconnectNip46: vi.fn().mockResolvedValue(undefined),
  getNip46Signer: vi.fn(),
  getCapabilities: vi.fn().mockReturnValue({})
}));

vi.mock('$lib/nostr/pool-utils.js', () => ({
  normalizeRelayUrl: vi.fn(url => url),
  uniqueRelays: vi.fn(arr => [...new Set(arr)])
}));

vi.mock('$lib/nostr/store-interface.js', () => ({
  requestPersistentStorage: vi.fn().mockResolvedValue(true)
}));

vi.mock('$lib/components/toast.js', () => ({
  toast: { success: vi.fn(), warning: vi.fn(), error: vi.fn() },
  removeToast: vi.fn()
}));

vi.mock('$lib/nostr/encrypted-controlplane.js', () => ({
  disconnectEncryptedControlplane: vi.fn()
}));

import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { base64Encode } from '../../src/lib/nostr/confidential.js';
import {
  CONFIDENTIAL_SCHEMA,
  CONFIDENTIAL_ALGORITHM,
  decryptConfidentialContent,
  OCK_WRAP_SCHEMA
} from '../../src/lib/nostr/confidential.js';

// ---------------------------------------------------------------------------
// Test fixtures
// ---------------------------------------------------------------------------

const TEST_USER_PUBKEY = 'a'.repeat(64);
const TEST_SERVICE_PUBKEY = 'b'.repeat(64);
const TEST_ORG_ID = 'org-fixture-1';

// Deterministic OCK for fixture
const TEST_OCK = new Uint8Array(32);
TEST_OCK[0] = 0xDE;
TEST_OCK[1] = 0xAD;
TEST_OCK[31] = 0xBE;

function buildFixtureKeyEnvelope(orgID = TEST_ORG_ID, version = 1) {
  const wrapPayload = JSON.stringify({
    schema: OCK_WRAP_SCHEMA,
    org_id: orgID,
    key_ref: 'ock:' + orgID,
    version,
    key: base64Encode(TEST_OCK),
    recipient_pubkey: TEST_USER_PUBKEY
  });

  return {
    id: `envelope-${orgID}-v${version}`,
    pubkey: TEST_SERVICE_PUBKEY,
    created_at: 1700000000,
    kind: 30900,
    tags: [
      ['d', 'org-key:' + orgID + ':v' + version + ':handle123'],
      ['t', 'org-key-envelope']
    ],
    content: wrapPayload, // In real life this would be NIP-44 encrypted
    sig: 'a'.repeat(128)
  };
}

function buildFixtureMemberRecord({ orgID = TEST_ORG_ID, pubkey = TEST_USER_PUBKEY, role = 'admin', deleted = false, version = 1, createdAt = 1700000001, id = `member-${orgID}-${pubkey}-${createdAt}`, dTagPubkey = pubkey } = {}) {
  const memberPlaintext = JSON.stringify({
    org_id: orgID,
    pubkey,
    ...(deleted ? {} : { role }),
    deleted
  });

  const ad = {
    schema: CONFIDENTIAL_SCHEMA,
    key_org: orgID,
    key_ref: 'ock:' + orgID,
    key_version: 'v' + version,
    legacy_kind: '32006',
    d: 'org:member:' + orgID + ':' + dTagPubkey,
    t: 'org-member'
  };

  const sortedKeys = Object.keys(ad).sort();
  const sortedAD = {};
  for (const k of sortedKeys) sortedAD[k] = ad[k];
  const adBytes = new TextEncoder().encode(JSON.stringify(sortedAD));

  const nonce = new Uint8Array(24);
  nonce[0] = 0x02;
  nonce[1] = createdAt & 0xff;

  const cipher = xchacha20poly1305(TEST_OCK, nonce, adBytes);
  const ciphertext = cipher.encrypt(new TextEncoder().encode(memberPlaintext));

  return {
    id,
    pubkey: TEST_SERVICE_PUBKEY,
    created_at: createdAt,
    kind: 30900,
    tags: [
      ['d', 'org:member:' + orgID + ':' + dTagPubkey],
      ['t', 'org-member'],
      ['legacy_kind', '32006'],
      ['deleted', String(deleted)]
    ],
    content: JSON.stringify({
      schema: CONFIDENTIAL_SCHEMA,
      algorithm: CONFIDENTIAL_ALGORITHM,
      key_org: orgID,
      key_ref: 'ock:' + orgID,
      key_version: 'v' + version,
      nonce: base64Encode(nonce),
      ciphertext: base64Encode(ciphertext),
      associated_data: ad
    }),
    sig: 'b'.repeat(128)
  };
}

// ---------------------------------------------------------------------------
// Mock event store
// ---------------------------------------------------------------------------

function createMockStore(events = []) {
  const subscribers = [];
  return {
    query(filter) {
      return events.filter(e => {
        if (filter.kinds && !filter.kinds.includes(e.kind)) return false;
        if (filter['#t']) {
          const tTags = e.tags.filter(t => t[0] === 't').map(t => t[1]);
          if (!filter['#t'].some(t => tTags.includes(t))) return false;
        }
        return true;
      });
    },
    subscribe(filter, cb) {
      const sub = { filter, cb };
      subscribers.push(sub);
      return () => {
        const idx = subscribers.indexOf(sub);
        if (idx >= 0) subscribers.splice(idx, 1);
      };
    },
    _subscribers: subscribers,
    emit(event) {
      events.push(event);
      for (const { cb } of subscribers) cb(event);
    },
    replace(event) {
      const d = event.tags.find(tag => tag[0] === 'd')?.[1];
      for (let i = events.length - 1; i >= 0; i--) {
        if (events[i].pubkey === event.pubkey && events[i].tags.find(tag => tag[0] === 'd')?.[1] === d) events.splice(i, 1);
      }
      this.emit(event);
    }
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('auth-roles', () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it('derives roles from fixture key envelopes and encrypted member records', async () => {
    const { startRoleDerivation, orgRoles, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');

    const envelopeEvent = buildFixtureKeyEnvelope();
    const memberEvent = buildFixtureMemberRecord();
    const store = createMockStore([envelopeEvent, memberEvent]);

    // Mock signer that "decrypts" the NIP-44 (in fixtures, content is plaintext)
    const signer = {
      decryptNip44: vi.fn().mockResolvedValue(envelopeEvent.content)
    };

    await startRoleDerivation({
      store,
      userPubkey: TEST_USER_PUBKEY,
      servicePubkey: TEST_SERVICE_PUBKEY,
      signer
    });

    expect(orgRoles[TEST_ORG_ID]).toBe('admin');

    stopRoleDerivation();
  });

  it('does not grant member A\'s role to signed-in member B', async () => {
    const { startRoleDerivation, orgRoles, hasAnyRole, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');
    const envelope = buildFixtureKeyEnvelope();
    await startRoleDerivation({
      store: createMockStore([envelope, buildFixtureMemberRecord({ pubkey: 'c'.repeat(64), role: 'owner' })]),
      userPubkey: TEST_USER_PUBKEY,
      servicePubkey: TEST_SERVICE_PUBKEY,
      signer: { decryptNip44: vi.fn().mockResolvedValue(envelope.content) }
    });

    expect(orgRoles[TEST_ORG_ID]).toBeUndefined();
    expect(hasAnyRole(['owner'])).toBe(false);
    stopRoleDerivation();
  });

  it('requires both the coordinate and decrypted member pubkey to match the session', async () => {
    const { startRoleDerivation, orgRoles, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');
    const envelope = buildFixtureKeyEnvelope();
    const mismatchedContent = buildFixtureMemberRecord({ pubkey: 'c'.repeat(64), dTagPubkey: TEST_USER_PUBKEY, role: 'owner' });
    const mismatchedCoordinate = buildFixtureMemberRecord({ pubkey: TEST_USER_PUBKEY, dTagPubkey: 'd'.repeat(64), role: 'admin' });
    await startRoleDerivation({
      store: createMockStore([envelope, mismatchedContent, mismatchedCoordinate]),
      userPubkey: TEST_USER_PUBKEY,
      servicePubkey: TEST_SERVICE_PUBKEY,
      signer: { decryptNip44: vi.fn().mockResolvedValue(envelope.content) }
    });

    expect(orgRoles[TEST_ORG_ID]).toBeUndefined();
    stopRoleDerivation();
  });

  it('revokes the current role on a newer encrypted member tombstone', async () => {
    const { startRoleDerivation, orgRoles, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');
    const envelope = buildFixtureKeyEnvelope();
    const store = createMockStore([envelope, buildFixtureMemberRecord({ role: 'owner' })]);
    await startRoleDerivation({ store, userPubkey: TEST_USER_PUBKEY, servicePubkey: TEST_SERVICE_PUBKEY,
      signer: { decryptNip44: vi.fn().mockResolvedValue(envelope.content) } });
    expect(orgRoles[TEST_ORG_ID]).toBe('owner');

    store.emit(buildFixtureMemberRecord({ deleted: true, createdAt: 1700000002 }));
    expect(orgRoles[TEST_ORG_ID]).toBeUndefined();
    stopRoleDerivation();
  });

  it('uses created_at, not query order, for role downgrades', async () => {
    const { startRoleDerivation, orgRoles, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');
    const envelope = buildFixtureKeyEnvelope();
    const store = createMockStore([envelope,
      buildFixtureMemberRecord({ role: 'viewer', createdAt: 1700000002 }),
      buildFixtureMemberRecord({ role: 'owner', createdAt: 1700000001 })]);
    await startRoleDerivation({ store, userPubkey: TEST_USER_PUBKEY, servicePubkey: TEST_SERVICE_PUBKEY,
      signer: { decryptNip44: vi.fn().mockResolvedValue(envelope.content) } });
    expect(orgRoles[TEST_ORG_ID]).toBe('viewer');

    store.emit(buildFixtureMemberRecord({ role: 'admin', createdAt: 1700000000 }));
    expect(orgRoles[TEST_ORG_ID]).toBe('viewer');
    stopRoleDerivation();
  });

  it('isolates roles and removals across orgs with mixed member records', async () => {
    const { startRoleDerivation, orgRoles, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');
    const otherOrg = 'org-fixture-2';
    const firstEnvelope = buildFixtureKeyEnvelope();
    const secondEnvelope = buildFixtureKeyEnvelope(otherOrg);
    const store = createMockStore([firstEnvelope, secondEnvelope,
      buildFixtureMemberRecord({ role: 'viewer' }),
      buildFixtureMemberRecord({ orgID: otherOrg, pubkey: 'c'.repeat(64), role: 'owner' }),
      buildFixtureMemberRecord({ orgID: otherOrg, role: 'deployer' })]);
    await startRoleDerivation({ store, userPubkey: TEST_USER_PUBKEY, servicePubkey: TEST_SERVICE_PUBKEY,
      signer: { decryptNip44: vi.fn(async (_sender, ciphertext) => ciphertext) } });
    expect(orgRoles).toEqual({ [TEST_ORG_ID]: 'viewer', [otherOrg]: 'deployer' });

    store.emit(buildFixtureMemberRecord({ orgID: otherOrg, deleted: true, createdAt: 1700000002 }));
    expect(orgRoles).toEqual({ [TEST_ORG_ID]: 'viewer' });
    stopRoleDerivation();
  });

  it('degrades gracefully when signer lacks NIP-44', async () => {
    const { startRoleDerivation, orgRoles, nip44Available, roleDerivationError, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');

    const store = createMockStore([]);
    const signerWithoutNip44 = {
      // No decryptNip44 method
    };

    await startRoleDerivation({
      store,
      userPubkey: TEST_USER_PUBKEY,
      servicePubkey: TEST_SERVICE_PUBKEY,
      signer: signerWithoutNip44
    });

    expect(nip44Available.value).toBe(false);
    expect(Object.keys(orgRoles)).toHaveLength(0);
    expect(roleDerivationError.value).toContain('NIP-44');

    stopRoleDerivation();
  });

  it('clears roles on stopRoleDerivation', async () => {
    const { startRoleDerivation, orgRoles, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');

    const envelopeEvent = buildFixtureKeyEnvelope();
    const memberEvent = buildFixtureMemberRecord();
    const store = createMockStore([envelopeEvent, memberEvent]);

    const signer = {
      decryptNip44: vi.fn().mockResolvedValue(envelopeEvent.content)
    };

    await startRoleDerivation({
      store,
      userPubkey: TEST_USER_PUBKEY,
      servicePubkey: TEST_SERVICE_PUBKEY,
      signer
    });

    expect(orgRoles[TEST_ORG_ID]).toBe('admin');

    stopRoleDerivation();

    expect(Object.keys(orgRoles)).toHaveLength(0);
  });

  it('skips envelopes for other recipients', async () => {
    const { startRoleDerivation, orgRoles, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');

    const envelopeEvent = buildFixtureKeyEnvelope();
    const memberEvent = buildFixtureMemberRecord();
    const store = createMockStore([envelopeEvent, memberEvent]);

    // Signer decrypts the envelope but it's for a different recipient
    const otherRecipientPayload = JSON.stringify({
      schema: OCK_WRAP_SCHEMA,
      org_id: TEST_ORG_ID,
      key_ref: 'ock:' + TEST_ORG_ID,
      version: 1,
      key: base64Encode(TEST_OCK),
      recipient_pubkey: 'c'.repeat(64) // different pubkey
    });

    const signer = {
      decryptNip44: vi.fn().mockResolvedValue(otherRecipientPayload)
    };

    await startRoleDerivation({
      store,
      userPubkey: TEST_USER_PUBKEY,
      servicePubkey: TEST_SERVICE_PUBKEY,
      signer
    });

    // Should NOT have any roles — envelope was for someone else
    expect(orgRoles[TEST_ORG_ID]).toBeUndefined();

    stopRoleDerivation();
  });

  it('subscribes to live key envelope and member record updates', async () => {
    const { startRoleDerivation, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');

    const store = createMockStore([]);
    const signer = { decryptNip44: vi.fn().mockRejectedValue(new Error('no key')) };

    await startRoleDerivation({
      store,
      userPubkey: TEST_USER_PUBKEY,
      servicePubkey: TEST_SERVICE_PUBKEY,
      signer
    });

    expect(store._subscribers.length).toBe(3);

    stopRoleDerivation();

    // Subscriptions should be cleaned up
    expect(store._subscribers.length).toBe(0);
  });

  it('stays active while a live key envelope is still being decrypted, then exposes the role', async () => {
    const { startRoleDerivation, stopRoleDerivation, orgRoles, roleDerivationActive } =
      await import('../../src/lib/stores/auth-roles.svelte.js');

    const store = createMockStore([]);
    let finishDecrypt;
    const signer = { decryptNip44: vi.fn(() => new Promise(resolve => { finishDecrypt = resolve; })) };

    await startRoleDerivation({ store, userPubkey: TEST_USER_PUBKEY, servicePubkey: TEST_SERVICE_PUBKEY, signer });
    // Nothing in the store yet: the initial pass is complete and found no org.
    expect(roleDerivationActive.value).toBe(false);
    expect(Object.keys(orgRoles)).toEqual([]);

    // Relay catch-up delivers the membership; the org is not known until the
    // signer finishes the trial decryption, and the store must say so.
    const envelopeEvent = buildFixtureKeyEnvelope();
    store.emit(buildFixtureMemberRecord());
    store.emit(envelopeEvent);
    expect(roleDerivationActive.value).toBe(true);
    expect(Object.keys(orgRoles)).toEqual([]);

    finishDecrypt(envelopeEvent.content);
    await vi.waitFor(() => expect(roleDerivationActive.value).toBe(false));
    expect(orgRoles[TEST_ORG_ID]).toBe('admin');

    // A superseded pass must not leave the next derivation marked active.
    store.emit(buildFixtureKeyEnvelope('org-fixture-2'));
    expect(roleDerivationActive.value).toBe(true);
    stopRoleDerivation();
    expect(roleDerivationActive.value).toBe(false);
    finishDecrypt('late');
    await Promise.resolve();
    expect(roleDerivationActive.value).toBe(false);
  });
});

describe('OCK never persisted to storage', () => {
  it('localStorage never contains OCK data after role derivation', async () => {
    vi.resetModules();
    const { startRoleDerivation, stopRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');

    const envelopeEvent = buildFixtureKeyEnvelope();
    const memberEvent = buildFixtureMemberRecord();
    const store = createMockStore([envelopeEvent, memberEvent]);
    const signer = { decryptNip44: vi.fn().mockResolvedValue(envelopeEvent.content) };

    await startRoleDerivation({
      store,
      userPubkey: TEST_USER_PUBKEY,
      servicePubkey: TEST_SERVICE_PUBKEY,
      signer
    });

    // Check localStorage doesn't contain any OCK-related data
    const allKeys = Object.keys(localStorage);
    const ockKeys = allKeys.filter(k =>
      k.toLowerCase().includes('ock') ||
      k.toLowerCase().includes('content_key') ||
      k.toLowerCase().includes('org_key') ||
      k.toLowerCase().includes('symmetric')
    );
    expect(ockKeys).toEqual([]);

    // Check that localStorage values don't contain OCK bytes
    for (const key of allKeys) {
      const value = localStorage.getItem(key);
      if (value) {
        expect(value).not.toContain(base64Encode(TEST_OCK));
      }
    }

    stopRoleDerivation();
  });
});

describe('OCK version hygiene', () => {
  it('holds a referenced old version, decrypts a late old record, then retires it after refounding', async () => {
    vi.resetModules();
    const { startRoleDerivation, stopRoleDerivation, contentKeyFor, contentKeyStateFor, onContentKeyChange,
      currentKeyVersionForOrg } = await import('../../src/lib/stores/auth-roles.svelte.js');
    const oldEnvelope = buildFixtureKeyEnvelope();
    const oldMember = buildFixtureMemberRecord({ role: 'owner' });
    const store = createMockStore([oldEnvelope, oldMember]);
    const signer = { decryptNip44: vi.fn(async (_pubkey, ciphertext) => ciphertext) };
    await startRoleDerivation({ store, userPubkey: TEST_USER_PUBKEY,
      servicePubkey: TEST_SERVICE_PUBKEY, signer });

    const nextKey = new Promise(resolve => {
      const unsubscribe = onContentKeyChange(() => {
        if (currentKeyVersionForOrg(TEST_ORG_ID) === 2) { unsubscribe(); resolve(); }
      });
    });
    store.emit(buildFixtureKeyEnvelope(TEST_ORG_ID, 2));
    await nextKey;
    await Promise.resolve();
    await Promise.resolve();
    expect(currentKeyVersionForOrg(TEST_ORG_ID)).toBe(2);
    expect(contentKeyFor(TEST_ORG_ID, 1)).toBeTruthy();
    expect(contentKeyFor(TEST_ORG_ID, 2)).toBeTruthy();

    const late = buildFixtureMemberRecord({ pubkey: 'c'.repeat(64), role: 'viewer', createdAt: 1700000002 });
    store.emit(late);
    const held = contentKeyStateFor(TEST_ORG_ID, 1);
    expect(held.status).toBe('ready');
    expect(JSON.parse(decryptConfidentialContent(held.key, late.content, {
      legacyKind: 32006, dTag: late.tags[0][1], topic: 'org-member'
    })).role).toBe('viewer');

    store.replace(buildFixtureMemberRecord({ role: 'owner', version: 2, createdAt: 1700000003 }));
    expect(contentKeyFor(TEST_ORG_ID, 1)).toBeTruthy();
    store.replace(buildFixtureMemberRecord({ pubkey: 'c'.repeat(64), role: 'viewer',
      version: 2, createdAt: 1700000003 }));
    expect(contentKeyFor(TEST_ORG_ID, 1)).toBeNull();
    expect(contentKeyStateFor(TEST_ORG_ID, 1)).toMatchObject({ key: null, status: 're-encryption pending' });

    store.emit(buildFixtureMemberRecord({ pubkey: 'd'.repeat(64), version: 1, createdAt: 1700000004 }));
    expect(contentKeyStateFor(TEST_ORG_ID, 1).status).toBe('re-encryption pending');
    store.emit(oldEnvelope);
    await Promise.resolve();
    expect(contentKeyFor(TEST_ORG_ID, 1)).toBeNull();
    stopRoleDerivation();
  });
});
