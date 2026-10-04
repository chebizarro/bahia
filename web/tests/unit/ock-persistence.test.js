import 'fake-indexeddb/auto';
import { describe, expect, it } from 'vitest';
import { finalizeEvent, getPublicKey, nip44 } from 'nostr-tools';
import { base64Encode, OCK_WRAP_SCHEMA } from '../../src/lib/nostr/confidential.js';
import { createBahiaEventStore } from '../../src/lib/nostr/store.js';
import { contentKeyFor, startRoleDerivation, stopRoleDerivation } from '../../src/lib/stores/auth-roles.svelte.js';

const serviceSecret = new Uint8Array(32).fill(11);
const userSecret = new Uint8Array(32).fill(12);
const servicePubkey = getPublicKey(serviceSecret);
const userPubkey = getPublicKey(userSecret);

function persistedEvents(name) {
  return new Promise((resolve, reject) => {
    const request = indexedDB.open(name);
    request.onerror = () => reject(request.error);
    request.onsuccess = () => {
      const db = request.result;
      const get = db.transaction('events', 'readonly').objectStore('events').getAll();
      get.onsuccess = () => { db.close(); resolve(get.result); };
      get.onerror = () => { db.close(); reject(get.error); };
    };
  });
}

describe('OCK persistence boundary', () => {
  it('persists only the NIP-44 envelope event, never the unwrapped content key', async () => {
    const orgID = '0199c749-9300-7444-8444-444444444444';
    const key = new Uint8Array(32).fill(19);
    const wrap = JSON.stringify({ schema: OCK_WRAP_SCHEMA, org_id: orgID, key_ref: `ock:${orgID}`,
      version: 1, key: base64Encode(key), recipient_pubkey: userPubkey });
    const conversation = nip44.v2.utils.getConversationKey(serviceSecret, userPubkey);
    const ciphertext = nip44.v2.encrypt(wrap, conversation);
    const event = finalizeEvent({ kind: 30900, created_at: Math.floor(Date.now() / 1000),
      tags: [['d', `org-key:${orgID}:v1:fixture`], ['domain', 'org'], ['schema', 'bahia.cp-state.v1'],
        ['legacy_kind', '32010'], ['deleted', 'false'], ['t', 'org-key-envelope']],
      content: ciphertext }, serviceSecret);
    const namespace = `ock${crypto.randomUUID().slice(0, 5)}`;
    const database = `bahia-events-${namespace}`;
    const store = createBahiaEventStore({ servicePubkeyPrefix: namespace });
    await store.open();
    try {
      expect(store.ingest(event)).toBe(true);
      await startRoleDerivation({ store, userPubkey, servicePubkey, signer: {
        decryptNip44: async (sender, encrypted) => nip44.v2.decrypt(encrypted,
          nip44.v2.utils.getConversationKey(userSecret, sender))
      } });
      expect(contentKeyFor(orgID, 1)?.key).toEqual(key);
      const rows = await persistedEvents(database);
      expect(rows).toHaveLength(1);
      expect(rows[0].content).toBe(ciphertext);
      expect(JSON.stringify(rows)).not.toContain(base64Encode(key));
      expect(JSON.stringify(localStorage)).not.toContain(base64Encode(key));
    } finally {
      stopRoleDerivation();
      await store.close();
    }
  });
});
