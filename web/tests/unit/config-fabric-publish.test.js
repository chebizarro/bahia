import { beforeEach, describe, expect, it, vi } from 'vitest';

const state = vi.hoisted(() => ({ altered: false, results: [{ sent: true, accepted: true, message: '' }] }));
vi.mock('$lib/stores/auth.js', async () => {
  const { finalizeEvent, getPublicKey } = await import('nostr-tools');
  const key = new Uint8Array(32).fill(1);
  return {
    authState: { pubkey: getPublicKey(key) },
    signWithAuth: async (unsigned) => finalizeEvent(
      state.altered ? { ...unsigned, content: '{}' } : unsigned, key
    )
  };
});
vi.mock('$lib/nostr/subscriptions.js', () => ({
  nostr: { publish: vi.fn(async () => state.results) }
}));

import { nostr } from '../../src/lib/nostr/subscriptions.js';
import { buildConfigEvent, publishConfigEvent } from '../../src/lib/config-fabric/publish.js';

const policy = {
  kind: 30078, service_id: 'svc', policy_name: 'rates', scope: 'prod',
  version: 2, schema: 'cascadia.config.rates.v1', policy: { max: 10 },
  secret_refs: { db: { provider: 'signet', ref: 'db/main' } }
};

describe('Config Fabric signed publication', () => {
  beforeEach(() => {
    state.altered = false;
    state.results = [{ sent: true, accepted: true, message: '' }];
    vi.mocked(nostr.publish).mockClear();
  });

  it('builds the canonical NIP-78 desired event shape', () => {
    expect(buildConfigEvent(policy, 123)).toEqual({
      kind: 30078, created_at: 123,
      tags: [
        ['d', 'service:svc:rates'], ['t', 'config-fabric'], ['service', 'svc'],
        ['scope', 'prod'], ['version', '2'], ['schema', 'cascadia.config.rates.v1']
      ],
      content: JSON.stringify({
        service_id: 'svc', scope: 'prod', version: 2,
        schema: 'cascadia.config.rates.v1', policy: { max: 10 },
        secret_refs: { db: { provider: 'signet', ref: 'db/main' } }
      })
    });
  });

  it('encodes NIP-51 list items as tags, not policy content', () => {
    const event = buildConfigEvent({
      ...policy, kind: 30000, policy_name: 'membership',
      schema: 'cascadia.config.membership.v1',
      items: [{ tag: 'p', value: 'a'.repeat(64) }]
    }, 123);
    expect(event.tags.at(-1)).toEqual(['p', 'a'.repeat(64)]);
    expect(JSON.parse(event.content)).not.toHaveProperty('policy');
  });

  it('publishes a signer-verified event and requires an accepted relay OK', async () => {
    await expect(publishConfigEvent(policy)).resolves.toMatchObject({ version: 2, event_id: expect.any(String) });
    expect(nostr.publish).toHaveBeenCalledWith(expect.objectContaining({ kind: 30078 }));
    state.results = [{ sent: true, accepted: false, message: 'blocked: not admitted' }];
    await expect(publishConfigEvent(policy)).rejects.toThrow('blocked: not admitted');
  });

  it('refuses signer substitution before publication', async () => {
    state.altered = true;
    await expect(publishConfigEvent(policy)).rejects.toThrow('Signer returned a different Config Fabric event');
    expect(nostr.publish).not.toHaveBeenCalled();
  });
});
