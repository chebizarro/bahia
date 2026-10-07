import { beforeEach, describe, expect, it, vi } from 'vitest';

const bootMock = vi.hoisted(() => ({ store: null, service: 'a'.repeat(64) }));
vi.mock('$lib/nostr/boot.js', () => ({
  getEventStore: () => bootMock.store,
  getServicePubkeys: () => [bootMock.service],
  getServicePubkey: () => bootMock.service
}));

import {
  deriveOperatorAllowlist,
  initOperatorAllowlistBinding,
  onOperatorAllowlistChange,
  operatorAllowlistAvailable,
  operatorAllowlistSignature,
  operatorAllowlists,
  teardownOperatorAllowlistBinding,
  trustedOperatorAuthors
} from '../../src/lib/stores/operator-allowlist.svelte.js';
import { stopRoleDerivation } from '../../src/lib/stores/auth-roles.svelte.js';
import { fleetOCKFixtureKey, operatorAllowlistFixture } from '../e2e/fleet-ock-fixtures.js';

const SERVICE = bootMock.service;
const ME = 'b'.repeat(64);
const ALICE = 'c'.repeat(64);
const BOB = 'd'.repeat(64);
const key = fleetOCKFixtureKey();
const otherKey = fleetOCKFixtureKey({ fill: 8 });
const holder = (orgID, version) => (orgID === 'fleet' && version === 1 ? key : null);
const nonHolder = () => null;

function record(options) {
  const fixture = operatorAllowlistFixture({ key, pubkey: SERVICE, createdAt: 100, ...options });
  return { ...fixture, id: options.id || fixture.id };
}

describe('operator allowlist derivation (bahia-fbyo5)', () => {
  it('no record → signed-in key only', () => {
    expect(deriveOperatorAllowlist([], 'continuity', { serviceAuthors: [SERVICE], keyFor: holder })).toBeNull();
    expect(trustedOperatorAuthors(null, ME)).toEqual([ME]);
    expect(trustedOperatorAuthors(null, '')).toEqual([]);
  });

  it('record readable with the fleet OCK → allowlist ∪ signed-in key', () => {
    const events = [record({ scope: 'continuity', pubkeys: [BOB, ALICE, ALICE] })];
    const allowlist = deriveOperatorAllowlist(events, 'continuity', { serviceAuthors: [SERVICE], keyFor: holder });
    expect(allowlist).toEqual([ALICE, BOB]);
    expect(trustedOperatorAuthors(allowlist, ME)).toEqual([ME, ALICE, BOB].sort());
    // The signed-in key is never dropped, even when it is also listed.
    expect(trustedOperatorAuthors(allowlist, ALICE)).toEqual([ALICE, BOB]);
    // Scopes are independent coordinates.
    expect(deriveOperatorAllowlist(events, 'soul-factory', { serviceAuthors: [SERVICE], keyFor: holder })).toBeNull();
  });

  it('non-holder (no fleet OCK, or another key) → signed-in key only', () => {
    const events = [record({ scope: 'continuity', pubkeys: [ALICE] })];
    expect(deriveOperatorAllowlist(events, 'continuity', { serviceAuthors: [SERVICE], keyFor: nonHolder })).toBeNull();
    expect(deriveOperatorAllowlist(events, 'continuity', { serviceAuthors: [SERVICE], keyFor: () => otherKey })).toBeNull();
    expect(trustedOperatorAuthors(null, ME)).toEqual([ME]);
  });

  it('ignores records not signed by a service key, on another coordinate, or whose plaintext names another scope', () => {
    const forged = record({ scope: 'continuity', pubkeys: [ALICE], pubkey: 'e'.repeat(64) });
    expect(deriveOperatorAllowlist([forged], 'continuity', { serviceAuthors: [SERVICE], keyFor: holder })).toBeNull();
    // A soul-factory record re-tagged as the continuity coordinate fails the AEAD binding.
    const retagged = record({ scope: 'soul-factory', pubkeys: [ALICE] });
    retagged.tags = retagged.tags.map((tag) => (tag[0] === 'd' ? ['d', 'operators:continuity'] : tag));
    expect(deriveOperatorAllowlist([retagged], 'continuity', { serviceAuthors: [SERVICE], keyFor: holder })).toBeNull();
    // Pubkeys that are not 64-hex are dropped from a readable list.
    const sloppy = record({ scope: 'continuity', pubkeys: [ALICE.toUpperCase()] });
    expect(deriveOperatorAllowlist([sloppy], 'continuity', { serviceAuthors: [SERVICE], keyFor: holder })).toEqual([ALICE]);
  });

  it('the newest record wins and a tombstone removes the allowlist', () => {
    const older = record({ scope: 'continuity', pubkeys: [ALICE], createdAt: 100, id: 'older' });
    const newer = record({ scope: 'continuity', pubkeys: [BOB], createdAt: 200, id: 'newer', nonceByte: 3 });
    expect(deriveOperatorAllowlist([older, newer], 'continuity', { serviceAuthors: [SERVICE], keyFor: holder })).toEqual([BOB]);
    expect(deriveOperatorAllowlist([newer, older], 'continuity', { serviceAuthors: [SERVICE], keyFor: holder })).toEqual([BOB]);
    const tombstone = record({ scope: 'continuity', pubkeys: [], createdAt: 300, id: 'tomb' });
    expect(tombstone.tags).toContainEqual(['deleted', 'true']);
    expect(deriveOperatorAllowlist([older, newer, tombstone], 'continuity', { serviceAuthors: [SERVICE], keyFor: holder })).toBeNull();
  });
});

function storeDouble(events) {
  const listeners = new Set();
  return {
    events,
    query: vi.fn((filter) => events.filter((event) => filter.kinds.includes(event.kind)
      && (!filter['#t'] || event.tags.some((tag) => tag[0] === 't' && filter['#t'].includes(tag[1])))
      && (!filter.authors || filter.authors.includes(event.pubkey)))),
    subscribe: vi.fn((_filter, cb) => { listeners.add(cb); return () => listeners.delete(cb); }),
    emit() { for (const cb of [...listeners]) cb(); }
  };
}

describe('operator allowlist binding', () => {
  beforeEach(() => { teardownOperatorAllowlistBinding(); stopRoleDerivation(); });

  it('re-projects when the record arrives and when the fleet OCK is obtained or lost', async () => {
    const store = storeDouble([]);
    bootMock.store = store;
    const changes = vi.fn();
    const off = onOperatorAllowlistChange(changes);
    initOperatorAllowlistBinding();
    expect(operatorAllowlistAvailable('continuity')).toBe(false);
    expect(operatorAllowlistSignature()).toBe('continuity=-|soul-factory=-');

    // The record arrives but this session holds no fleet OCK: still signed-in only.
    store.events.push(record({ scope: 'continuity', pubkeys: [ALICE] }));
    store.emit();
    expect(operatorAllowlists.continuity).toBeNull();
    expect(changes).not.toHaveBeenCalled();

    // Role derivation finds the fleet key wrap: the allowlist becomes readable.
    const { startRoleDerivation } = await import('../../src/lib/stores/auth-roles.svelte.js');
    const { base64Encode } = await import('../../src/lib/nostr/confidential.js');
    const wrap = JSON.stringify({ schema: 'bahia.ock-wrap.v1', org_id: 'fleet', key_ref: 'ock:fleet', version: 1, key: base64Encode(key.key), recipient_pubkey: ME });
    store.events.push({ id: 'wrap', kind: 30900, pubkey: SERVICE, created_at: 50,
      tags: [['d', 'org-key:fleet:v1:' + ME.slice(0, 8)], ['t', 'org-key-envelope'], ['legacy_kind', '32010']], content: 'wrapped' });
    await startRoleDerivation({ store, userPubkey: ME, servicePubkey: SERVICE, signer: { decryptNip44: async () => wrap } });
    expect(operatorAllowlists.continuity).toEqual([ALICE]);
    expect(operatorAllowlistAvailable('continuity')).toBe(true);
    expect(operatorAllowlistSignature()).toBe(`continuity=${ALICE}|soul-factory=-`);
    expect(changes).toHaveBeenCalledTimes(1);

    // An unchanged re-emit does not notify; a replaced record does.
    store.emit();
    expect(changes).toHaveBeenCalledTimes(1);
    store.events.push(record({ scope: 'continuity', pubkeys: [ALICE, BOB], createdAt: 200, id: 'v2', nonceByte: 4 }));
    store.emit();
    expect(operatorAllowlists.continuity).toEqual([ALICE, BOB]);
    expect(changes).toHaveBeenCalledTimes(2);

    // Logout clears the content keys: back to signed-in only.
    stopRoleDerivation();
    expect(operatorAllowlists.continuity).toBeNull();
    expect(changes).toHaveBeenCalledTimes(3);
    off();
    teardownOperatorAllowlistBinding();
    // The binding subscribes to the allowlist topic only; the role derivation
    // above owns the key-envelope and member subscriptions.
    expect(store.subscribe.mock.calls.filter(([filter]) => filter['#t']?.includes('operator-allowlist'))).toHaveLength(1);
  });
});
