import { describe, expect, it } from 'vitest';
import { finalizeEvent, getEventHash, getPublicKey, nip44, verifyEvent } from 'nostr-tools';
import { giftWrapIntent, sensitiveIntentBlocker } from '../../src/lib/nostr/intent-giftwrap.js';
import fixture from '../fixtures/intent-giftwrap-web.json';
import 'fake-indexeddb/auto';
import { createPendingIntents } from '../../src/lib/stores/pending-intents.svelte.js';

const serviceKey = Uint8Array.from(Buffer.from(fixture.service_secret_key_hex, 'hex'));

describe('NIP-59 sensitive intent gift wrap', () => {
  it('matches the committed Go-consumed web fixture', () => {
    expect(getPublicKey(serviceKey)).toBe(fixture.service_pubkey);
    expect(verifyEvent(fixture.outer)).toBe(true);
    expect(fixture.outer.kind).toBe(1059);
    expect(fixture.outer.tags).toEqual([['p', fixture.service_pubkey]]);
    expect(fixture.outer.pubkey).not.toBe(fixture.operator_pubkey);
    const seal = JSON.parse(nip44.v2.decrypt(fixture.outer.content,
      nip44.v2.utils.getConversationKey(serviceKey, fixture.outer.pubkey)));
    expect(seal.kind).toBe(13);
    expect(seal.pubkey).toBe(fixture.operator_pubkey);
    expect(verifyEvent(seal)).toBe(true);
    const rumor = JSON.parse(nip44.v2.decrypt(seal.content,
      nip44.v2.utils.getConversationKey(serviceKey, seal.pubkey)));
    expect(rumor.kind).toBe(30900);
    expect(rumor.sig).toBe('');
    expect(rumor.id).toBe(getEventHash(rumor));
    expect(rumor.id).toBe(fixture.inner.id);
  });

  it('decrypts the deterministic Go-produced wrap from the same committed fixture', () => {
    const outer = fixture.go_outer;
    expect(outer.kind).toBe(1059);
    expect(verifyEvent(outer)).toBe(true);
    expect(outer.pubkey).toBe(getPublicKey(Uint8Array.from(Buffer.from('04'.padStart(64, '0'), 'hex'))));
    const seal = JSON.parse(nip44.v2.decrypt(outer.content,
      nip44.v2.utils.getConversationKey(serviceKey, outer.pubkey)));
    expect(verifyEvent(seal)).toBe(true);
    expect(seal.pubkey).toBe(fixture.operator_pubkey);
    const rumor = JSON.parse(nip44.v2.decrypt(seal.content,
      nip44.v2.utils.getConversationKey(serviceKey, seal.pubkey)));
    expect(rumor.kind).toBe(30900);
    expect(rumor.id).toBe(getEventHash(rumor));
    expect(rumor.id).toBe(fixture.inner.id);
  });

  it('rejects a signer without NIP-44 before publishing', async () => {
    expect(sensitiveIntentBlocker({ nip44: false })).toMatch(/NIP-44/);
    expect(sensitiveIntentBlocker({ nip44: true })).toBeNull();
    await expect(giftWrapIntent(fixture.inner, fixture.service_pubkey,
      { getPublicKey: async () => fixture.operator_pubkey, signEvent: event => event }))
      .rejects.toThrow(/NIP-44/);
  });

  it('uses a fresh ephemeral author and a signed seal', async () => {
    const operatorKey = new Uint8Array(32); operatorKey[31] = 3;
    const signer = {
      getPublicKey: async () => fixture.operator_pubkey,
      signEvent: async event => finalizeEvent(event, operatorKey),
      encryptNip44: async (pubkey, plaintext) => nip44.v2.encrypt(plaintext,
        nip44.v2.utils.getConversationKey(operatorKey, pubkey))
    };
    const first = await giftWrapIntent(fixture.inner, fixture.service_pubkey, signer, { now: 1791040000 });
    const second = await giftWrapIntent(fixture.inner, fixture.service_pubkey, signer, { now: 1791040000 });
    expect(first.pubkey).not.toBe(second.pubkey);
    expect(first.created_at).toBeLessThan(1791040000);
    expect(verifyEvent(first)).toBe(true);
  });

  it('keeps only redacted pending metadata and resolves on 30315', async () => {
    const namespace = `giftwrap-${crypto.randomUUID()}`;
    let store = createPendingIntents({ namespace,
      servicePubkey: fixture.service_pubkey, requesterPubkey: fixture.operator_pubkey });
    await store.open();
    const row = await store.add({ event: fixture.inner, domain: 'secret', op: 'create', desiredState: null });
    expect(row.desiredState).toBeNull();
    expect(store.query()).toHaveLength(1);
    store.close();
    store = createPendingIntents({ namespace, servicePubkey: fixture.service_pubkey,
      requesterPubkey: fixture.operator_pubkey });
    await store.open();
    expect(store.query()).toEqual([expect.objectContaining({ desiredState: null, status: 'pending' })]);
    const coordinate = fixture.inner.tags.find(tag => tag[0] === 'd')[1];
    const intentId = fixture.inner.tags.find(tag => tag[0] === 'intent_id')[1];
    await store.handleStatus({ kind: 30315, pubkey: fixture.service_pubkey,
      tags: [['d', `intent-status:${fixture.operator_pubkey}:${coordinate}`], ['p', fixture.operator_pubkey],
        ['intent_id', intentId], ['status', 'accepted']], content: '{}' });
    expect(store.query()).toEqual([]);
    store.close();
  });
});
