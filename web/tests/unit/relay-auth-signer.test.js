import { afterEach, describe, expect, it, vi } from 'vitest';
import { getRelayAuthSigner, onRelayAuthSigner, resetRelayAuthSigner, setRelayAuthSigner } from '../../src/lib/nostr/relay-auth-signer.js';

afterEach(() => resetRelayAuthSigner());

describe('relay-auth-signer registry', () => {
  it('publishes the signer to listeners, replaying the current value on subscribe', () => {
    const seen = [];
    const stop = onRelayAuthSigner((signer) => seen.push(signer));
    const signer = vi.fn();
    setRelayAuthSigner(signer);
    setRelayAuthSigner('not a function');
    expect(getRelayAuthSigner()).toBeNull();
    expect(seen).toEqual([null, signer, null]);
    stop();
    setRelayAuthSigner(signer);
    expect(seen).toHaveLength(3);
  });
});
