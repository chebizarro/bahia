import { describe, it, expect, beforeEach, vi } from 'vitest';

describe('NIP-07 Utilities', () => {
  let nip07Module;

  beforeEach(async () => {
    // Reset modules to avoid state leakage
    vi.resetModules();
    
    // Clear window.nostr before each test
    delete global.window;
    global.window = {};
    
    // Dynamically import to get fresh module state
    nip07Module = await import('../../src/lib/nostr/nip07.js');
  });

  describe('detectNip07', () => {
    it('should detect SSR environment (no window)', async () => {
      delete global.window;
      
      // Re-import module in SSR context
      vi.resetModules();
      const module = await import('../../src/lib/nostr/nip07.js');
      
      const result = module.detectNip07();
      
      expect(result).toEqual({
        available: false,
        provider: null,
        reason: 'not_browser'
      });
    });

    it('should detect missing window.nostr', () => {
      const result = nip07Module.detectNip07();
      
      expect(result).toEqual({
        available: false,
        provider: null,
        reason: 'missing_window_nostr'
      });
    });

    it('should detect available extension', () => {
      const mockNostr = {
        getPublicKey: vi.fn(),
        signEvent: vi.fn()
      };
      global.window.nostr = mockNostr;
      
      const result = nip07Module.detectNip07();
      
      expect(result).toEqual({
        available: true,
        provider: mockNostr,
        reason: null
      });
    });
  });

  describe('waitForNip07', () => {
    it('should resolve immediately if extension is available', async () => {
      const mockNostr = {
        getPublicKey: vi.fn(),
        signEvent: vi.fn()
      };
      global.window.nostr = mockNostr;
      
      const result = await nip07Module.waitForNip07({ timeoutMs: 100 });
      
      expect(result.available).toBe(true);
      expect(result.provider).toBe(mockNostr);
    });

    it('should timeout if extension never becomes available', async () => {
      const startTime = Date.now();
      const result = await nip07Module.waitForNip07({ timeoutMs: 200, intervalMs: 50 });
      const elapsed = Date.now() - startTime;
      
      expect(result.available).toBe(false);
      expect(elapsed).toBeGreaterThanOrEqual(150);
      expect(elapsed).toBeLessThan(300);
    });

    it('should detect extension that loads after initial check', async () => {
      // Start with no extension
      setTimeout(() => {
        global.window.nostr = {
          getPublicKey: vi.fn(),
          signEvent: vi.fn()
        };
      }, 100);
      
      const result = await nip07Module.waitForNip07({ timeoutMs: 500, intervalMs: 50 });
      
      expect(result.available).toBe(true);
      expect(result.provider).toBe(global.window.nostr);
    });
  });

  describe('watchNip07Availability', () => {
    it('notifies subscribers when a provider is injected after startup', async () => {
      const changes = [];

      const stopWatching = nip07Module.watchNip07Availability((result) => {
        changes.push(result.available);
      });

      global.window.nostr = {
        getPublicKey: vi.fn(),
        signEvent: vi.fn()
      };

      await new Promise((resolve) => setTimeout(resolve, 0));

      expect(changes).toEqual([false, true]);
      stopWatching();
    });
  });

  describe('getPublicKey', () => {
    it('should throw when extension is not available', async () => {
      await expect(nip07Module.getPublicKey()).rejects.toThrow('NIP-07 extension not available');
    });

    it('should return valid hex pubkey', async () => {
      const validPubkey = 'a'.repeat(64);
      global.window.nostr = {
        getPublicKey: vi.fn().mockResolvedValue(validPubkey)
      };
      
      const pubkey = await nip07Module.getPublicKey();
      
      expect(pubkey).toBe(validPubkey);
      expect(global.window.nostr.getPublicKey).toHaveBeenCalled();
    });

    it('should reject invalid pubkey format (too short)', async () => {
      global.window.nostr = {
        getPublicKey: vi.fn().mockResolvedValue('abc123')
      };
      
      await expect(nip07Module.getPublicKey()).rejects.toThrow('Invalid public key format');
    });

    it('should reject invalid pubkey format (not hex)', async () => {
      global.window.nostr = {
        getPublicKey: vi.fn().mockResolvedValue('g'.repeat(64))
      };
      
      await expect(nip07Module.getPublicKey()).rejects.toThrow('Invalid public key format');
    });

    it('should reject invalid pubkey format (wrong length)', async () => {
      global.window.nostr = {
        getPublicKey: vi.fn().mockResolvedValue('a'.repeat(63))
      };
      
      await expect(nip07Module.getPublicKey()).rejects.toThrow('Invalid public key format');
    });

    it('should propagate errors from extension', async () => {
      global.window.nostr = {
        getPublicKey: vi.fn().mockRejectedValue(new Error('User denied'))
      };
      
      await expect(nip07Module.getPublicKey()).rejects.toThrow('Failed to get public key: User denied');
    });
  });

  describe('getRelays', () => {
    it('should return empty object when extension not available', async () => {
      await expect(nip07Module.getRelays()).rejects.toThrow('NIP-07 extension not available');
    });

    it('should return relay object when available', async () => {
      const mockRelays = {
        'wss://relay.damus.io': { read: true, write: true },
        'wss://relay.nostr.band': { read: true, write: false }
      };
      
      global.window.nostr = {
        getRelays: vi.fn().mockResolvedValue(mockRelays)
      };
      
      const relays = await nip07Module.getRelays();
      
      expect(relays).toEqual(mockRelays);
      expect(global.window.nostr.getRelays).toHaveBeenCalled();
    });

    it('should return empty object if getRelays is not implemented', async () => {
      global.window.nostr = {
        getPublicKey: vi.fn()
        // getRelays not implemented
      };
      
      const relays = await nip07Module.getRelays();
      
      expect(relays).toEqual({});
    });

    it('should return empty object if getRelays throws', async () => {
      global.window.nostr = {
        getRelays: vi.fn().mockRejectedValue(new Error('Not implemented'))
      };
      
      const consoleSpy = vi.spyOn(console, 'warn').mockImplementation(() => {});
      const relays = await nip07Module.getRelays();
      
      expect(relays).toEqual({});
      expect(consoleSpy).toHaveBeenCalledWith(
        'Failed to get relays from NIP-07:',
        expect.any(Error)
      );
      
      consoleSpy.mockRestore();
    });

    it('should return empty object if getRelays returns null', async () => {
      global.window.nostr = {
        getRelays: vi.fn().mockResolvedValue(null)
      };
      
      const relays = await nip07Module.getRelays();
      
      expect(relays).toEqual({});
    });
  });

  describe('signEvent', () => {
    it('should throw when extension not available', async () => {
      const event = { kind: 1, content: 'test' };
      
      await expect(nip07Module.signEvent(event)).rejects.toThrow('NIP-07 extension not available');
    });

    it('should throw when event is invalid', async () => {
      global.window.nostr = {
        signEvent: vi.fn()
      };
      
      await expect(nip07Module.signEvent(null)).rejects.toThrow('Invalid event object');
      await expect(nip07Module.signEvent('not an object')).rejects.toThrow('Invalid event object');
    });

    it('should sign valid event', async () => {
      const unsignedEvent = {
        kind: 1,
        content: 'Hello Nostr',
        tags: [],
        created_at: Math.floor(Date.now() / 1000)
      };
      
      const signedEvent = {
        ...unsignedEvent,
        id: 'event-id-123',
        sig: 'signature-abc',
        pubkey: 'a'.repeat(64)
      };
      
      global.window.nostr = {
        signEvent: vi.fn().mockResolvedValue(signedEvent)
      };
      
      const result = await nip07Module.signEvent(unsignedEvent);
      
      expect(result).toEqual(signedEvent);
      expect(global.window.nostr.signEvent).toHaveBeenCalledWith(unsignedEvent);
    });

    it('should propagate signing errors', async () => {
      const event = { kind: 1, content: 'test' };
      
      global.window.nostr = {
        signEvent: vi.fn().mockRejectedValue(new Error('User rejected'))
      };
      
      await expect(nip07Module.signEvent(event)).rejects.toThrow('Failed to sign event: User rejected');
    });
  });

  describe('NIP-44 encryption wrappers', () => {
    it('encrypts with window.nostr.nip44.encrypt', async () => {
      global.window.nostr = {
        nip44: {
          encrypt: vi.fn().mockResolvedValue('ciphertext'),
          decrypt: vi.fn()
        }
      };

      await expect(nip07Module.encryptNip44('b'.repeat(64), 'secret')).resolves.toBe('ciphertext');
      expect(global.window.nostr.nip44.encrypt).toHaveBeenCalledWith('b'.repeat(64), 'secret');
    });

    it('decrypts with window.nostr.nip44.decrypt', async () => {
      global.window.nostr = {
        nip44: {
          encrypt: vi.fn(),
          decrypt: vi.fn().mockResolvedValue('plaintext')
        }
      };

      await expect(nip07Module.decryptNip44('b'.repeat(64), 'ciphertext')).resolves.toBe('plaintext');
      expect(global.window.nostr.nip44.decrypt).toHaveBeenCalledWith('b'.repeat(64), 'ciphertext');
    });

    it('documents missing NIP-44 support as a hard blocker', async () => {
      global.window.nostr = { signEvent: vi.fn() };

      await expect(nip07Module.encryptNip44('b'.repeat(64), 'secret')).rejects.toThrow('does not expose NIP-44');
      await expect(nip07Module.decryptNip44('b'.repeat(64), 'ciphertext')).rejects.toThrow('does not expose NIP-44');
    });

    it('validates NIP-44 pubkeys', async () => {
      global.window.nostr = {
        nip44: { encrypt: vi.fn(), decrypt: vi.fn() }
      };

      await expect(nip07Module.encryptNip44('not-hex', 'secret')).rejects.toThrow('Invalid recipient pubkey');
      await expect(nip07Module.decryptNip44('not-hex', 'ciphertext')).rejects.toThrow('Invalid sender pubkey');
    });

    it('retries transient NIP-44 bridge failures before succeeding', async () => {
      const encrypt = vi.fn()
        .mockRejectedValueOnce(new Error('aka-profiles: Could not establish connection. Receiving end does not exist.'))
        .mockResolvedValueOnce('ciphertext');

      global.window.nostr = {
        nip44: {
          encrypt,
          decrypt: vi.fn()
        }
      };

      await expect(nip07Module.encryptNip44('b'.repeat(64), 'secret')).resolves.toBe('ciphertext');
      expect(encrypt).toHaveBeenCalledTimes(2);
    });

    it('serializes concurrent NIP-44 encryption calls against the provider', async () => {
      const order = [];
      global.window.nostr = {
        nip44: {
          encrypt: vi.fn(async (_pubkey, plaintext) => {
            order.push(`start:${plaintext}`);
            await new Promise((resolve) => setTimeout(resolve, 10));
            order.push(`end:${plaintext}`);
            return `cipher:${plaintext}`;
          }),
          decrypt: vi.fn()
        }
      };

      const [first, second] = await Promise.all([
        nip07Module.encryptNip44('b'.repeat(64), 'one'),
        nip07Module.encryptNip44('b'.repeat(64), 'two')
      ]);

      expect(first).toBe('cipher:one');
      expect(second).toBe('cipher:two');
      expect(order).toEqual(['start:one', 'end:one', 'start:two', 'end:two']);
    });
  });

  describe('getCapabilities', () => {
    it('should return all false when extension not available', () => {
      const caps = nip07Module.getCapabilities();
      
      expect(caps).toEqual({
        getPublicKey: false,
        signEvent: false,
        getRelays: false,
        nip04: false,
        nip44: false
      });
    });

    it('should detect basic capabilities', () => {
      global.window.nostr = {
        getPublicKey: vi.fn(),
        signEvent: vi.fn()
      };
      
      const caps = nip07Module.getCapabilities();
      
      expect(caps.getPublicKey).toBe(true);
      expect(caps.signEvent).toBe(true);
      expect(caps.getRelays).toBe(false);
      expect(caps.nip04).toBe(false);
      expect(caps.nip44).toBe(false);
    });

    it('should detect NIP-04 capabilities', () => {
      global.window.nostr = {
        getPublicKey: vi.fn(),
        signEvent: vi.fn(),
        nip04: {
          encrypt: vi.fn(),
          decrypt: vi.fn()
        }
      };
      
      const caps = nip07Module.getCapabilities();
      
      expect(caps.nip04).toBe(true);
    });

    it('should detect NIP-44 capabilities', () => {
      global.window.nostr = {
        getPublicKey: vi.fn(),
        signEvent: vi.fn(),
        nip44: {
          encrypt: vi.fn(),
          decrypt: vi.fn()
        }
      };
      
      const caps = nip07Module.getCapabilities();
      
      expect(caps.nip44).toBe(true);
    });

    it('should detect all capabilities', () => {
      global.window.nostr = {
        getPublicKey: vi.fn(),
        signEvent: vi.fn(),
        getRelays: vi.fn(),
        nip04: {
          encrypt: vi.fn(),
          decrypt: vi.fn()
        },
        nip44: {
          encrypt: vi.fn(),
          decrypt: vi.fn()
        }
      };
      
      const caps = nip07Module.getCapabilities();
      
      expect(caps).toEqual({
        getPublicKey: true,
        signEvent: true,
        getRelays: true,
        nip04: true,
        nip44: true
      });
    });
  });

  describe('getNip07Signer', () => {
    it('exposes NIP-44 encrypt/decrypt on the signer-shaped contract', () => {
      const signer = nip07Module.getNip07Signer();
      expect(signer.encryptNip44).toBe(nip07Module.encryptNip44);
      expect(signer.decryptNip44).toBe(nip07Module.decryptNip44);
    });
  });
});
