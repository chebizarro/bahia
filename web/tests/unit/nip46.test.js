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

  describe('capabilities and NIP-44 wrappers', () => {
    it('reports an explicit NIP-44 blocker when provider lacks encrypt/decrypt', () => {
      global.window.nostr = { nip46: { signEvent: vi.fn() } };

      const caps = nip46Module.getCapabilities();

      expect(caps.nip44).toBe(false);
      expect(caps.nip44Blocker).toContain('does not expose a NIP-44');
    });

    it('encrypts and decrypts when the NIP-46 provider explicitly exposes nip44', async () => {
      const provider = {
        signEvent: vi.fn(),
        nip44: {
          encrypt: vi.fn().mockResolvedValue('ciphertext'),
          decrypt: vi.fn().mockResolvedValue('plaintext')
        }
      };
      global.window.nostr = { nip46: provider };

      expect(nip46Module.getCapabilities().nip44).toBe(true);
      await expect(nip46Module.encryptNip44('b'.repeat(64), 'secret')).resolves.toBe('ciphertext');
      await expect(nip46Module.decryptNip44('b'.repeat(64), 'ciphertext')).resolves.toBe('plaintext');
      expect(provider.nip44.encrypt).toHaveBeenCalledWith('b'.repeat(64), 'secret');
      expect(provider.nip44.decrypt).toHaveBeenCalledWith('b'.repeat(64), 'ciphertext');
    });

    it('throws the exact blocker from encrypt/decrypt when nip44 is unavailable', async () => {
      global.window.nostr = { nip46: { signEvent: vi.fn() } };

      await expect(nip46Module.encryptNip44('b'.repeat(64), 'secret')).rejects.toThrow('NIP-46 provider does not expose a NIP-44');
      await expect(nip46Module.decryptNip44('b'.repeat(64), 'ciphertext')).rejects.toThrow('NIP-46 provider does not expose a NIP-44');
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

      const result = await nip46Module.connectNip46(`nostrconnect://${'b'.repeat(64)}?relay=wss://relay.one&secret=shh`);

      expect(provider.connect).toHaveBeenCalled();
      expect(result.pubkey).toBe('b'.repeat(64));
      expect(result.relays).toEqual({ 'wss://relay.one': { read: true, write: true } });
    });

    it('rejects a provider identity that does not match the requested signer', async () => {
      const provider = {
        connect: vi.fn().mockResolvedValue(),
        getPublicKey: vi.fn().mockResolvedValue('b'.repeat(64)),
        signEvent: vi.fn()
      };
      global.window.nostr = { nip46: provider };

      await expect(nip46Module.connectNip46(
        `nostrconnect://${'a'.repeat(64)}?relay=wss://relay.one&secret=shh`
      )).rejects.toThrow('does not match the requested session');
    });

    it('never falls back to the top-level NIP-07 signer', async () => {
      const nip07GetPublicKey = vi.fn().mockResolvedValue('a'.repeat(64));
      const nip07SignEvent = vi.fn();
      const provider = { connect: vi.fn().mockResolvedValue() };
      global.window.nostr = {
        getPublicKey: nip07GetPublicKey,
        signEvent: nip07SignEvent,
        nip46: provider
      };

      await expect(nip46Module.connectNip46(
        `nostrconnect://${'a'.repeat(64)}?relay=wss://relay.one&secret=shh`
      )).rejects.toThrow('does not expose getPublicKey');
      expect(() => nip46Module.getNip46Signer()).toThrow('NIP-46 signer API is unavailable');
      expect(nip07GetPublicKey).not.toHaveBeenCalled();
      expect(nip07SignEvent).not.toHaveBeenCalled();
    });
  });
});
