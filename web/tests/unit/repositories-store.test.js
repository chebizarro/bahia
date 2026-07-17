import { describe, it, expect, beforeEach, vi } from 'vitest';

vi.mock('$lib/stores/system.svelte.js', () => ({
  currentSystemInfo: vi.fn(),
  loadSystemInfo: vi.fn()
}));

vi.mock('$lib/nostr/client.js', () => ({
  nostr: {
    relays: ['wss://default-relay.example'],
    connect: vi.fn()
  },
  fetchRepositories: vi.fn()
}));

describe('repositories store', () => {
  let store;
  let systemModule;
  let nostrModule;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();

    systemModule = await import('$lib/stores/system.svelte.js');
    nostrModule = await import('$lib/nostr/client.js');
    systemModule.currentSystemInfo.mockReturnValue({
      nostr: {
        nip34_relays: ['wss://nip34.example', ' wss://nip34-backup.example ', 'wss://nip34.example']
      }
    });
    systemModule.loadSystemInfo.mockResolvedValue({ nostr: { nip34_relays: ['wss://loaded-nip34.example'] } });

    nostrModule.fetchRepositories.mockResolvedValue([]);
    store = await import('$lib/stores/repositories.svelte.js');
  });

  it('loads repositories through advertised NIP-34 relays without fabricating CI state', async () => {
    const repos = [
      { displayName: 'Alpha', repoCoordinate: 'github.com/org/alpha', primaryUrl: 'https://github.com/org/alpha' },
      { displayName: 'Beta', repoCoordinate: 'github.com/org/beta', primaryUrl: 'https://github.com/org/beta' }
    ];

    nostrModule.fetchRepositories.mockResolvedValue(repos);
    const result = await store.loadRepositories({ authors: ['bob', 'alice', 'bob'] });

    expect(nostrModule.nostr.connect).not.toHaveBeenCalled();
    expect(systemModule.loadSystemInfo).not.toHaveBeenCalled();
    expect(nostrModule.fetchRepositories).toHaveBeenCalledWith({
      authors: ['alice', 'bob'],
      relayUrls: ['wss://nip34.example', 'wss://nip34-backup.example']
    });
    expect(store.meta.relayUrls).toEqual(['wss://nip34.example', 'wss://nip34-backup.example']);
    expect(result).toHaveLength(2);
    expect(store.repositories[0]).not.toHaveProperty('ci');
    expect(store.repositories[1]).not.toHaveProperty('ci');
    expect(store).not.toHaveProperty('enrichRepositoriesWithCI');
    expect(store).not.toHaveProperty('ensureRepositoryConnection');
  });

  it('does not reload for same normalized authors and relay policy unless forced', async () => {
    nostrModule.fetchRepositories.mockResolvedValue([{ displayName: 'Alpha' }]);

    await store.loadRepositories({ authors: ['alice', 'bob'] });
    await store.loadRepositories({ authors: ['bob', 'alice'] });

    expect(nostrModule.fetchRepositories).toHaveBeenCalledTimes(1);

    await store.loadRepositories({ authors: ['bob', 'alice'], force: true });
    expect(nostrModule.fetchRepositories).toHaveBeenCalledTimes(2);
  });

  it('reloads when the advertised NIP-34 relay policy changes', async () => {
    nostrModule.fetchRepositories.mockResolvedValue([{ displayName: 'Alpha' }]);

    await store.loadRepositories({ force: true });
    systemModule.currentSystemInfo.mockReturnValue({
      nostr: { nip34_relays: ['wss://changed-nip34.example'] }
    });
    await store.loadRepositories();

    expect(nostrModule.fetchRepositories).toHaveBeenCalledTimes(2);
    expect(nostrModule.fetchRepositories).toHaveBeenLastCalledWith({
      authors: null,
      relayUrls: ['wss://changed-nip34.example']
    });
  });

  it('loads NIP-34 relays from system info when current info is not cached', async () => {
    systemModule.currentSystemInfo.mockReturnValue(null);
    systemModule.loadSystemInfo.mockResolvedValue({
      nostr: { nip34_relays: ['wss://loaded-nip34.example'] }
    });

    await store.loadRepositories({ force: true });

    expect(systemModule.loadSystemInfo).toHaveBeenCalledTimes(1);
    expect(nostrModule.fetchRepositories).toHaveBeenCalledWith({
      authors: null,
      relayUrls: ['wss://loaded-nip34.example']
    });
  });

  it('falls back to global repository relays when NIP-34 relay discovery fails', async () => {
    const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
    systemModule.currentSystemInfo.mockReturnValue(null);
    systemModule.loadSystemInfo.mockRejectedValue(new Error('discovery unavailable'));

    await store.loadRepositories({ force: true });

    expect(warn).toHaveBeenCalledWith('[repositories] Failed to load NIP-34 relay configuration:', expect.any(Error));
    expect(nostrModule.fetchRepositories).toHaveBeenCalledWith({ authors: null, relayUrls: [] });
    warn.mockRestore();
  });

  it('records degraded EOSE metadata for partial repository read models', async () => {
    const repos = [{ displayName: 'Alpha', repoCoordinate: 'github.com/org/alpha' }];
    Object.defineProperty(repos, 'eose', {
      value: {
        complete: false,
        degraded: { incomplete: true, reason: 'all_relays_closed', partialEventCount: 1 },
        relaySummary: [{ relay: 'wss://relay.example', status: 'closed', reason: 'relay closed' }]
      }
    });
    nostrModule.fetchRepositories.mockResolvedValue(repos);

    await store.loadRepositories({ force: true });

    expect(store.repositories).toHaveLength(1);
    expect(store.meta.eose).toMatchObject({
      complete: false,
      degraded: { incomplete: true, reason: 'all_relays_closed', partialEventCount: 1 },
      relaySummary: [{ relay: 'wss://relay.example', status: 'closed', reason: 'relay closed' }]
    });
  });

  it('rejects a failed refresh while retaining stale data only in reactive state', async () => {
    nostrModule.fetchRepositories.mockResolvedValueOnce([{ displayName: 'Alpha' }]);
    await store.loadRepositories({ force: true });
    const loadedAt = store.meta.lastLoadedAt;

    nostrModule.fetchRepositories.mockRejectedValueOnce(new Error('relay unavailable'));

    await expect(store.loadRepositories({ force: true })).rejects.toThrow('relay unavailable');
    expect(store.repositories).toEqual([{ displayName: 'Alpha' }]);
    expect(store.error.value).toBe('relay unavailable');
    expect(store.meta.lastLoadedAt).toBe(loadedAt);
    expect(store.loading.list).toBe(false);
  });

  it('filters repositories by text and requirePrimaryUrl option', () => {
    const repoList = [
      { displayName: 'Alpha Service', primaryUrl: 'https://github.com/org/alpha', repoCoordinate: 'github.com/org/alpha' },
      { displayName: 'Beta Utility', primaryUrl: '', repoCoordinate: 'github.com/org/beta' }
    ];

    expect(store.filterRepositories(repoList, 'alpha')).toHaveLength(1);
    expect(store.filterRepositories(repoList, 'github.com/org/beta')).toHaveLength(1);
    expect(store.filterRepositories(repoList, '', { requirePrimaryUrl: true })).toHaveLength(1);
  });

  it('resolves selection helpers for manual and NIP-34 repositories', () => {
    const manual = store.createManualRepositorySelection(' https://git.example/repo.git ');
    expect(manual).toEqual({
      source: 'manual',
      provider: 'manual',
      repoUrl: 'https://git.example/repo.git',
      displayName: 'https://git.example/repo.git'
    });

    const nip34Repo = {
      displayName: 'Alpha',
      primaryUrl: 'https://github.com/org/alpha/',
      cloneUrls: ['https://github.com/org/alpha.git'],
      webUrls: ['https://github.com/org/alpha'],
      relayUrls: ['wss://relay.example'],
      maintainers: ['npub1...'],
      repoCoordinate: 'github.com/org/alpha'
    };

    const resolved = store.resolveSelectionFromRepoUrl('https://github.com/org/alpha', [nip34Repo]);
    expect(resolved.source).toBe('nip34');
    expect(resolved.repoCoordinate).toBe('github.com/org/alpha');
    expect(resolved.relayUrls).toEqual(['wss://relay.example']);

    const fallback = store.resolveSelectionFromRepoUrl('https://unknown.example/repo', [nip34Repo]);
    expect(fallback.source).toBe('manual');
  });
});
