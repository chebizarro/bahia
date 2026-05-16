import { describe, expect, it, vi, beforeEach, afterEach } from 'vitest';

const nostrMock = vi.hoisted(() => ({
  queryUntilEose: vi.fn()
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

describe('Nostr branch behavior coverage', () => {
  let fetchRepoBranches;
  let isNostrRepository;
  let NostrIncompleteEOSEError;

  beforeEach(async () => {
    vi.clearAllMocks();
    vi.resetModules();
    const clientModule = await import('../../src/lib/nostr/client.js');
    NostrIncompleteEOSEError = clientModule.NostrIncompleteEOSEError;
    const branchModule = await import('../../src/lib/nostr/branches.js');
    fetchRepoBranches = branchModule.fetchRepoBranches;
    isNostrRepository = branchModule.isNostrRepository;
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('fetches repository branches with an EOSE-authoritative query and selects the latest state event', async () => {
    const pubkey = 'a'.repeat(64);
    nostrMock.queryUntilEose.mockResolvedValue([
      repoStateEvent({ id: 'old', pubkey, created_at: 10, tags: [['d', 'bahia'], ['refs/heads/old', 'old']] }),
      repoStateEvent({ id: 'new', pubkey, created_at: 20 })
    ]);

    const result = await fetchRepoBranches(repoCoordinate(pubkey, 'bahia'), { timeout: 2500 });

    expect(nostrMock.queryUntilEose).toHaveBeenCalledWith([
      { kinds: [30618], authors: [pubkey], '#d': ['bahia'] }
    ], { timeoutMs: 2500 });
    expect(result).toEqual({
      branches: ['main', 'feature/auth'],
      defaultBranch: 'main',
      error: null
    });
  });

  it('returns an explicit degraded error when branch history is incomplete before EOSE', async () => {
    nostrMock.queryUntilEose.mockRejectedValue(new NostrIncompleteEOSEError('timeout', {
      partialEvents: [repoStateEvent()],
      relaySummary: [{ relay: 'wss://relay.example', status: 'pending' }]
    }));

    const result = await fetchRepoBranches(repoCoordinate(), { timeout: 100 });

    expect(result.branches).toEqual([]);
    expect(result.defaultBranch).toBeNull();
    expect(result.error).toContain('Nostr query did not receive complete EOSE history: timeout');
  });

  it('keeps non-Nostr repository selections out of NIP-34 branch lookup', () => {
    expect(isNostrRepository({ source: 'manual', repoCoordinate: repoCoordinate() })).toBe(false);
    expect(isNostrRepository({ source: 'nip34', repoCoordinate: repoCoordinate() })).toBe(true);
  });
});
