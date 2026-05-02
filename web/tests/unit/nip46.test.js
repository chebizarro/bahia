import { describe, it, expect, beforeEach, vi } from 'vitest';

describe('NIP-46 Utilities', () => {
  let nip46Module;

  beforeEach(async () => {
    vi.resetModules();
    delete global.window;
    global.window = {};
    nip46Module = await import('../../src/lib/nostr/nip46.js');
  });

  describe('parseNostrConnectUri', () => {
    it('parses valid nostrconnect URI', () => {
      const uri = `nostrconnect://${'a'.repeat(64)}?relay=wss://relay.one&relay=wss://relay.two&secret=shh`;
      const parsed = nip46Module.parseNostrConnectUri(uri);

      expect(parsed.signerPubkey).toBe('a'.repeat(64));
      expect(parsed.relays).toEqual(['wss://relay.one', 'wss://relay.two']);
      expect(parsed.secret).toBe('shh');
    });

    it('rejects invalid scheme', () => {
      expect(() => nip46Module.parseNostrConnectUri('https://example.com')).toThrow('URI must start with nostrconnect://');
    });

    it('rejects missing relays', () => {
      const uri = `nostrconnect://${'a'.repeat(64)}?secret=shh`;
      expect(() => nip46Module.parseNostrConnectUri(uri)).toThrow('at least one relay');
    });
  });

  describe('connectNip46', () => {
    it('connects via provider and returns pubkey + relays', async () => {
      const provider = {
        connect: vi.fn().mockResolvedValue(),
        getPublicKey: vi.fn().mockResolvedValue('b'.repeat(64)),
        getRelays: vi.fn().mockResolvedValue({ 'wss://relay.one': { read: true, write: true } })
      };
      global.window.nostr = { nip46: provider };

      const result = await nip46Module.connectNip46(`nostrconnect://${'a'.repeat(64)}?relay=wss://relay.one&secret=shh`);

      expect(provider.connect).toHaveBeenCalled();
      expect(result.pubkey).toBe('b'.repeat(64));
      expect(result.relays).toEqual({ 'wss://relay.one': { read: true, write: true } });
    });
  });
});
