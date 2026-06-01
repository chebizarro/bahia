import { beforeEach, describe, expect, it, vi } from 'vitest';

vi.mock('$lib/stores/system.svelte.js', () => ({
  currentSystemInfo: vi.fn(),
  loadSystemInfo: vi.fn()
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

  it('loads system info and connects the shared nostr client to browser relays', async () => {
    const systemModule = await import('$lib/stores/system.svelte.js');
    const nostrModule = await import('$lib/nostr/client.js');
    systemModule.currentSystemInfo.mockReturnValue(null);
    systemModule.loadSystemInfo.mockResolvedValue({
      nostr: {
        browser_relays: ['wss://relay-1.example', 'https://relay-2.example/relay'],
        sidecar_url: 'wss://relay-1.example'
      }
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

    expect(systemModule.loadSystemInfo).toHaveBeenCalledTimes(1);
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
