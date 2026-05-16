import { describe, it, expect, beforeEach, vi } from 'vitest';

vi.mock('$lib/stores/auth.js', () => ({
  authState: { relays: {} }
}));

vi.mock('$lib/nostr/client.js', () => ({
  nostr: {
    relays: ['wss://default-relay.example'],
    connect: vi.fn()
  },
  fetchRepositories: vi.fn()
}));

vi.mock('$lib/api/client.js', () => ({
  api: {
    lookupRepositoryCI: vi.fn()
  }
}));

describe('repositories store', () => {
  let store;
  let authModule;
  let nostrModule;
  let apiModule;

  beforeEach(async () => {
    vi.resetModules();
    vi.clearAllMocks();

    authModule = await import('$lib/stores/auth.js');
    nostrModule = await import('$lib/nostr/client.js');
    apiModule = await import('$lib/api/client.js');

    authModule.authState.relays = {
      'wss://auth-read.example': { read: true, write: true },
      'wss://auth-write-only.example': { read: false, write: true }
    };

    nostrModule.fetchRepositories.mockResolvedValue([]);
    apiModule.api.lookupRepositoryCI.mockResolvedValue([]);

    store = await import('$lib/stores/repositories.svelte.js');
  });

  it('loads repositories, uses auth relays, and marks CI as public read-model only', async () => {
    const repos = [
      { displayName: 'Alpha', repoCoordinate: 'github.com/org/alpha', primaryUrl: 'https://github.com/org/alpha' },
      { displayName: 'Beta', repoCoordinate: 'github.com/org/beta', primaryUrl: 'https://github.com/org/beta' }
    ];

    nostrModule.fetchRepositories.mockResolvedValue(repos);
    const result = await store.loadRepositories({ authors: ['bob', 'alice', 'bob'] });

    expect(nostrModule.nostr.connect).not.toHaveBeenCalled();
    expect(nostrModule.fetchRepositories).toHaveBeenCalledWith({ authors: ['alice', 'bob'] });
    expect(result).toHaveLength(2);

    await Promise.resolve();

    expect(apiModule.api.lookupRepositoryCI).not.toHaveBeenCalled();
    expect(store.repositories[0].ci.state).toBe('empty');
    expect(store.repositories[1].ci.state).toBe('empty');
  });

  it('does not reload for same normalized authors unless forced', async () => {
    nostrModule.fetchRepositories.mockResolvedValue([{ displayName: 'Alpha' }]);

    await store.loadRepositories({ authors: ['alice', 'bob'] });
    await store.loadRepositories({ authors: ['bob', 'alice'] });

    expect(nostrModule.fetchRepositories).toHaveBeenCalledTimes(1);

    await store.loadRepositories({ authors: ['bob', 'alice'], force: true });
    expect(nostrModule.fetchRepositories).toHaveBeenCalledTimes(2);
  });

  it('sets unsupported CI state when repositories have no coordinates', async () => {
    const repoList = [{ displayName: 'Local repo' }];

    await store.enrichRepositoriesWithCI(repoList);

    expect(apiModule.api.lookupRepositoryCI).not.toHaveBeenCalled();
    expect(repoList[0].ci).toEqual({ state: 'unsupported', lookup: null, error: null });
  });

  it('does not call REST CI lookup while enriching public repository cards', async () => {
    const repoList = [{ displayName: 'Alpha', repoCoordinate: 'github.com/org/alpha' }];
    apiModule.api.lookupRepositoryCI.mockRejectedValue(new Error('CI backend unavailable'));

    await store.enrichRepositoriesWithCI(repoList);

    expect(apiModule.api.lookupRepositoryCI).not.toHaveBeenCalled();
    expect(store.ciError.value).toBe(null);
    expect(repoList[0].ci).toEqual({ state: 'empty', lookup: null, error: null });
    expect(store.ciLoading.value).toBe(false);
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

    const fallback = store.resolveSelectionFromRepoUrl('https://unknown.example/repo', [nip34Repo]);
    expect(fallback.source).toBe('manual');
  });
});
