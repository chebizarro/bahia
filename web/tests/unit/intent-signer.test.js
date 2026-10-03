import { describe, expect, it } from 'vitest';
import { finalizeEvent, getPublicKey } from 'nostr-tools';
import { buildIntentEvent, signIntent } from '../../src/lib/nostr/intent-signer.js';

const secret = new Uint8Array(32).fill(7);
const pubkey = getPublicKey(secret);
const orgId = '3b45458b-2724-4dda-9fc6-66f12249660d';
const intentId = '018f1fae-7b91-7bea-81d6-0669758de945';

describe('intent signer', () => {
  it('builds Go-compatible ordered tags and sorted JSON without mutating content', () => {
    const content = { name: 'api', id: 'record-1', config: { z: 1, a: 2 } };
    const event = buildIntentEvent({ domain: 'service', op: 'update', coordinate: 'service:record-1',
      orgId, intentId, content, currentRecord: { content: '{"updated_at":42}' }, createdAt: 123, pubkey });
    expect(event.tags).toEqual([
      ['d', 'service:record-1'], ['domain', 'service'], ['schema', 'bahia.intent.service.v1'],
      ['t', 'bahia-intent'], ['t', 'service'], ['op', 'update'], ['org', orgId], ['intent_id', intentId]
    ]);
    expect(event.content).toBe('{"config":{"a":2,"z":1},"expected_updated_at":42,"id":"record-1","name":"api"}');
    expect(content).not.toHaveProperty('expected_updated_at');
  });

  it('requires a current revision for updates', () => {
    expect(() => buildIntentEvent({ domain: 'service', coordinate: 'service:record-1', orgId }))
      .toThrow(/canonical updated_at/);
  });

  it('mints UUIDv7 intent ids and matches Go JSON HTML escaping', () => {
    const event = buildIntentEvent({ domain: 'service', op: 'create', coordinate: 'record-2', orgId,
      content: { name: '<api>&' }, createdAt: 123 });
    expect(event.tags[7][1]).toMatch(/^[0-9a-f]{8}-[0-9a-f]{4}-7[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/);
    expect(event.content).toBe('{"name":"\\u003capi\\u003e\\u0026"}');
  });

  it('signs with the same abstraction used by NIP-07, NIP-46 and e2e', async () => {
    const signer = { getPublicKey: async () => pubkey, signEvent: async event => finalizeEvent(event, secret) };
    const { event } = await signIntent({ domain: 'service', op: 'create', coordinate: 'service:new',
      orgId, intentId, content: { name: 'new' }, createdAt: 123 }, signer);
    expect(event.id).toMatch(/^[0-9a-f]{64}$/);
    expect(event.sig).toMatch(/^[0-9a-f]{128}$/);
  });
});
