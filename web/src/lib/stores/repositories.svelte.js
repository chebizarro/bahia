import { fetchRepositories } from '$lib/nostr/client.js';
import { currentSystemInfo, loadSystemInfo } from '$lib/stores/system.svelte.js';

export const repositories = $state([]);

export const loading = $state({
  list: false
});

export const error = $state({ value: null });

export const meta = $state({
  lastLoadedAt: null,
  requestSeq: 0,
  authors: null,
  eose: null,
  relayUrls: []
});

// CI enrichment state
export const ciMeta = $state({ requestSeq: 0, lastLoadedAt: null });
export const ciLoading = $state({ value: false });
export const ciError = $state({ value: null });

function normalizeAuthors(authors) {
  if (!Array.isArray(authors) || authors.length === 0) {
    return null;
  }

  return [...new Set(authors.filter(Boolean))].sort();
}

function normalizeUrl(url) {
  return (url || '').trim().replace(/\/+$/, '').toLowerCase();
}

function normalizeRelayUrls(value) {
  const values = Array.isArray(value) ? value : [];
  const seen = new Set();
  const relayUrls = [];
  for (const entry of values) {
    const relay = typeof entry === 'string' ? entry.trim() : '';
    if (!relay || seen.has(relay)) continue;
    seen.add(relay);
    relayUrls.push(relay);
  }
  return relayUrls;
}

function systemRepositoryRelays(info) {
  return normalizeRelayUrls(info?.nostr?.nip34_relays);
}

async function resolveRepositoryRelays() {
  const current = systemRepositoryRelays(currentSystemInfo());
  if (current.length > 0) return current;

  try {
    return systemRepositoryRelays(await loadSystemInfo());
  } catch (err) {
    console.warn('[repositories] Failed to load NIP-34 relay configuration:', err);
    return [];
  }
}

function sameStringArray(a, b) {
  const left = Array.isArray(a) ? a : [];
  const right = Array.isArray(b) ? b : [];
  if (left.length !== right.length) return false;
  return left.every((value, idx) => value === right[idx]);
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
  // Do NOT call nostr.connect() here. Passing user NIP-07 relay URLs replaces the
  // singleton's relay list and closes Bahia control-plane sockets as a side-effect.
  // The singleton's connection lifecycle is managed globally by bootstrap / Settings.
}

export async function loadRepositories({ authors = null, force = false } = {}) {
  const normalizedAuthors = normalizeAuthors(authors);
  const relayUrls = await resolveRepositoryRelays();
  const shouldReload = force || repositories.length === 0 || !sameAuthors(meta.authors, normalizedAuthors) || !sameStringArray(meta.relayUrls, relayUrls);

  if (!shouldReload) {
    return repositories;
  }

  const nextSeq = (meta.requestSeq || 0) + 1;
  meta.requestSeq = nextSeq;
  loading.list = true;
  error.value = null;

  try {
    await ensureRepositoryConnection();
    const fetched = await fetchRepositories({ authors: normalizedAuthors, relayUrls });

    if (meta.requestSeq !== nextSeq) {
      return repositories;
    }

    repositories.length = 0;
    repositories.push(...(Array.isArray(fetched) ? fetched : []));
    meta.eose = fetched?.eose || null;
    meta.lastLoadedAt = Date.now();
    meta.authors = normalizedAuthors;
    meta.relayUrls = relayUrls;

    // Trigger CI enrichment (non-blocking)
    const repos = [...repositories];
    enrichRepositoriesWithCI(repos).then(() => {
      repositories.length = 0;
      repositories.push(...repos);
    });

    return repositories;
  } catch (err) {
    if (meta.requestSeq === nextSeq) {
      error.value = err?.message || 'Failed to load repositories';
    }
    return repositories;
  } finally {
    if (meta.requestSeq === nextSeq) {
      loading.list = false;
    }
  }
}

export async function enrichRepositoriesWithCI(repoList) {
  const currentSeq = ciMeta.requestSeq + 1;
  ciMeta.requestSeq = currentSeq;
  ciLoading.value = false;
  ciError.value = null;

  repoList.forEach(r => {
    r.ci = r.repoCoordinate
      ? { state: 'empty', lookup: null, error: null }
      : { state: 'unsupported', lookup: null, error: null };
  });

  if (ciMeta.requestSeq === currentSeq) {
    ciMeta.lastLoadedAt = Date.now();
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
