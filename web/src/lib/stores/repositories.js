import { writable, get } from 'svelte/store';
import { authState } from '$lib/stores/auth.js';
import { nostr, fetchRepositories } from '$lib/nostr/client.js';

export const repositories = writable([]);

export const loading = writable({
  list: false
});

export const error = writable(null);

export const meta = writable({
  lastLoadedAt: null,
  requestSeq: 0,
  authors: null
});

function normalizeAuthors(authors) {
  if (!Array.isArray(authors) || authors.length === 0) {
    return null;
  }

  return [...new Set(authors.filter(Boolean))].sort();
}

function normalizeUrl(url) {
  return (url || '').trim().replace(/\/+$/, '').toLowerCase();
}

function toRelayList(relaysRecord) {
  if (!relaysRecord || typeof relaysRecord !== 'object') {
    return [];
  }

  return Object.entries(relaysRecord)
    .filter(([url, policy]) => {
      if (!url) return false;
      if (policy == null) return true;
      if (typeof policy.read === 'boolean') return policy.read;
      if (typeof policy === 'boolean') return policy;
      return true;
    })
    .map(([url]) => url);
}

function sameAuthors(a, b) {
  const left = normalizeAuthors(a);
  const right = normalizeAuthors(b);

  if (left === null && right === null) return true;
  if (left === null || right === null) return false;
  if (left.length !== right.length) return false;

  return left.every((value, idx) => value === right[idx]);
}

export async function ensureRepositoryConnection() {
  const state = get(authState);
  const authRelays = toRelayList(state?.relays);
  const relays = authRelays.length > 0 ? authRelays : (nostr.relays || []);

  await nostr.connect(relays);
}

export async function loadRepositories({ authors = null, force = false } = {}) {
  const currentRepos = get(repositories);
  const currentMeta = get(meta);
  const normalizedAuthors = normalizeAuthors(authors);
  const shouldReload = force || currentRepos.length === 0 || !sameAuthors(currentMeta.authors, normalizedAuthors);

  if (!shouldReload) {
    return currentRepos;
  }

  const nextSeq = (currentMeta.requestSeq || 0) + 1;
  meta.update((m) => ({ ...m, requestSeq: nextSeq }));
  loading.update((l) => ({ ...l, list: true }));
  error.set(null);

  try {
    await ensureRepositoryConnection();
    const fetched = await fetchRepositories({ authors: normalizedAuthors });

    if (get(meta).requestSeq !== nextSeq) {
      return get(repositories);
    }

    repositories.set(Array.isArray(fetched) ? fetched : []);
    meta.update((m) => ({
      ...m,
      lastLoadedAt: Date.now(),
      authors: normalizedAuthors
    }));

    return get(repositories);
  } catch (err) {
    if (get(meta).requestSeq === nextSeq) {
      error.set(err?.message || 'Failed to load repositories');
    }
    return get(repositories);
  } finally {
    if (get(meta).requestSeq === nextSeq) {
      loading.update((l) => ({ ...l, list: false }));
    }
  }
}

export function createManualRepositorySelection(url) {
  const repoUrl = (url || '').trim();

  return {
    source: 'manual',
    provider: 'manual',
    repoUrl,
    displayName: repoUrl
  };
}

export function createNip34RepositorySelection(repo) {
  const repoUrl = repo?.primaryUrl || repo?.cloneUrls?.[0] || repo?.webUrls?.[0] || '';

  return {
    source: 'nip34',
    provider: 'nostr',
    repoUrl,
    displayName: repo?.displayName || repo?.name || repo?.identifier || repoUrl,
    repoCoordinate: repo?.repoCoordinate,
    cloneUrl: repo?.cloneUrls?.[0],
    webUrl: repo?.webUrls?.[0],
    relayUrls: Array.isArray(repo?.relayUrls) ? repo.relayUrls : [],
    maintainers: Array.isArray(repo?.maintainers) ? repo.maintainers : [],
    raw: repo || null
  };
}

export function resolveSelectionFromRepoUrl(repoUrl, repoList) {
  const manual = createManualRepositorySelection(repoUrl);
  const normalizedTarget = normalizeUrl(repoUrl);

  if (!normalizedTarget || !Array.isArray(repoList) || repoList.length === 0) {
    return manual;
  }

  const match = repoList.find((repo) => {
    const candidates = [
      repo?.primaryUrl,
      ...(Array.isArray(repo?.cloneUrls) ? repo.cloneUrls : []),
      ...(Array.isArray(repo?.webUrls) ? repo.webUrls : [])
    ];

    return candidates.some((candidate) => normalizeUrl(candidate) === normalizedTarget);
  });

  return match ? createNip34RepositorySelection(match) : manual;
}

export function filterRepositories(repoList, query, { requirePrimaryUrl = false } = {}) {
  const normalizedQuery = (query || '').trim().toLowerCase();

  return (Array.isArray(repoList) ? repoList : []).filter((repo) => {
    if (requirePrimaryUrl && !repo?.primaryUrl) {
      return false;
    }

    if (!normalizedQuery) {
      return true;
    }

    const haystack = repo?.searchText || [
      repo?.displayName,
      repo?.name,
      repo?.identifier,
      repo?.description,
      repo?.primaryUrl,
      repo?.repoCoordinate,
      ...(Array.isArray(repo?.cloneUrls) ? repo.cloneUrls : []),
      ...(Array.isArray(repo?.webUrls) ? repo.webUrls : [])
    ].filter(Boolean).join(' ').toLowerCase();

    return haystack.includes(normalizedQuery);
  });
}
