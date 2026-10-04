import { describe, expect, it, vi } from 'vitest';

const fixture = vi.hoisted(() => ({ events: [], servicePubkey: 'b'.repeat(64) }));
vi.mock('../../src/lib/nostr/boot.js', () => ({
  getEventStore: () => ({ query: () => fixture.events }),
  getServicePubkey: () => fixture.servicePubkey
}));
vi.mock('../../src/lib/stores/auth-roles.svelte.js', () => ({
  contentKeyStateFor: () => ({ key: null, status: 're-encryption pending' })
}));

import { readConfidentialTopic } from '../../src/lib/stores/collections/confidential-records.js';

describe('late superseded confidential records', () => {
  it('returns an explicit re-encryption pending marker instead of throwing', () => {
    fixture.events = [{ id: 'old-record', kind: 30900, pubkey: fixture.servicePubkey,
      created_at: 42, tags: [['d', 'org:record:one'], ['t', 'org'], ['legacy_kind', '32005']],
      content: JSON.stringify({ schema: 'bahia.confidential.aead.v1', key_org: 'org-one', key_version: 'v1' }) }];
    const result = readConfidentialTopic('org', 32005);
    expect(result.rows).toEqual([]);
    expect(result.unreadable).toBe(1);
    expect(result.reencryptionPending).toEqual([{ orgID: 'org-one', version: 1,
      dTag: 'org:record:one', eventId: 'old-record', status: 're-encryption pending' }]);
  });
});
