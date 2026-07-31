import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest';

const nostrMock = vi.hoisted(() => ({
  subscribeWithRecovery: vi.fn(),
  subscribeWithRecoveryOnRelays: vi.fn()
}));

vi.mock('../../src/lib/nostr/client.js', async () => {
  const actual = await vi.importActual('../../src/lib/nostr/client.js');
  return {
    ...actual,
    nostr: nostrMock
  };
});

function repoCoordinate(pubkey = 'a'.repeat(64), identifier = 'bahia') {
  return `30617:${pubkey}:${identifier}`;
}

function repoStateEvent(overrides = {}) {
  return {
    id: overrides.id || 'state-event',
    kind: 30618,
    pubkey: overrides.pubkey || 'a'.repeat(64),
    created_at: overrides.created_at || 100,
    tags: overrides.tags || [
      ['d', 'bahia'],
      ['HEAD', 'ref: refs/heads/main'],
      ['refs/heads/main', 'commit-main'],
      ['refs/heads/feature/auth', 'commit-feature']
    ],
    content: overrides.content || '',
    sig: overrides.sig || 'b'.repeat(128)
  };
}

describe('NIP-34 repository parsing coverage', () => {
  it('preserves repository relay tag values from 30617 announcements', async () => {
    const { parseRepositoryEvent } = await import('../../src/lib/nostr/client.js');

    const parsed = parseRepositoryEvent({
      id: 'repo-event',
      kind: 30617,
      pubkey: 'a'.repeat(64),
      created_at: 100,
      tags: [
        ['d', 'bahia'],
        ['name', 'Bahia'],
        ['relays', 'wss://repo-a.example', 'wss://repo-b.example'],
        ['clone', 'https://github.com/example/bahia.git']
      ],
      content: '',
      sig: 'b'.repeat(128)
    });

    expect(parsed.relayUrls).toEqual(['wss://repo-a.example', 'wss://repo-b.example']);
    expect(parsed.searchText).toContain('wss://repo-a.example');
  });
});

describe('Nostr branch behavior coverage', () => {
  let fetchRepoBranches;
  let isNostrRepository;

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.resetModules();
    const branchModule = await import('../../src/lib/nostr/branches.js');
    fetchRepoBranches = branchModule.fetchRepoBranches;
    isNostrRepository = branchModule.isNostrRepository;
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
  });

  it('fetches repository branches from explicit NIP-34 relay URLs, settles on EOSE, and stays subscribed', async () => {
    const pubkey = 'a'.repeat(64);
    const unsubscribe = vi.fn();
    nostrMock.subscribeWithRecoveryOnRelays.mockImplementation((_relays, _filters, handlers) => {
      handlers.onEvent(repoStateEvent({ id: 'old', pubkey, created_at: 10, tags: [['d', 'bahia'], ['refs/heads/old', 'old']] }), 'wss://repo-a.example');
      handlers.onEvent(repoStateEvent({ id: 'new', pubkey, created_at: 20 }), 'wss://repo-b.example');
      handlers.onEose('wss://repo-a.example');
      handlers.onEose('wss://repo-b.example');
      return unsubscribe;
    });

    const result = await fetchRepoBranches(repoCoordinate(pubkey, 'bahia'), {
      timeout: 2500,
      relayUrls: ['wss://repo-a.example', 'wss://repo-b.example']
    });

    expect(nostrMock.subscribeWithRecoveryOnRelays).toHaveBeenCalledWith(
      ['wss://repo-a.example', 'wss://repo-b.example'],
      [{ kinds: [30618], authors: [pubkey], '#d': ['bahia'] }],
      expect.objectContaining({
        onEvent: expect.any(Function),
        onEose: expect.any(Function),
        onClosed: expect.any(Function)
      })
    );
    expect(unsubscribe).not.toHaveBeenCalled();
    expect(result).toMatchObject({
      branches: ['main', 'feature/auth'],
      defaultBranch: 'main',
      error: null,
      complete: true,
      degraded: null,
      relaySummary: expect.arrayContaining([
        expect.objectContaining({ relay: 'wss://repo-a.example', status: 'eose' }),
        expect.objectContaining({ relay: 'wss://repo-b.example', status: 'eose' })
      ])
    });
  });

  it('uses global relay fallback with degraded metadata when a NIP-34 selection has no repository relay hints', async () => {
    nostrMock.subscribeWithRecovery.mockImplementation((_filters, handlers) => {
      handlers.onEvent(repoStateEvent());
      handlers.onEose('wss://global.example');
      return vi.fn();
    });

    const result = await fetchRepoBranches(repoCoordinate(), { timeout: 100, relayUrls: [] });

    expect(nostrMock.subscribeWithRecovery).toHaveBeenCalledWith(
      [{ kinds: [30618], authors: ['a'.repeat(64)], '#d': ['bahia'] }],
      expect.objectContaining({
        onEvent: expect.any(Function),
        onEose: expect.any(Function),
        onClosed: expect.any(Function)
      })
    );
    expect(result.branches).toEqual(['main', 'feature/auth']);
    expect(result.complete).toBe(true);
    expect(result.degraded).toMatchObject({
      incomplete: false,
      reason: 'missing_repository_relays',
      partialEventCount: 0,
      relaySummary: expect.arrayContaining([expect.objectContaining({ relay: 'wss://global.example', status: 'eose' })])
    });
  });

  it('returns partial repo state when the branch subscription closes before EOSE', async () => {
    nostrMock.subscribeWithRecovery.mockImplementation((_filters, handlers) => {
      handlers.onEvent(repoStateEvent());
      handlers.onClosed('relay closed before EOSE', 'wss://relay.example');
      return vi.fn();
    });

    const result = await fetchRepoBranches(repoCoordinate(), { timeout: 100 });

    expect(result.branches).toEqual(['main', 'feature/auth']);
    expect(result.defaultBranch).toBe('main');
    expect(result.error).toBeNull();
    expect(result.complete).toBe(false);
    expect(result.degraded).toMatchObject({
      incomplete: true,
      reason: 'closed',
      partialEventCount: 1,
      relaySummary: [expect.objectContaining({ relay: 'wss://relay.example', status: 'closed', reason: 'relay closed before EOSE' })]
    });
  });

  it('returns a closed-subscription error when branch history closes with no partial events', async () => {
    nostrMock.subscribeWithRecovery.mockImplementation((_filters, handlers) => {
      handlers.onClosed('relay closed before EOSE', 'wss://relay.example');
      return vi.fn();
    });

    const result = await fetchRepoBranches(repoCoordinate(), { timeout: 100 });

    expect(result.branches).toEqual([]);
    expect(result.defaultBranch).toBeNull();
    expect(result.error).toBe('relay closed before EOSE');
    expect(result.complete).toBe(false);
    expect(result.degraded).toMatchObject({
      incomplete: true,
      reason: 'closed',
      partialEventCount: 0,
      relaySummary: [expect.objectContaining({ relay: 'wss://relay.example', status: 'closed', reason: 'relay closed before EOSE' })]
    });
  });

  it('returns AUTH-required branch closure metadata without treating it as complete history', async () => {
    nostrMock.subscribeWithRecovery.mockImplementation((_filters, handlers) => {
      handlers.onClosed('auth-required: sign in first', 'wss://auth.example', { terminal: true, source: 'auth', authRequired: true });
      return vi.fn();
    });

    const result = await fetchRepoBranches(repoCoordinate(), { timeout: 100 });

    expect(result.branches).toEqual([]);
    expect(result.defaultBranch).toBeNull();
    expect(result.error).toBe('auth-required: sign in first');
    expect(result.complete).toBe(false);
    expect(result.degraded).toMatchObject({
      incomplete: true,
      reason: 'auth-required',
      authRequired: true,
      partialEventCount: 0,
      relaySummary: [expect.objectContaining({ relay: 'wss://auth.example', status: 'auth-required' })]
    });
  });

  it('uses the timeout only as a fallback deadline and keeps recovery active', async () => {
    vi.useFakeTimers();
    const unsubscribe = vi.fn();
    nostrMock.subscribeWithRecovery.mockImplementation((_filters, handlers) => {
      handlers.onClosed('relay reconnecting', 'wss://relay.example', { terminal: false, recovering: true });
      return unsubscribe;
    });

    const resultPromise = fetchRepoBranches(repoCoordinate(), { timeout: 100 });
    let settled = false;
    resultPromise.then(() => { settled = true; });

    await vi.advanceTimersByTimeAsync(99);
    expect(settled).toBe(false);
    await vi.advanceTimersByTimeAsync(1);

    const result = await resultPromise;
    expect(result.complete).toBe(false);
    expect(result.error).toBe('Timed out waiting for repo state EOSE');
    expect(result.degraded).toMatchObject({ reason: 'timeout-before-eose' });
    expect(unsubscribe).not.toHaveBeenCalled();
  });

  it('keeps non-Nostr repository selections out of NIP-34 branch lookup', () => {
    expect(isNostrRepository({ source: 'manual', repoCoordinate: repoCoordinate() })).toBe(false);
    expect(isNostrRepository({ source: 'nip34', repoCoordinate: repoCoordinate() })).toBe(true);
  });
});
