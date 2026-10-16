import { describe, expect, it, vi } from 'vitest';
import 'fake-indexeddb/auto';
import { finalizeEvent } from 'nostr-tools';
import fixture from '../fixtures/trust-epoch-envelopes.json';
import { createTrustEpochGuard, parseTrustEpoch, parseTrustEpochAck, verifiedInventory } from '../../src/lib/nostr/trust-epoch.js';
import { createBahiaEventStore } from '../../src/lib/nostr/store.js';

const oldSecret = Uint8Array.from(Buffer.from('2'.repeat(64), 'hex'));
const newSecret = Uint8Array.from(Buffer.from('3'.repeat(64), 'hex'));
const storage = () => {
  const values = new Map();
  return { getItem: key => values.has(key) ? values.get(key) : null,
    setItem: (key, value) => values.set(key, value) };
};
const guard = (overrides = {}) => createTrustEpochGuard({ serviceID: 'deployment-a',
  operatorRoot: fixture.root_pubkey, legacyPubkeys: [fixture.old_pubkey], storage: storage(), ...overrides });
const note = (key, at, content) => finalizeEvent({ kind: 1, created_at: at, tags: [], content }, key);

describe('trust epoch envelopes and admission', () => {
  it('round-trips the shared signed epoch and ack envelopes byte-exactly', () => {
    const epoch = parseTrustEpoch(fixture.epoch, 'deployment-a', fixture.root_pubkey);
    expect(JSON.stringify(epoch)).toBe(fixture.epoch.content);
    const ack = parseTrustEpochAck(fixture.ack, { serviceID: 'deployment-a', consumerID: 'daemon',
      epoch: 1, epochEventID: fixture.epoch.id,
      incidentID: JSON.parse(fixture.epoch.content).incident_id, newPubkey: fixture.new_pubkey });
    expect(JSON.stringify(ack)).toBe(fixture.ack.content);
    expect(fixture.ack.tags).toEqual([['d', 'trust-epoch-ack:deployment-a:daemon'],
      ['t', 'identity-recovery-receipt'], ['domain', 'identity'], ['schema', 'bahia.trust-epoch-ack.v1'],
      ['legacy_kind', '32053'], ['epoch', '1'], ['deleted', 'false']]);
  });

  it('fails closed on missing inventory, bad signature, and malformed present epoch', () => {
    const old = note(oldSecret, 140, 'old');
    const current = note(newSecret, 160, 'new');
    const g = guard();
    g.observe(fixture.epoch);
    expect(g.pubkeys).toEqual([fixture.new_pubkey]);
    expect(g.judgeEvent(old)).toBe('hostile');
    expect(g.judgeEvent(current)).toBe('accept');
    expect(verifiedInventory([], JSON.parse(fixture.epoch.content).inventory_digest, 0)).toEqual(new Set());
    const malformed = { ...fixture.epoch, tags: fixture.epoch.tags.map(tag => [...tag]) };
    malformed.tags[1][1] = 'wrong';
    expect(() => guard().observe(malformed)).toThrow(/Unverifiable/);
    expect(() => parseTrustEpoch({ ...fixture.epoch, sig: '0'.repeat(128) }, 'deployment-a', fixture.root_pubkey)).toThrow();
  });

  it('prunes old-key cache on activation and rejects non-inventoried replay at ingest', async () => {
    const store = createBahiaEventStore({ servicePubkeyPrefix: 'epoch-test',
      trustEpoch: { serviceID: 'deployment-a', operatorRoot: fixture.root_pubkey,
        legacyPubkeys: [fixture.old_pubkey], storage: storage() } });
    await store.open();
    try {
      const old = note(oldSecret, 140, 'old cached');
      expect(store.ingest(old)).toBe(true);
      expect(store.ingest(fixture.epoch)).toBe(true);
      expect(store.getTrustIdentity()).toEqual({ pubkeys: [fixture.new_pubkey], epoch: 1 });
      expect(store.query({ ids: [old.id] })).toEqual([]);
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
      expect(store.ingest(old)).toBe(false);
      expect(store.hostileTrustEpochEvents).toBeGreaterThan(0);
      warn.mockRestore();
      expect(store.ingest(note(newSecret, 160, 'new'))).toBe(true);
    } finally { await store.close(); }
  });

  it('poisons durable admission when a root-coordinate event has an invalid signature', async () => {
    const durable = storage();
    const store = createBahiaEventStore({ servicePubkeyPrefix: 'epoch-poison',
      trustEpoch: { serviceID: 'deployment-a', operatorRoot: fixture.root_pubkey,
        legacyPubkeys: [fixture.old_pubkey], storage: durable } });
    await store.open();
    try {
      const old = note(oldSecret, 140, 'old before poisoned head');
      expect(store.ingest(old)).toBe(true);
      const malformed = { ...fixture.epoch, sig: '0'.repeat(128) };
      const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
      expect(store.ingest(malformed)).toBe(false);
      expect(store.query({ ids: [old.id] })).toEqual([]);
      expect(store.ingest(note(newSecret, 160, 'new while poisoned'))).toBe(false);
      warn.mockRestore();
    } finally { await store.close(); }
    expect(() => createTrustEpochGuard({ serviceID: 'deployment-a', operatorRoot: fixture.root_pubkey,
      legacyPubkeys: [fixture.old_pubkey], storage: durable })).toThrow(/Corrupt persisted/);
  });
});
