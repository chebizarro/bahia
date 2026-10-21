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

it('remembers every retired identity after three rotations and durable reload', () => {
 const durable = storage();
 const g = guard({storage: durable});
 const secrets = [oldSecret, newSecret, ...['4','5'].map(c => Uint8Array.from(Buffer.from(c.repeat(64),'hex')))];
 const pubkeys = secrets.map(key => note(key,200,'identity').pubkey);
 let last;
 for (let i=1; i<secrets.length; i++) {
  const body = {...JSON.parse(fixture.epoch.content), epoch:i, old_pubkey:pubkeys[i-1], new_pubkey:pubkeys[i]};
  last = finalizeEvent({...fixture.epoch, created_at:fixture.epoch.created_at+i, content:JSON.stringify(body), tags:fixture.epoch.tags.map(tag => tag[0]==='epoch' ? ['epoch',String(i)] : [...tag])},Uint8Array.from(Buffer.from('1'.repeat(64),'hex')));
  g.observe(last);
 }
 const assertRetired = candidate => {
  for (const key of secrets.slice(0,-1)) expect(candidate.judgeEvent(note(key,200,'retired'))).toBe('hostile');
  expect(candidate.judgeEvent(note(secrets.at(-1),200,'current'))).toBe('accept');
  expect(candidate.judgeEvent(note(Uint8Array.from(Buffer.from('6'.repeat(64),'hex')),200,'component'))).toBe('accept');
 };
 assertRetired(g);
 const reloaded = guard({storage:durable});
 reloaded.hydrate([last]);
 assertRetired(reloaded);
});

it('refuses a compacted old ledger instead of inventing lost lineage', () => {
 const durable = storage();
 const g = guard({storage:durable});
 g.observe(fixture.epoch);
 const key='bahia:trust-epoch:deployment-a';
 const prior=JSON.parse(durable.getItem(key));
 delete prior.known_pubkeys;
 prior.old_pubkey=fixture.new_pubkey;
 prior.new_pubkey=note(Uint8Array.from(Buffer.from('4'.repeat(64),'hex')),200,'next').pubkey;
 prior.epoch=2;
 durable.setItem(key,JSON.stringify(prior));
 expect(() => guard({storage:durable})).toThrow(/lineage is incomplete/);
});

it('retains a legacy unactivated candidate when a different candidate supersedes it', () => {
 const durable=storage();
 const root=Uint8Array.from(Buffer.from('1'.repeat(64),'hex'));
 const original={...JSON.parse(fixture.epoch.content),activated:false};
 const first=finalizeEvent({...fixture.epoch,content:JSON.stringify(original)},root);
 guard({storage:durable}).observe(first);
 const storageKey='bahia:trust-epoch:deployment-a';
 const legacy=JSON.parse(durable.getItem(storageKey));delete legacy.known_pubkeys;
 durable.setItem(storageKey,JSON.stringify(legacy));
 const replacementKey=Uint8Array.from(Buffer.from('4'.repeat(64),'hex'));
 const second=finalizeEvent({...fixture.epoch,created_at:first.created_at+1,
  tags:fixture.epoch.tags.map(tag=>tag[0]==='epoch'?['epoch','2']:[...tag]),
  content:JSON.stringify({...original,epoch:2,activated:true,new_pubkey:note(replacementKey,200,'candidate').pubkey})},root);
 const migrated=guard({storage:durable});migrated.observe(second);
 expect(migrated.judgeEvent(note(newSecret,200,'abandoned candidate'))).toBe('hostile');
 const reloaded=guard({storage:durable});reloaded.hydrate([second]);
 expect(reloaded.judgeEvent(note(newSecret,200,'abandoned candidate'))).toBe('hostile');
});
