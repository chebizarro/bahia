import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('$lib/stores/discovery.svelte.js', () => ({
  getBootstrapSeed: vi.fn()
}));

vi.mock('$lib/nostr/client.js', () => ({
  nostr: {
    setRelays: vi.fn(),
    connect: vi.fn()
  }
}));

describe('ensureRelayConnection', () => {
  beforeEach(() => {
    vi.resetModules();
    vi.clearAllMocks();
    global.window = global.window || {};
    global.document = global.document || {};
  });

  it('connects directly to deployment-configured browser relays', async () => {
    const discoveryModule = await import('$lib/stores/discovery.svelte.js');
    const nostrModule = await import('$lib/nostr/client.js');
    discoveryModule.getBootstrapSeed.mockReturnValue({
      relay_urls: ['wss://relay-1.example', 'https://relay-2.example/relay']
    });
    nostrModule.nostr.connect.mockResolvedValue({
      connected: 2,
      relays: [
        { url: 'wss://relay-1.example', status: 'connected' },
        { url: 'wss://relay-2.example/relay', status: 'connected' }
      ]
    });

    const { ensureRelayConnection } = await import('$lib/nostr/connection-guard.js');
    await ensureRelayConnection();

    expect(nostrModule.nostr.setRelays).toHaveBeenCalledWith([
      'wss://relay-1.example',
      'wss://relay-2.example/relay'
    ], false);
    expect(nostrModule.nostr.connect).toHaveBeenCalledWith([
      'wss://relay-1.example',
      'wss://relay-2.example/relay'
    ], { force: false });
  });
});
