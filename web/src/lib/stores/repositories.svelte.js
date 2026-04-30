import { authState } from '$lib/stores/auth.js';
import { nostr, fetchRepositories } from '$lib/nostr/client.js';
import { api } from '$lib/api/client.js';

export const repositories = $state([]);

export const loading = $state({
  list: false
});

export const error = $state({ value: null });

export const meta = $state({
  lastLoadedAt: null,
  requestSeq: 0,
  authors: null
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
  const authRelays = toRelayList(authState?.relays);
  const relays = authRelays.length > 0 ? authRelays : (nostr.relays || []);

  await nostr.connect(relays);
}

export async function loadRepositories({ authors = null, force = false } = {}) {
  const normalizedAuthors = normalizeAuthors(authors);
  const shouldReload = force || repositories.length === 0 || !sameAuthors(meta.authors, normalizedAuthors);

  if (!shouldReload) {
    return repositories;
  }

  const nextSeq = (meta.requestSeq || 0) + 1;
  meta.requestSeq = nextSeq;
  loading.list = true;
  error.value = null;

  try {
    await ensureRepositoryConnection();
    const fetched = await fetchRepositories({ authors: normalizedAuthors });

    if (meta.requestSeq !== nextSeq) {
      return repositories;
    }

    repositories.length = 0;
    repositories.push(...(Array.isArray(fetched) ? fetched : []));
    meta.lastLoadedAt = Date.now();
    meta.authors = normalizedAuthors;

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
  const coords = [...new Set(
    repoList
      .map(r => r.repoCoordinate)
      .filter(Boolean)
  )];

  if (coords.length === 0) {
    repoList.forEach(r => {
      r.ci = { state: 'unsupported', lookup: null, error: null };
    });
    return;
  }

  // Mark as loading
  repoList.forEach(r => {
    r.ci = r.repoCoordinate
      ? { state: 'loading', lookup: null, error: null }
      : { state: 'unsupported', lookup: null, error: null };
  });

  const currentSeq = ciMeta.requestSeq + 1;
  ciMeta.requestSeq = currentSeq;
  ciLoading.value = true;
  ciError.value = null;

  try {
    const results = await api.lookupRepositoryCI(coords);

    // Check for stale response
    if (ciMeta.requestSeq !== currentSeq) return;

    // Build lookup map
    const lookupMap = new Map();
    for (const result of results) {
      lookupMap.set(result.repo_coordinate, result);
    }

    // Enrich repos
    repoList.forEach(r => {
      if (!r.repoCoordinate) return;
      const lookup = lookupMap.get(r.repoCoordinate);
      if (lookup) {
        const hasRun = !!lookup.latest_run;
        const hasPolicies = lookup.policies?.length > 0;
        r.ci = {
          state: (hasRun || hasPolicies) ? 'ready' : 'empty',
          lookup,
          error: null
        };
      } else {
        r.ci = { state: 'empty', lookup: null, error: null };
      }
    });

    ciMeta.lastLoadedAt = Date.now();
  } catch (err) {
    if (ciMeta.requestSeq === currentSeq) {
      ciError.value = err?.message || 'Failed to load CI status';
      repoList.forEach(r => {
        if (r.repoCoordinate && r.ci?.state === 'loading') {
          r.ci = { state: 'error', lookup: null, error: err?.message };
        }
      });
    }
  } finally {
    if (ciMeta.requestSeq === currentSeq) {
      ciLoading.value = false;
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
