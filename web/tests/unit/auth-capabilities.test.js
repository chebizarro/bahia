import { describe, expect, it } from 'vitest';
import { supportsNostrAuthExchange } from '../../src/lib/auth/capabilities.js';

describe('supportsNostrAuthExchange', () => {
  it('returns true when backend advertises exchange support', () => {
    expect(supportsNostrAuthExchange({ features: { nostr_auth_exchange: true } })).toBe(true);
  });

  it('returns false when backend does not advertise exchange support', () => {
    expect(supportsNostrAuthExchange({ features: { nostr_auth_exchange: false } })).toBe(false);
    expect(supportsNostrAuthExchange({ features: {} })).toBe(false);
    expect(supportsNostrAuthExchange(null)).toBe(false);
  });
});
