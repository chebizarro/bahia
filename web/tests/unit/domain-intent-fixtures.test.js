import 'fake-indexeddb/auto';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { buildIntentEvent } from '../../src/lib/nostr/intent-signer.js';
import { createPendingIntents } from '../../src/lib/stores/pending-intents.svelte.js';
import { FLEET_INTENT_ORG_ID, resolveIntentOrgId } from '../../src/lib/nostr/intent-client.svelte.js';

const servicePubkey = 'b'.repeat(64);
const families = ['llm', 'backup', 'package'];
const fixtures = families.flatMap(family => {
  const fixture = JSON.parse(readFileSync(join(process.cwd(), 'tests', 'fixtures', `${family}-intents.json`), 'utf8'));
  return fixture.cases;
});

describe('Go-generated domain intent fixtures', () => {
  it('provides a valid fleet org tag without requiring operator org membership', () => {
    expect(resolveIntentOrgId('backup')).toBe(FLEET_INTENT_ORG_ID);
    expect(resolveIntentOrgId('package')).toBe(FLEET_INTENT_ORG_ID);
    expect(() => resolveIntentOrgId('llm')).toThrow(/organization/);
    expect(resolveIntentOrgId('backup', '3b45458b-2724-4dda-9fc6-66f12249660d')).toBe('3b45458b-2724-4dda-9fc6-66f12249660d');
  });

  for (const fixture of fixtures) {
    it(`${fixture.request.domain}/${fixture.name} matches ParseIntent and handler content`, () => {
      expect(buildIntentEvent(fixture.request)).toEqual(fixture.event);
    });

    it(`${fixture.request.domain}/${fixture.name} persists pending and resolves on scoped 30315`, async () => {
      const requesterPubkey = fixture.request.pubkey;
      const store = createPendingIntents({ namespace: `fixture-${fixture.request.intentId}`,
        servicePubkey, requesterPubkey });
      await store.open();
      const event = { ...fixture.event, id: fixture.request.intentId };
      await store.add({ event, domain: fixture.request.domain, op: fixture.request.op,
        desiredState: fixture.request.content });
      expect(store.query({ domain: fixture.request.domain, status: 'pending' })).toHaveLength(1);
      const status = { kind: 30315, pubkey: servicePubkey,
        tags: [['d', `intent-status:${requesterPubkey}:${fixture.request.coordinate}`],
          ['p', requesterPubkey], ['intent_id', fixture.request.intentId], ['status', 'accepted']],
        content: '{"status":"accepted"}' };
      expect(await store.handleStatus({ ...status, pubkey: requesterPubkey })).toBe(false);
      expect(store.query()).toHaveLength(1);
      expect(await store.handleStatus(status)).toBe(true);
      expect(store.query()).toHaveLength(0);
      store.close();
    });
  }
});
